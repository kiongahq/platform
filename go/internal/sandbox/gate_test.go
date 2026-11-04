package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

// testToken is long enough for every environment and is no real credential.
var testToken = strings.Repeat("t0ken", 8)

const (
	testImage    = "ghcr.io/kiongahq/sandbox-base@sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRun      = "atr-aaaaaaaaaaaaaaaaaaaaaaaa"
	testOtherRun = "atr-bbbbbbbbbbbbbbbbbbbbbbbb"
	testWorker   = "atw-cccccccccccccccccccccccc"
	testProject  = "p1"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func validEnv(extra map[string]string) func(string) string {
	values := map[string]string{"KIONGA_SANDBOX_TOKEN": testToken, "KIONGA_SANDBOX_IMAGES": testImage, "KIONGA_SANDBOX_GIT_HOSTS": "github.com,git.example:8443"}
	for key, value := range extra {
		if value == "" {
			delete(values, key)
		} else {
			values[key] = value
		}
	}
	return env(values)
}

func TestConfigDefaultsMatchTheSpec(t *testing.T) {
	config, err := LoadConfig(validEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if config.Runtime != "runsc" || config.AllowUnsafe || config.Pids != 512 || config.MemoryBytes != 2<<30 || config.NanoCPUs != 2e9 || config.TTL != 24*time.Hour || config.Port != "8088" || config.Driver != "docker" {
		t.Fatalf("defaults: %+v", config)
	}
	if defaultExecTimeout != 120*time.Second || maxExecTimeout != 1800*time.Second || outputCap != 64<<10 {
		t.Fatal("exec limits differ from the contract")
	}
}

func TestConfigRefusals(t *testing.T) {
	cases := map[string]map[string]string{
		"no token":                {"KIONGA_SANDBOX_TOKEN": ""},
		"short token":             {"KIONGA_SANDBOX_TOKEN": "short"},
		"short production token":  {"KIONGA_SANDBOX_TOKEN": strings.Repeat("x", 20), "KIONGA_ENVIRONMENT": "production"},
		"unsafe in production":    {"KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME": "1", "KIONGA_ENVIRONMENT": "production"},
		"unsafe not exactly 1":    {"KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME": "true"},
		"tag image":               {"KIONGA_SANDBOX_IMAGES": "ghcr.io/kiongahq/sandbox-base:latest"},
		"bare name image":         {"KIONGA_SANDBOX_IMAGES": "debian"},
		"one bad image of two":    {"KIONGA_SANDBOX_IMAGES": testImage + ",debian"},
		"bad helper image":        {"KIONGA_SANDBOX_HELPER_IMAGE": "debian:latest"},
		"pids over ceiling":       {"KIONGA_SANDBOX_PIDS": "4097"},
		"memory over ceiling":     {"KIONGA_SANDBOX_MEMORY_BYTES": strconv.FormatInt(maxMemoryBytes+1, 10)},
		"cpus over ceiling":       {"KIONGA_SANDBOX_CPUS": "17"},
		"tmp over ceiling":        {"KIONGA_SANDBOX_TMP_BYTES": strconv.FormatInt(maxTmpBytes+1, 10)},
		"ttl over ceiling":        {"KIONGA_SANDBOX_TTL_SECONDS": strconv.Itoa(maxTTLSeconds + 1)},
		"negative pids":           {"KIONGA_SANDBOX_PIDS": "-1"},
		"driver":                  {"KIONGA_SANDBOX_DRIVER": "lxc"},
		"kubernetes no namespace": {"KIONGA_SANDBOX_DRIVER": "kubernetes"},
		"kubernetes local digest": {"KIONGA_SANDBOX_DRIVER": "kubernetes", "KIONGA_SANDBOX_NAMESPACE": "x", "KIONGA_SANDBOX_IMAGES": "sha256:" + strings.Repeat("a", 64)},
		"git host with path":      {"KIONGA_SANDBOX_GIT_HOSTS": "github.com/org"},
		"git host with scheme":    {"KIONGA_SANDBOX_GIT_HOSTS": "https://github.com"},
		"git network none":        {"KIONGA_SANDBOX_GIT_NETWORK": "none"},
		"bad prefix":              {"KIONGA_SANDBOX_NAME_PREFIX": "Bad Prefix"},
		"bad runtime name":        {"KIONGA_SANDBOX_RUNTIME": "run sc;"},
		"missing git CA":          {"KIONGA_SANDBOX_GIT_CA_FILE": "/nonexistent/ca.pem"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(validEnv(extra)); err == nil {
				t.Fatal("expected a refusal")
			} else if strings.Contains(err.Error(), testToken) {
				t.Fatal("the error carries the token")
			}
		})
	}
}

func TestConfigAcceptsCommaSeparatedListsAndEmptyImages(t *testing.T) {
	config, err := LoadConfig(validEnv(map[string]string{"KIONGA_SANDBOX_IMAGES": " " + testImage + " , " + testImage + " ", "KIONGA_SANDBOX_GIT_HOSTS": "GitHub.com, gitlab.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Images) != 2 || strings.Join(config.GitHosts, ",") != "github.com,gitlab.com" {
		t.Fatalf("%+v", config)
	}
	// Deploy starts the manager with no images configured: it stays up and refuses creates.
	if config, err = LoadConfig(validEnv(map[string]string{"KIONGA_SANDBOX_IMAGES": ""})); err != nil || len(config.Images) != 0 {
		t.Fatalf("%+v, %v", config, err)
	}
	if _, err := LoadConfig(validEnv(map[string]string{"KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME": "1", "KIONGA_SANDBOX_RUNTIME": "crun"})); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRefusesToStartWithoutAToken(t *testing.T) {
	if _, err := LoadConfig(env(map[string]string{"KIONGA_SANDBOX_IMAGES": testImage})); err == nil {
		t.Fatal("the manager must refuse to start without a token")
	}
}

type fakeDriver struct {
	mu          sync.Mutex
	registered  bool
	listErr     error
	createErr   error
	kernel      string
	instances   map[string]*Instance
	specs       []ContainerSpec
	volumes     map[string]map[string]string
	execs       []execCall
	onExec      func(call execCall, request ExecRequest) (int, error)
	removed     []string
	restarted   []string
	removedVols []string
	networks    []string
	networkErr  error
}

type execCall struct {
	Name  string
	Argv  []string
	Stdin string
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{registered: true, kernel: "4.19.0-gvisor", instances: map[string]*Instance{}, volumes: map[string]map[string]string{}}
}

func (f *fakeDriver) Name() string { return "docker" }

func (f *fakeDriver) RuntimeRegistered(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registered, nil
}

func (f *fakeDriver) Create(_ context.Context, spec ContainerSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if f.createErr != nil {
		return f.createErr
	}
	if _, ok := f.instances[spec.Name]; ok {
		return ErrExists
	}
	f.instances[spec.Name] = &Instance{Name: spec.Name, Labels: spec.Labels, Running: true, Runtime: spec.Runtime}
	if spec.Volume != nil {
		f.volumes[spec.Volume.Name] = spec.Labels
	}
	return nil
}

func (f *fakeDriver) Inspect(_ context.Context, name string) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found, ok := f.instances[name]
	if !ok {
		return Instance{}, ErrNotFound
	}
	return *found, nil
}

func matches(have, want map[string]string) bool {
	for key, value := range want {
		if have[key] != value {
			return false
		}
	}
	return true
}

func (f *fakeDriver) List(_ context.Context, labels map[string]string) ([]Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []Instance
	for _, item := range f.instances {
		if matches(item.Labels, labels) {
			out = append(out, *item)
		}
	}
	return out, nil
}

func (f *fakeDriver) ListVolumes(_ context.Context, labels map[string]string) ([]VolumeInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []VolumeInfo
	for name, have := range f.volumes {
		if matches(have, labels) {
			out = append(out, VolumeInfo{Name: name, Labels: have})
		}
	}
	return out, nil
}

func (f *fakeDriver) EnsureNetwork(_ context.Context, name string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks = append(f.networks, name)
	return f.networkErr
}

func (f *fakeDriver) Exec(ctx context.Context, name string, request ExecRequest) (int, error) {
	call := execCall{Name: name, Argv: request.Argv}
	if request.Stdin != nil {
		raw, _ := io.ReadAll(request.Stdin)
		call.Stdin = string(raw)
	}
	f.mu.Lock()
	f.execs = append(f.execs, call)
	handler := f.onExec
	kernel := f.kernel
	f.mu.Unlock()
	if containsArg(request.Argv, "uname -r") {
		_, _ = io.WriteString(request.Stdout, kernel+"\n")
		return 0, nil
	}
	if handler != nil {
		return handler(call, request)
	}
	return 0, ctx.Err()
}

func (f *fakeDriver) Restart(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted = append(f.restarted, name)
	if found, ok := f.instances[name]; ok {
		found.Running = true
	}
	return nil
}

func (f *fakeDriver) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	delete(f.instances, name)
	return nil
}

func (f *fakeDriver) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedVols = append(f.removedVols, name)
	delete(f.volumes, name)
	return nil
}

func (f *fakeDriver) specNamed(role string) []ContainerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerSpec
	for _, spec := range f.specs {
		if spec.Labels[LabelRole] == role {
			out = append(out, spec)
		}
	}
	return out
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestService(t *testing.T, extra map[string]string) (*Service, *fakeDriver, *lockedBuffer) {
	t.Helper()
	config, err := LoadConfig(validEnv(extra))
	if err != nil {
		t.Fatal(err)
	}
	driver := newFakeDriver()
	logs := &lockedBuffer{}
	service := NewService(config, driver, log.New(logs, "", 0))
	// Gate tests never touch real DNS.
	service.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("140.82.112.3")}, nil
	}
	return service, driver, logs
}

func createTest(t *testing.T, service *Service) string {
	t.Helper()
	sandbox, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	return sandbox.ID
}

func wantStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	var failure *client.Error
	if !errors.As(err, &failure) || failure.Status != status || failure.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

func TestSandboxSpecIsHardenedAndHasNoHelperRole(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	createTest(t, service)
	specs := driver.specNamed(RoleSandbox)
	if len(specs) != 1 {
		t.Fatalf("specs: %+v", driver.specs)
	}
	spec := specs[0]
	if spec.Network != NetworkNone || spec.Runtime != "runsc" || spec.Image != testImage || spec.Pids != 512 || spec.MemoryBytes != 2<<30 || spec.NanoCPUs != 2e9 {
		t.Fatalf("spec: %+v", spec)
	}
	if spec.Labels[LabelRole] == RoleGitHelper {
		t.Fatal("a sandbox must never carry the git-helper role, which is what grants egress")
	}
	if spec.Volume == nil || spec.Volume.Path != WorkspacePath || !strings.HasSuffix(spec.Volume.Name, "-workspace") {
		t.Fatalf("workspace: %+v", spec.Volume)
	}
	if len(spec.Tmpfs) != 1 || spec.Tmpfs[0].Path != "/tmp" {
		t.Fatalf("tmpfs: %+v", spec.Tmpfs)
	}
}

func TestCreateBodyIsolation(t *testing.T) {
	spec := ContainerSpec{Name: "n", Image: testImage, Runtime: "runsc", Labels: map[string]string{"a": "b"}, Command: []string{"/bin/sh", "-c", "x"}, Network: NetworkNone, MemoryBytes: 2 << 30, NanoCPUs: 2e9, Pids: 512,
		Tmpfs: []TmpfsMount{{Path: "/tmp", SizeBytes: 1024}}, Volume: &VolumeMount{Name: "v", Path: WorkspacePath}}
	raw, err := json.Marshal(createBody(spec))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		User            string
		Env             []string
		NetworkDisabled bool
		HostConfig      struct {
			Runtime        string
			NetworkMode    string
			ReadonlyRootfs bool
			CapDrop        []string
			CapAdd         []string
			SecurityOpt    []string
			Privileged     bool
			PidsLimit      int64
			Memory         int64
			MemorySwap     int64
			NanoCpus       int64
			Tmpfs          map[string]string
			Binds          []string
			Devices        []string
			Mounts         []struct{ Type, Source, Target string }
			RestartPolicy  struct{ Name string }
		}
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	host := body.HostConfig
	switch {
	case host.Runtime != "runsc":
		t.Error("runtime is not set explicitly, so the engine default would apply")
	case host.NetworkMode != NetworkNone || !body.NetworkDisabled:
		t.Error("network must be none")
	case !host.ReadonlyRootfs:
		t.Error("root must be read-only")
	case strings.Join(host.CapDrop, ",") != "ALL" || len(host.CapAdd) != 0:
		t.Error("capabilities must all be dropped")
	case strings.Join(host.SecurityOpt, ",") != "no-new-privileges":
		t.Errorf("security options must be exactly no-new-privileges, keeping default seccomp: %v", host.SecurityOpt)
	case host.Privileged:
		t.Error("must not be privileged")
	case host.PidsLimit != 512 || host.Memory != 2<<30 || host.MemorySwap != host.Memory || host.NanoCpus != 2e9:
		t.Errorf("limits: %+v", host)
	case body.User != "10001:10001":
		t.Errorf("user: %s", body.User)
	case len(host.Binds) != 0 || len(host.Devices) != 0:
		t.Error("no host mounts")
	case len(host.Mounts) != 1 || host.Mounts[0].Type != "volume":
		t.Errorf("mounts: %+v", host.Mounts)
	case !strings.Contains(host.Tmpfs["/tmp"], "size=1024"):
		t.Errorf("tmpfs: %v", host.Tmpfs)
	case host.RestartPolicy.Name != "no":
		t.Error("the engine must not restart a sandbox on its own")
	case len(body.Env) != 1 || !strings.HasPrefix(body.Env[0], "PATH="):
		t.Errorf("environment must hold only PATH: %v", body.Env)
	}
	if strings.Contains(string(raw), ".sock") {
		t.Fatal("an engine socket appears in the create body")
	}
}

func TestFailClosedWhenRuntimeNotRegistered(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	driver.registered = false
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(driver.specs) != 0 {
		t.Fatalf("a container was attempted: %+v", driver.specs)
	}
	if health := service.Health(context.Background()); health.RuntimeVerified {
		t.Fatalf("health: %+v", health)
	}
}

func TestRuntimeCheckedBeforeEachCreateNotOnlyAtStartup(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	createTest(t, service)
	driver.mu.Lock()
	driver.registered = false
	driver.instances = map[string]*Instance{}
	driver.specs = nil
	driver.mu.Unlock()
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testOtherRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(driver.specs) != 0 {
		t.Fatal("a runtime that disappeared after startup still got a container")
	}
}

// The trap on Podman: the engine lists runsc with no binary. Starting fails, and
// nothing may be left. Docker can also have runsc registered to point at runc.
func TestFailClosedWhenRegisteredRuntimeCannotStart(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	driver.createErr = errors.New("executable file not found")
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(driver.instances) != 0 || len(driver.volumes) != 0 {
		t.Fatalf("left: %+v %+v", driver.instances, driver.volumes)
	}
	if len(driver.removed) == 0 {
		t.Fatal("the probe was not cleaned up")
	}
	if health := service.Health(context.Background()); health.RuntimeVerified {
		t.Fatalf("health: %+v", health)
	}
}

func TestFailClosedWhenKernelIsNotGVisor(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	driver.kernel = "6.8.0-1-generic"
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(driver.instances) != 0 || len(driver.volumes) != 0 {
		t.Fatalf("left: %+v %+v", driver.instances, driver.volumes)
	}
	if len(driver.specNamed(RoleSandbox)) != 0 {
		t.Fatal("a sandbox was created after the probe failed")
	}
}

func TestEverySandboxIsAttestedNotOnlyTheProbe(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	createTest(t, service) // the probe passes with the good kernel
	driver.mu.Lock()
	driver.kernel = "6.8.0-1-generic"
	driver.mu.Unlock()
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testOtherRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	id := DeriveID(testProject, testOtherRun, testWorker)
	if _, ok := driver.instances[service.containerName(id)]; ok {
		t.Fatal("a sandbox on the wrong kernel was kept")
	}
	if _, ok := driver.volumes[service.volumeName(id)]; ok {
		t.Fatal("its volume was kept")
	}
}

func TestEmptyImagesStaysUpAndRefuses(t *testing.T) {
	service, driver, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_IMAGES": ""})
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(driver.specs) != 0 {
		t.Fatal("a container was attempted")
	}
	if health := service.Health(context.Background()); health.RuntimeVerified {
		t.Fatalf("health: %+v", health)
	}
}

func TestUnsafeRuntimeReportedAndWarnedOnEveryCreate(t *testing.T) {
	service, driver, logs := newTestService(t, map[string]string{"KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME": "1", "KIONGA_SANDBOX_RUNTIME": "crun"})
	driver.kernel = "6.8.0-1-generic" // unsafe mode skips the kernel comparison
	createTest(t, service)
	createTest(t, service)
	if count := strings.Count(logs.String(), "WARNING"); count != 2 {
		t.Fatalf("want a warning per create, got %d:\n%s", count, logs.String())
	}
	health := service.Health(context.Background())
	if !health.UnsafeRuntime || !health.RuntimeVerified || health.Runtime != "crun" {
		t.Fatalf("health: %+v", health)
	}
}

func TestHealthUnsafeFalseByDefault(t *testing.T) {
	service, _, logs := newTestService(t, nil)
	createTest(t, service)
	health := service.Health(context.Background())
	if health.UnsafeRuntime || !health.RuntimeVerified || health.Driver != "docker" || health.Runtime != "runsc" {
		t.Fatalf("health: %+v", health)
	}
	if strings.Contains(logs.String(), "WARNING") {
		t.Fatal("warned with no unsafe flag")
	}
}

func TestCreateValidationAndAllowlist(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	for _, request := range []client.CreateRequest{
		{RunID: "x", WorkerID: testWorker, ProjectID: testProject},
		{RunID: testRun, WorkerID: "x", ProjectID: testProject},
		{RunID: testRun, WorkerID: testWorker},
		{RunID: testRun + "\n", WorkerID: testWorker, ProjectID: testProject},
		{RunID: testRun, WorkerID: testWorker + "\n", ProjectID: testProject},
	} {
		if _, err := service.Create(context.Background(), request); client.Code(err) != client.CodeInvalid {
			t.Errorf("%+v: %v", request, err)
		}
	}
	for _, image := range []string{"debian:latest", "ghcr.io/evil/image@sha256:" + strings.Repeat("b", 64), "sha256:" + strings.Repeat("a", 64)} {
		_, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject, Image: image})
		wantStatus(t, err, http.StatusForbidden, client.CodeImageNotAllowed)
	}
	if len(driver.specs) != 0 {
		t.Fatal("an invalid create reached the engine")
	}
	if _, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject, Image: testImage}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateIsIdempotent(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	first := createTest(t, service)
	second := createTest(t, service)
	if first != second || len(driver.specNamed(RoleSandbox)) != 1 {
		t.Fatalf("%s %s %+v", first, second, driver.specs)
	}
	if other, err := service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: "atw-dddddddddddddddddddddddd", ProjectID: testProject}); err != nil || other.ID == first {
		t.Fatalf("%+v %v", other, err)
	}
}

func TestSandboxLimit(t *testing.T) {
	service, _, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_MAX_SANDBOXES": "1"})
	createTest(t, service)
	_, err := service.Create(context.Background(), client.CreateRequest{RunID: testOtherRun, WorkerID: testWorker, ProjectID: testProject})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
}

func TestLookupsRefuseForeignIDsAndContainers(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	for _, id := range []string{"", "../../containers/json", "sbx-short", "mlaiops-gateway-1", "sbx-" + strings.Repeat("a", 24) + "\n"} {
		_, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "id"})
		wantStatus(t, err, http.StatusNotFound, client.CodeNotFound)
		if err := service.Delete(context.Background(), id); client.Code(err) != client.CodeNotFound {
			t.Errorf("delete %q: %v", id, err)
		}
	}
	// A container with the right name but no labels of ours is not a sandbox.
	id := "sbx-" + strings.Repeat("a", 24)
	driver.instances[service.containerName(id)] = &Instance{Name: service.containerName(id), Labels: map[string]string{}, Running: true}
	_, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "id"})
	wantStatus(t, err, http.StatusNotFound, client.CodeNotFound)
	if err := service.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.instances[service.containerName(id)]; !ok {
		t.Fatal("delete removed a container that was not ours")
	}
}

func TestExecRefusesWrongRuntimeOnExistingSandbox(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	driver.instances[service.containerName(id)].Runtime = "runc"
	_, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "id"})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if _, err = service.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject}); client.Code(err) != client.CodeUnavailable {
		t.Fatalf("idempotent create returned a sandbox on the wrong runtime: %v", err)
	}
}

func TestExecArgvClearsEnvironmentAndKeepsCommandOneArgument(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	command := `echo "$(id)"; rm -rf / ; '`
	driver.onExec = func(_ execCall, request ExecRequest) (int, error) {
		if request.Argv[len(request.Argv)-1] != command {
			t.Errorf("command was not passed whole: %q", request.Argv)
		}
		_, _ = io.WriteString(request.Stdout, "out")
		_, _ = io.WriteString(request.Stderr, "err")
		return 3, nil
	}
	result, err := service.Exec(context.Background(), id, client.ExecRequest{Command: command})
	if err != nil || result.ExitCode != 3 || result.Stdout != "out" || result.Stderr != "err" || result.TimedOut {
		t.Fatalf("%+v, %v", result, err)
	}
	last := driver.execs[len(driver.execs)-1].Argv
	if last[0] != "/usr/bin/env" || last[1] != "-i" {
		t.Fatalf("the environment is not cleared: %q", last)
	}
	for _, arg := range last[:len(last)-1] {
		if strings.Contains(arg, "TOKEN") || strings.Contains(arg, testToken) {
			t.Fatalf("a secret reached argv: %q", last)
		}
	}
}

func TestExecRequestLimits(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	before := len(driver.execs)
	for _, request := range []client.ExecRequest{
		{},
		{Command: "x", TimeoutSeconds: -1},
		{Command: "x", TimeoutSeconds: 1801},
		{Command: strings.Repeat("x", maxCommand+1)},
		{Command: "a\x00b"},
	} {
		wantStatus(t, execIgnoringResult(service, id, request), http.StatusBadRequest, client.CodeInvalid)
	}
	if len(driver.execs) != before {
		t.Fatal("an invalid exec reached the engine")
	}
}

func execIgnoringResult(service *Service, id string, request client.ExecRequest) error {
	_, err := service.Exec(context.Background(), id, request)
	return err
}

func TestTimeoutSweepsEverythingNotOnlyTheGroup(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	sweeps := 0
	blocking := &blockingDriver{fakeDriver: driver, onSweep: func(stdout io.Writer) (int, error) {
		sweeps++
		_, _ = io.WriteString(stdout, "3\n")
		return 0, nil
	}}
	service.driver = blocking
	result, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "sleep 100", TimeoutSeconds: 1})
	if err != nil || !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("%+v, %v", result, err)
	}
	if sweeps != 1 {
		t.Fatalf("sweeps: %d", sweeps)
	}
	if len(driver.restarted) != 0 {
		t.Fatal("a clean sweep should not restart")
	}
	if !strings.Contains(sweepScript, "kill -KILL -1") {
		t.Fatal("the sweep must signal every process, because setsid escapes a process group")
	}
}

// blockingDriver makes user commands wait for ctx, as a real hung process does.
type blockingDriver struct {
	*fakeDriver
	onSweep func(io.Writer) (int, error)
}

func (b *blockingDriver) Exec(ctx context.Context, name string, request ExecRequest) (int, error) {
	if containsArg(request.Argv, sweepScript) {
		return b.onSweep(request.Stdout)
	}
	if containsArg(request.Argv, "uname -r") {
		return b.fakeDriver.Exec(ctx, name, request)
	}
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestTimeoutRestartsWhenSweepFailsOrAnythingSurvives(t *testing.T) {
	cases := map[string]func(io.Writer) (int, error){
		"exec cannot start at the pid limit": func(io.Writer) (int, error) { return 0, errors.New("cannot fork") },
		"too many survivors": func(stdout io.Writer) (int, error) {
			_, _ = fmt.Fprintf(stdout, "%d\n", maxSweepSurvivors+1)
			return 0, nil
		},
		"unreadable count": func(stdout io.Writer) (int, error) {
			_, _ = io.WriteString(stdout, "not a number")
			return 0, nil
		},
		"nonzero exit": func(io.Writer) (int, error) { return 1, nil },
	}
	for name, sweep := range cases {
		t.Run(name, func(t *testing.T) {
			service, driver, _ := newTestService(t, nil)
			id := createTest(t, service)
			service.driver = &blockingDriver{fakeDriver: driver, onSweep: sweep}
			result, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "x", TimeoutSeconds: 1})
			if err != nil || !result.TimedOut {
				t.Fatalf("%+v, %v", result, err)
			}
			if len(driver.restarted) != 1 {
				t.Fatalf("restarts: %v", driver.restarted)
			}
		})
	}
}

func TestCancelledCallerStillKillsTheCommand(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	sweeps := make(chan struct{}, 1)
	service.driver = &blockingDriver{fakeDriver: driver, onSweep: func(stdout io.Writer) (int, error) {
		sweeps <- struct{}{}
		_, _ = io.WriteString(stdout, "3\n")
		return 0, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := service.Exec(ctx, id, client.ExecRequest{Command: "x", TimeoutSeconds: 60}); err == nil {
		t.Fatal("expected an error")
	}
	select {
	case <-sweeps:
	default:
		t.Fatal("a caller that went away left the command running")
	}
}

func TestOutputCappedAndFloodStopped(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	driver.onExec = func(_ execCall, request ExecRequest) (int, error) {
		_, _ = io.WriteString(request.Stdout, "HEAD"+strings.Repeat("x", 200<<10)+"TAIL")
		_, _ = io.WriteString(request.Stderr, strings.Repeat("\xff", 200<<10))
		return 0, nil
	}
	result, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "x"})
	if err != nil || !result.Truncated {
		t.Fatalf("%+v %v", result.Truncated, err)
	}
	if len(result.Stdout) > outputCap || len([]rune(result.Stderr)) > outputCap {
		t.Fatalf("cap exceeded: %d %d", len(result.Stdout), len([]rune(result.Stderr)))
	}
	if !strings.HasPrefix(result.Stdout, "HEAD") || !strings.HasSuffix(result.Stdout, "TAIL") {
		t.Fatal("head and tail must both survive")
	}
	if !json.Valid(mustJSON(t, result)) || strings.ContainsRune(result.Stderr, 0xFFFD) == false {
		t.Fatal("invalid UTF-8 must be replaced so the response is valid JSON")
	}

	flood := newCapture(outputCap)
	fired := 0
	flood.flood, flood.onFlood = 1024, func() { fired++ }
	_, _ = flood.Write(make([]byte, 2048))
	_, _ = flood.Write(make([]byte, 2048))
	if fired != 1 {
		t.Fatalf("flood fired %d times", fired)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCaptureSmallWritesKeepOrder(t *testing.T) {
	capture := newCapture(16)
	for index := 0; index < 40; index++ {
		_, _ = capture.Write([]byte{byte('a' + index%26)})
	}
	text := capture.String()
	if !strings.HasPrefix(text, "abcdefgh") || !strings.HasSuffix(text, string([]byte{byte('a' + 39%26)})) {
		t.Fatalf("%q", text)
	}
	whole := newCapture(16)
	_, _ = whole.Write([]byte("short"))
	if whole.String() != "short" || whole.truncated() {
		t.Fatalf("%q", whole.String())
	}
}

func TestWorkspacePathLexicalRules(t *testing.T) {
	good := map[string]string{".": "/workspace", "/workspace": "/workspace", "/workspace/": "/workspace", "a/b": "/workspace/a/b", "/workspace/a b/c": "/workspace/a b/c", "a..b": "/workspace/a..b", "...": "/workspace/...", "-rf": "/workspace/-rf"}
	for raw, want := range good {
		if got, err := workspacePath(raw); err != nil || got != want {
			t.Errorf("%q: %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "..", "../x", "a/../b", "/workspace/..", "/workspace/../etc", "/etc/passwd", "/", "/workspaces/x", "/workspacex", "a\n", "a\nb", "a\x00b", "a\tb", "\x7f", "a/\r", strings.Repeat("a", 4097)} {
		if got, err := workspacePath(raw); client.Code(err) != client.CodePathRejected {
			t.Errorf("%q: accepted as %q, %v", raw, got, err)
		}
	}
}

func TestBranchRule(t *testing.T) {
	if err := checkBranch("kionga/"+testRun+"/fix-1.2_x", testRun); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{
		"main", "master", "HEAD", "refs/heads/main",
		"kionga/" + testOtherRun + "/x", // another run's namespace
		"kionga/" + testRun, "kionga/" + testRun + "/", "kionga/" + testRun + "/a/b",
		"kionga/" + testRun + "/x\n", "\nkionga/" + testRun + "/x", "kionga/" + testRun + "/x\x00", "kionga/" + testRun + "/x y",
		"kionga/" + testRun + "/x\t", "kionga/" + testRun + "/..", "kionga/" + testRun + "/a..b", "kionga/" + testRun + "/x.lock",
		"+kionga/" + testRun + "/x", ":kionga/" + testRun + "/x", "kionga/" + testRun + "/x:main", "kionga/" + testRun + "/-x",
		"kionga/" + testRun + "/" + strings.Repeat("x", 300),
	} {
		if err := checkBranch(branch, testRun); client.Code(err) != client.CodeBranchRejected {
			t.Errorf("%q: %v", branch, err)
		}
	}
}

func TestRepositoryRules(t *testing.T) {
	hosts := []string{"github.com", "git.example:8443"}
	good := map[string]string{
		"https://github.com/o/r":       "https://github.com/o/r",
		"https://github.com/o/r.git":   "https://github.com/o/r.git",
		"https://GITHUB.com/o/r/":      "https://github.com/o/r",
		"https://github.com:443/o/r":   "https://github.com:443/o/r",
		"https://git.example:8443/o/r": "https://git.example:8443/o/r",
	}
	for raw, want := range good {
		if got, err := canonicalRepository(raw, hosts); err != nil || got != want {
			t.Errorf("%q: %q, %v", raw, got, err)
		}
	}
	notAllowed := []string{"https://evil.example/o/r", "https://github.com.evil.example/o/r", "https://git.example/o/r", "https://git.example:9000/o/r", "https://github.com:8443/o/r", "https://evilgithub.com/o/r"}
	for _, raw := range notAllowed {
		if _, err := canonicalRepository(raw, hosts); client.Code(err) != client.CodeHostNotAllowed {
			t.Errorf("%q: %v", raw, err)
		}
	}
	bad := []string{"http://github.com/o/r", "ssh://github.com/o/r", "git://github.com/o/r", "file:///x", "ext::sh -c id", "github.com/o/r", "git@github.com:o/r.git", "https://u:p@github.com/o/r", "https://u@github.com/o/r", "https://github.com@evil.example/o/r",
		"https://github.com/o/r?x=1", "https://github.com/o/r#x", "https://github.com/o/../r", "https://github.com/o/./r", "https://github.com/o/r\n", "https://github.com/o/r\x00", "https://github.com/o r", "https://github.com/%2e%2e/r", "--upload-pack=x", "https://github.com", "https://github.com/", "https://github.com\\@evil.example/r", ""}
	for _, raw := range bad {
		if _, err := canonicalRepository(raw, hosts); client.Code(err) != client.CodeInvalid {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if _, err := canonicalRepository("https://github.com/o/r", nil); client.Code(err) != client.CodeHostNotAllowed {
		t.Fatalf("an empty allowlist must allow nothing: %v", err)
	}
	if dir, err := repositoryDir("https://github.com/o/r.git"); err != nil || dir != "/workspace/r" {
		t.Fatalf("%q %v", dir, err)
	}
	if _, err := repositoryDir("https://github.com/o/.git"); err == nil {
		t.Fatal("a repository with no usable name must be refused")
	}
}

func gitFixture(t *testing.T, service *Service, driver *fakeDriver) string {
	t.Helper()
	id := createTest(t, service)
	driver.mu.Lock()
	driver.onExec = func(call execCall, request ExecRequest) (int, error) {
		switch {
		case containsArg(call.Argv, helperCloneScript):
			_, _ = io.WriteString(request.Stdout, "5\n")
		case containsArg(call.Argv, ingestScript):
			_, _ = io.WriteString(request.Stdout, strings.Repeat("a", 40)+"\n")
		case call.Argv[len(call.Argv)-1] == helperScratch+"/out.bundle":
			_, _ = io.WriteString(request.Stdout, "BUNDL")
		case containsArg(call.Argv, bundleScript):
			_, _ = io.WriteString(request.Stdout, "/workspace/.kionga-xfer.abc123\n5\n")
		case len(call.Argv) >= 2 && call.Argv[len(call.Argv)-1] == "/workspace/.kionga-xfer.abc123" && containsArg(call.Argv, "/bin/cat"):
			_, _ = io.WriteString(request.Stdout, "BUNDL")
		case containsArg(call.Argv, helperPushScript):
			_, _ = io.WriteString(request.Stdout, strings.Repeat("b", 40)+"\n")
		}
		return 0, nil
	}
	driver.mu.Unlock()
	return id
}

func secretValue() string { return strings.Join([]string{"s3cret", "value", "for", "tests"}, "-") }

func assertSecretOnlyInHelperStdin(t *testing.T, service *Service, driver *fakeDriver, secret string) {
	t.Helper()
	sawSecret := false
	for _, call := range driver.execs {
		for _, arg := range call.Argv {
			if strings.Contains(arg, secret) {
				t.Fatalf("the credential appeared in argv: %q", call.Argv)
			}
		}
		if strings.Contains(call.Stdin, secret) {
			if !strings.Contains(call.Name, "git-") {
				t.Fatalf("the credential was sent to %s", call.Name)
			}
			sawSecret = true
		}
	}
	if !sawSecret {
		t.Fatal("the helper never received the credential")
	}
	for _, spec := range driver.specs {
		if strings.Contains(fmt.Sprintf("%+v", spec), secret) {
			t.Fatalf("the credential appeared in a container spec: %+v", spec)
		}
	}
}

func TestGitCloneUsesIsolatedHelper(t *testing.T) {
	service, driver, logs := newTestService(t, nil)
	id := gitFixture(t, service, driver)
	secret := secretValue()
	result, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r.git", Ref: "main", Credential: client.Credential(secret)})
	if err != nil || result.Commit != strings.Repeat("a", 40) {
		t.Fatalf("%+v, %v", result, err)
	}
	helpers := driver.specNamed(RoleGitHelper)
	if len(helpers) != 1 {
		t.Fatalf("helpers: %+v", driver.specs)
	}
	helper := helpers[0]
	if helper.Volume != nil {
		t.Fatal("the helper must not mount the workspace: it must never run git in a .git the agent touched")
	}
	if helper.Network != "kionga-sandbox-git" || len(driver.networks) == 0 || driver.networks[0] != helper.Network {
		t.Fatalf("the helper must be on its own ensured network: %q %v", helper.Network, driver.networks)
	}
	if len(helper.ExtraHosts) != 1 || helper.ExtraHosts[0] != (HostEntry{Host: "github.com", Address: "140.82.112.3"}) {
		t.Fatalf("the helper must use the address the manager checked: %+v", helper.ExtraHosts)
	}
	for _, spec := range driver.specNamed(RoleSandbox) {
		if len(spec.ExtraHosts) != 0 {
			t.Fatal("a sandbox has no network and needs no hosts")
		}
	}
	if helper.Runtime != "runsc" {
		t.Fatalf("the helper must use the same runtime: %q", helper.Runtime)
	}
	if helper.Labels[LabelID] != id {
		t.Fatal("the helper must be findable by sandbox id for cleanup")
	}
	assertSecretOnlyInHelperStdin(t, service, driver, secret)
	if len(driver.removed) == 0 || !strings.Contains(driver.removed[len(driver.removed)-1], "git-") {
		t.Fatalf("the helper was not removed: %v", driver.removed)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("the credential reached the log")
	}
}

func TestGitPushSequenceAndEnvironment(t *testing.T) {
	service, driver, logs := newTestService(t, nil)
	id := gitFixture(t, service, driver)
	secret := secretValue()
	branch := "kionga/" + testRun + "/fix"
	result, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: branch, Credential: client.Credential(secret)})
	if err != nil || result.Branch != branch || result.Commit != strings.Repeat("b", 40) {
		t.Fatalf("%+v, %v", result, err)
	}
	if specs := driver.specNamed(RoleGitHelper); len(specs) != 1 || specs[0].Volume != nil {
		t.Fatalf("helper: %+v", specs)
	}
	assertSecretOnlyInHelperStdin(t, service, driver, secret)
	sandboxName := service.containerName(id)
	var bundled, pushed bool
	for _, call := range driver.execs {
		if containsArg(call.Argv, bundleScript) {
			if call.Name != sandboxName {
				t.Fatalf("the bundle must be created in the sandbox, not %s", call.Name)
			}
			if pushed {
				t.Fatal("the bundle was created after the push")
			}
			bundled = true
		}
		if containsArg(call.Argv, helperPushScript) {
			if call.Name == sandboxName {
				t.Fatal("the push must run in the helper")
			}
			joined := strings.Join(call.Argv, " ")
			for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https", "GIT_ASKPASS=/run/kionga/askpass"} {
				if !strings.Contains(joined, want) {
					t.Errorf("push environment lacks %s", want)
				}
			}
			pushed = true
		}
	}
	if !bundled || !pushed {
		t.Fatalf("bundled %v, pushed %v", bundled, pushed)
	}
	var removedBundle bool
	for _, call := range driver.execs {
		removedBundle = removedBundle || (containsArg(call.Argv, "/bin/rm") && containsArg(call.Argv, "/workspace/.kionga-xfer.abc123"))
	}
	if !removedBundle {
		t.Fatal("the temporary bundle was left in the workspace")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("the credential reached the log")
	}
}

func TestHardenedGitScriptsCloseCodeExecutionPaths(t *testing.T) {
	for _, want := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "credential.helper=", "http.followRedirects=false"} {
		if !strings.Contains(hardenedGit, want) {
			t.Errorf("the helper's git lacks %s", want)
		}
	}
	for name, script := range map[string]string{"clone": helperCloneScript, "push": helperPushScript} {
		if !strings.HasPrefix(script, hardenedGit) {
			t.Errorf("%s does not use the hardened wrapper", name)
		}
		for _, line := range strings.Split(strings.TrimPrefix(script, hardenedGit), "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "git" {
				t.Errorf("%s calls unhardened git: %s", name, line)
			}
		}
	}
	if !strings.Contains(helperPushScript, `"refs/kionga/incoming:refs/heads/$2"`) {
		t.Error("the push refspec must have a fixed non-empty source and no +, so no force and no delete")
	}
	for _, line := range strings.Split(helperPushScript, "\n") {
		if strings.Contains(line, " push ") && (strings.Contains(line, "--force") || strings.Contains(line, "--mirror") || strings.Contains(line, " -f ") || strings.Contains(line, "--delete") || strings.Contains(line, "\"+")) {
			t.Errorf("the push must not force or delete: %s", line)
		}
	}
	if strings.Contains(helperCloneScript, "--recurse") {
		t.Error("the clone must not recurse into submodules")
	}
	if strings.Contains(helperCloneScript, "checkout") {
		t.Error("the helper must not check out repository content")
	}
}

func TestGitRejectionsNeverStartAHelper(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := gitFixture(t, service, driver)
	secret := client.Credential(secretValue())
	cases := map[string]func() error{
		"disallowed host": func() error {
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://evil.example/o/r", Branch: "kionga/" + testRun + "/x", Credential: secret})
			return err
		},
		"another run's branch": func() error {
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testOtherRun + "/x", Credential: secret})
			return err
		},
		"trailing newline": func() error {
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x\n", Credential: secret})
			return err
		},
		"default branch": func() error {
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "main", Credential: secret})
			return err
		},
		"no credential on push": func() error {
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x"})
			return err
		},
		"newline in credential": func() error {
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x", Credential: "a\nb"})
			return err
		},
		"clone disallowed host": func() error {
			_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://evil.example/o/r", Credential: secret})
			return err
		},
		"clone option as ref": func() error {
			_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r", Ref: "--upload-pack=x", Credential: secret})
			return err
		},
		"clone ref newline": func() error {
			_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r", Ref: "main\n", Credential: secret})
			return err
		},
		"clone ref range": func() error {
			_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r", Ref: "a..b", Credential: secret})
			return err
		},
	}
	for name, attempt := range cases {
		t.Run(name, func(t *testing.T) {
			if err := attempt(); err == nil {
				t.Fatal("expected a rejection")
			}
			if helpers := driver.specNamed(RoleGitHelper); len(helpers) != 0 {
				t.Fatalf("a helper started: %+v", helpers)
			}
		})
	}
}

func TestGitFailureMessageIsScrubbedAndHelperRemoved(t *testing.T) {
	service, driver, logs := newTestService(t, nil)
	id := gitFixture(t, service, driver)
	secret := secretValue()
	driver.mu.Lock()
	inner := driver.onExec
	driver.onExec = func(call execCall, request ExecRequest) (int, error) {
		if containsArg(call.Argv, helperCloneScript) {
			_, _ = io.WriteString(request.Stderr, "fatal: could not read from https://x:"+secret+"@github.com/o/r\n")
			return 128, nil
		}
		return inner(call, request)
	}
	driver.mu.Unlock()
	_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r", Credential: client.Credential(secret)})
	wantStatus(t, err, http.StatusBadGateway, client.CodeGitFailed)
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("the credential leaked: %v", err)
	}
	removed := false
	for _, name := range driver.removed {
		removed = removed || strings.Contains(name, "git-")
	}
	if !removed {
		t.Fatal("a failed clone left its credentialed helper running")
	}
}

func TestPushRejectsMalformedSandboxOutput(t *testing.T) {
	cases := map[string]string{
		"path outside the workspace": "/etc/passwd\n5\n",
		"path with traversal":        "/workspace/.kionga-xfer.a/../../etc/passwd\n5\n",
		"not a number":               "/workspace/.kionga-xfer.abc\nfive\n",
		"negative":                   "/workspace/.kionga-xfer.abc\n-5\n",
		"extra lines":                "/workspace/.kionga-xfer.abc\n5\nextra\n",
		"empty":                      "",
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			service, driver, _ := newTestService(t, nil)
			id := createTest(t, service)
			driver.onExec = func(call execCall, request ExecRequest) (int, error) {
				if containsArg(call.Argv, bundleScript) {
					_, _ = io.WriteString(request.Stdout, output)
				}
				return 0, nil
			}
			_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x", Credential: "secret-value"})
			if client.Code(err) != client.CodeGitFailed {
				t.Fatalf("got %v", err)
			}
			if helpers := driver.specNamed(RoleGitHelper); len(helpers) != 0 {
				t.Fatal("hostile sandbox output still started a helper")
			}
		})
	}
}

func TestPushRefusesOversizeBundleBeforeHelper(t *testing.T) {
	service, driver, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_GIT_BUNDLE_BYTES": "4"})
	id := createTest(t, service)
	driver.onExec = func(call execCall, request ExecRequest) (int, error) {
		if containsArg(call.Argv, bundleScript) {
			_, _ = io.WriteString(request.Stdout, "/workspace/.kionga-xfer.abc\n5\n")
		}
		return 0, nil
	}
	_, err := service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x", Credential: "secret-value"})
	wantStatus(t, err, http.StatusRequestEntityTooLarge, client.CodeTooLarge)
	if len(driver.specNamed(RoleGitHelper)) != 0 {
		t.Fatal("an oversize bundle started a helper")
	}
}

func TestSizeLimitWriterStopsAHostileSender(t *testing.T) {
	var out bytes.Buffer
	limited := &sizeLimitWriter{w: &out, remaining: 4}
	if _, err := limited.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Write([]byte("cdef")); !errors.Is(err, errStreamFull) {
		t.Fatalf("got %v", err)
	}
	if out.String() != "abcd" {
		t.Fatalf("%q", out.String())
	}
}

func TestPathScriptExitsMapToCodes(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	cases := map[int]string{exitPathEscape: client.CodePathRejected, exitResolveFailed: client.CodePathRejected, exitNotFound: client.CodeNotFound, exitSpecialFile: client.CodeInvalid}
	for exit, code := range cases {
		driver.onExec = func(execCall, ExecRequest) (int, error) { return exit, nil }
		if _, err := service.ReadPath(context.Background(), id, "x"); client.Code(err) != code {
			t.Errorf("exit %d: %v", exit, err)
		}
	}
	driver.onExec = func(execCall, ExecRequest) (int, error) { return exitPathEscape, nil }
	if _, err := service.WriteFile(context.Background(), id, "x", []byte("y")); client.Code(err) != client.CodePathRejected {
		t.Fatalf("write: %v", err)
	}
	if _, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "x", Cwd: "link"}); client.Code(err) != client.CodePathRejected {
		t.Fatalf("cwd: %v", err)
	}
}

func TestScriptsResolveInsideTheSandbox(t *testing.T) {
	for name, script := range map[string]string{"read": readScript, "write": writeScript, "cwd": checkDirScript, "ingest": ingestScript, "bundle": bundleScript} {
		if !strings.Contains(script, `realpath -m -- "$1"`) || !strings.Contains(script, "/workspace|/workspace/*") {
			t.Errorf("%s does not resolve symlinks and check the result", name)
		}
	}
	if !strings.Contains(ingestScript, `[ -L "$p" ]`) {
		t.Error("a clone must refuse to land on an existing symlink")
	}
}

func TestParseListingToleratesHostileNames(t *testing.T) {
	raw := []byte("f\t12\tplain\x00d\t4096\tdir\x00l\t5\tlink\x00p\t0\tfifo\x00f\t1\ttab\tin name\x00garbage\x00f\t7\t\xff\xfe\x00f\t9\tcut-off")
	entries := parseListing(raw)
	want := []client.DirEntry{
		{Name: "plain", Type: "file", Size: 12}, {Name: "dir", Type: "directory"}, {Name: "link", Type: "symlink"}, {Name: "fifo", Type: "other"},
		{Name: "tab\tin name", Type: "file", Size: 1}, {Name: "\uFFFD", Type: "file", Size: 7},
	}
	if fmt.Sprint(entries) != fmt.Sprint(want) {
		t.Fatalf("%+v", entries)
	}
}

func TestReadPathShapes(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	driver.onExec = func(_ execCall, request ExecRequest) (int, error) {
		_, _ = io.WriteString(request.Stdout, "Fhello")
		return 0, nil
	}
	if content, err := service.ReadPath(context.Background(), id, "x"); err != nil || content.Content != "hello" || content.IsDirectory {
		t.Fatalf("%+v %v", content, err)
	}
	driver.onExec = func(_ execCall, request ExecRequest) (int, error) {
		_, _ = io.WriteString(request.Stdout, "D")
		return 0, nil
	}
	content, err := service.ReadPath(context.Background(), id, ".")
	if err != nil || !content.IsDirectory {
		t.Fatalf("%+v %v", content, err)
	}
	if string(mustJSON(t, content)) != `{"entries":[],"truncated":false}` {
		t.Fatalf("an empty listing must still have entries: %s", mustJSON(t, content))
	}
	driver.onExec = func(execCall, ExecRequest) (int, error) { return 0, nil }
	if _, err := service.ReadPath(context.Background(), id, "x"); err == nil {
		t.Fatal("empty output must be an error")
	}
}

func TestWriteFileLimits(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	before := len(driver.execs)
	_, err := service.WriteFile(context.Background(), id, "x", make([]byte, maxFileWrite+1))
	wantStatus(t, err, http.StatusRequestEntityTooLarge, client.CodeTooLarge)
	if _, err := service.WriteFile(context.Background(), id, ".", []byte("x")); client.Code(err) != client.CodeInvalid {
		t.Fatalf("%v", err)
	}
	if len(driver.execs) != before {
		t.Fatal("reached the engine")
	}
	driver.onExec = func(call execCall, request ExecRequest) (int, error) {
		if call.Stdin != "body" {
			t.Errorf("stdin: %q", call.Stdin)
		}
		for _, arg := range call.Argv {
			if strings.Contains(arg, "body") {
				t.Errorf("file content reached argv: %q", arg)
			}
		}
		_, _ = io.WriteString(request.Stdout, "/workspace/x")
		return 0, nil
	}
	if result, err := service.WriteFile(context.Background(), id, "x", []byte("body")); err != nil || result.BytesWritten != 4 || result.Path != "/workspace/x" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestDeadSandboxIsRestarted(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	driver.instances[service.containerName(id)].Running = false
	if _, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	if len(driver.restarted) != 1 {
		t.Fatalf("restarts: %v", driver.restarted)
	}
}

func TestExecThatCannotStartRestartsOnceThenFails(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	attempts := 0
	driver.onExec = func(execCall, ExecRequest) (int, error) {
		attempts++
		return 0, errors.New("cannot fork")
	}
	_, err := service.Exec(context.Background(), id, client.ExecRequest{Command: "true"})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if attempts != 2 || len(driver.restarted) != 1 {
		t.Fatalf("attempts %d, restarts %v", attempts, driver.restarted)
	}
}

func TestDeleteRemovesHelpersContainerAndVolume(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := createTest(t, service)
	helperLabels := service.labels(RoleGitHelper)
	helperLabels[LabelID] = id
	driver.instances["stale-helper"] = &Instance{Name: "stale-helper", Labels: helperLabels}
	if err := service.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(driver.instances) != 0 || len(driver.volumes) != 0 {
		t.Fatalf("left %+v %+v", driver.instances, driver.volumes)
	}
	if err := service.Delete(context.Background(), id); err != nil {
		t.Fatalf("a repeated delete must succeed: %v", err)
	}
}

func TestReaperByLabelAndRole(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	now := time.Unix(1_000_000, 0)
	service.now = func() time.Time { return now }
	young := createTest(t, service)
	helper := service.labels(RoleGitHelper)
	driver.instances["helper"] = &Instance{Name: "helper", Labels: helper}
	foreign := map[string]string{LabelManaged: "true", LabelPrefix: "another-install", LabelRole: RoleSandbox, LabelCreated: "1"}
	driver.instances["foreign"] = &Instance{Name: "foreign", Labels: foreign}
	driver.volumes["foreign-volume"] = foreign
	unmanaged := map[string]string{"app": "gateway"}
	driver.instances["gateway"] = &Instance{Name: "gateway", Labels: unmanaged}
	orphan := service.labels(RoleSandbox)
	orphan[LabelID] = "sbx-orphan"
	driver.volumes["orphan-volume"] = orphan

	// A helper's limit is far shorter than a sandbox's: it may hold a credential.
	now = now.Add(service.config.HelperMaxAge + time.Minute)
	service.Reap(context.Background())
	if _, ok := driver.instances["helper"]; ok {
		t.Fatal("an old helper survived")
	}
	if _, ok := driver.instances[service.containerName(young)]; !ok {
		t.Fatal("a young sandbox was reaped")
	}
	if _, ok := driver.volumes["orphan-volume"]; !ok {
		t.Fatal("a young volume was reaped")
	}
	now = now.Add(service.config.TTL)
	service.Reap(context.Background())
	if _, ok := driver.instances[service.containerName(young)]; ok {
		t.Fatal("an expired sandbox survived")
	}
	if _, ok := driver.volumes[service.volumeName(young)]; ok {
		t.Fatal("an expired sandbox's volume survived")
	}
	if _, ok := driver.volumes["orphan-volume"]; ok {
		t.Fatal("an orphan volume survived")
	}
	for _, name := range []string{"foreign", "gateway"} {
		if _, ok := driver.instances[name]; !ok {
			t.Fatalf("the reaper removed %s, which is not this manager's", name)
		}
	}
	if _, ok := driver.volumes["foreign-volume"]; !ok {
		t.Fatal("the reaper removed another install's volume")
	}
}

func TestReaperSurvivesListFailure(t *testing.T) {
	service, driver, logs := newTestService(t, nil)
	driver.listErr = errors.New("engine down")
	service.Reap(context.Background())
	if !strings.Contains(logs.String(), "reaper could not list") {
		t.Fatal(logs.String())
	}
}

// HTTP layer.

func testAPI(t *testing.T, extra map[string]string) (*httptest.Server, *Service, *fakeDriver, *lockedBuffer) {
	t.Helper()
	service, driver, logs := newTestService(t, extra)
	server := httptest.NewServer(NewHandler(service, testToken, log.New(logs, "", 0)))
	t.Cleanup(server.Close)
	return server, service, driver, logs
}

func TestAuthorizationEveryRouteButHealth(t *testing.T) {
	server, _, driver, _ := testAPI(t, nil)
	id := "sbx-" + strings.Repeat("a", 24)
	routes := [][2]string{{"POST", "/sandboxes"}, {"POST", "/sandboxes/" + id + "/exec"}, {"GET", "/sandboxes/" + id + "/files?path=x"}, {"PUT", "/sandboxes/" + id + "/files?path=x"}, {"POST", "/sandboxes/" + id + "/git/clone"}, {"POST", "/sandboxes/" + id + "/git/push"}, {"GET", "/sandboxes/" + id + "/git/diff"}, {"DELETE", "/sandboxes/" + id}}
	headers := []string{"", "Bearer", "Bearer ", "Bearer wrong", "Basic " + testToken, "bearer " + testToken, "Bearer " + testToken + "x", "Bearer " + testToken[:len(testToken)-1]}
	for _, route := range routes {
		for _, header := range headers {
			request, _ := http.NewRequest(route[0], server.URL+route[1], strings.NewReader("{}"))
			if header != "" {
				request.Header.Set("Authorization", header)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Errorf("%v with %q: %d", route, header, response.StatusCode)
			}
		}
	}
	if len(driver.specs)+len(driver.execs) != 0 {
		t.Fatal("an unauthorized call reached the engine")
	}
	empty := authorize("", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer ")
	empty.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatal("an empty configured token must refuse everyone")
	}
}

func TestHealthContractFields(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		extra := map[string]string{}
		if unsafe {
			extra = map[string]string{"KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME": "1"}
		}
		server, _, _, _ := testAPI(t, extra)
		api := client.New(server.URL, "")
		health, err := api.Health(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if health.UnsafeRuntime != unsafe {
			t.Fatalf("typed client: unsafe_runtime %v, want %v", health.UnsafeRuntime, unsafe)
		}
		response, err := http.Get(server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		for _, key := range []string{"runtime", "runtime_verified", "driver", "unsafe_runtime"} {
			if _, ok := raw[key]; !ok {
				t.Errorf("missing %s", key)
			}
		}
		if len(raw) != 4 {
			t.Errorf("the contract is closed: %v", raw)
		}
	}
}

func TestStrictRequestBodies(t *testing.T) {
	server, _, driver, _ := testAPI(t, nil)
	api := client.New(server.URL, testToken)
	sandbox, err := api.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	specs := len(driver.specs)
	cases := []struct{ path, body string }{
		{"/sandboxes", `{"run_id":"` + testRun + `","worker_id":"` + testWorker + `","project_id":"p","privileged":true}`},
		{"/sandboxes", `{"run_id":"` + testRun + `","worker_id":"` + testWorker + `","project_id":"p","env":["A=B"]}`},
		{"/sandboxes", `{"run_id":"` + testRun + `","worker_id":"` + testWorker + `","project_id":"p","runtime":"runc"}`},
		{"/sandboxes", `{"run_id":"` + testRun + `","worker_id":"` + testWorker + `","project_id":"p","network":"host"}`},
		{"/sandboxes", `{"run_id":"` + testRun + `","worker_id":"` + testWorker + `","project_id":"p","mounts":["/:/host"]}`},
		{"/sandboxes", `{"run_id":"` + testRun + `"} trailing`},
		{"/sandboxes/" + sandbox.ID + "/exec", `{"command":"id","env":{"A":"B"}}`},
		{"/sandboxes/" + sandbox.ID + "/exec", `{"command":["id"]}`},
		{"/sandboxes/" + sandbox.ID + "/git/push", `{"repository":"https://github.com/o/r","branch":"x","credential":"c","force":true}`},
		{"/sandboxes/" + sandbox.ID + "/exec", `not json`},
		{"/sandboxes/" + sandbox.ID + "/git/clone", `{"repository":"https://github.com/o/r","extra_hosts":["github.com:10.0.0.1"]}`},
		{"/sandboxes/" + sandbox.ID + "/git/push", `{"repository":"https://github.com/o/r","branch":"x","credential":"c","network":"host"}`},
	}
	for _, item := range cases {
		request, _ := http.NewRequest("POST", server.URL+item.path, strings.NewReader(item.body))
		request.Header.Set("Authorization", "Bearer "+testToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var failure map[string]any
		_ = json.NewDecoder(response.Body).Decode(&failure)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || failure["error"] != client.CodeInvalid || failure["message"] == nil || len(failure) != 2 {
			t.Errorf("%s: %d %v", item.body, response.StatusCode, failure)
		}
	}
	if len(driver.specs) != specs {
		t.Fatal("an invalid create reached the engine")
	}
}

func TestErrorMessagesAreBoundedAndEngineTextStaysPrivate(t *testing.T) {
	server, service, driver, logs := testAPI(t, nil)
	_ = service
	driver.createErr = errors.New("/var/run/secret/engine/path exploded")
	response := postJSON(t, server.URL+"/sandboxes", client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "exploded") {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	if !strings.Contains(logs.String(), "exploded") {
		t.Fatal("the operator's log should have the engine's reason")
	}
	long := &client.Error{Status: 502, Code: client.CodeGitFailed, Message: strings.Repeat("é", 2000)}
	recorder := httptest.NewRecorder()
	(&api{service: service, logger: log.New(io.Discard, "", 0)}).fail(recorder, long)
	var failure client.Error
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil || len([]rune(failure.Message)) > 500 {
		t.Fatalf("%v %d", err, len(failure.Message))
	}
}

func postJSON(t *testing.T, target string, value any) *http.Response {
	t.Helper()
	request, _ := http.NewRequest("POST", target, bytes.NewReader(mustJSON(t, value)))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRequestLogHoldsNoPathOrBody(t *testing.T) {
	server, _, driver, logs := testAPI(t, nil)
	api := client.New(server.URL, testToken)
	sandbox, err := api.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	gitFixtureOn(driver)
	secret := secretValue()
	if _, err := api.GitPush(context.Background(), sandbox.ID, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x", Credential: client.Credential(secret)}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.ReadPath(context.Background(), sandbox.ID, "private-file-name"); err == nil {
		t.Log("read returned content")
	}
	for _, needle := range []string{secret, "private-file-name", "kionga/" + testRun} {
		if strings.Contains(logs.String(), needle) {
			t.Fatalf("the log holds %q:\n%s", needle, logs.String())
		}
	}
}

func gitFixtureOn(driver *fakeDriver) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.onExec = func(call execCall, request ExecRequest) (int, error) {
		switch {
		case containsArg(call.Argv, bundleScript):
			_, _ = io.WriteString(request.Stdout, "/workspace/.kionga-xfer.abc123\n5\n")
		case containsArg(call.Argv, "/workspace/.kionga-xfer.abc123") && containsArg(call.Argv, "/bin/cat"):
			_, _ = io.WriteString(request.Stdout, "BUNDL")
		case containsArg(call.Argv, helperPushScript):
			_, _ = io.WriteString(request.Stdout, strings.Repeat("b", 40)+"\n")
		}
		return 0, nil
	}
}

func TestDeleteReturns204AndClientRoundTrip(t *testing.T) {
	server, _, driver, _ := testAPI(t, nil)
	api := client.New(server.URL, testToken)
	sandbox, err := api.Create(context.Background(), client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject})
	if err != nil || sandbox.Status != "running" {
		t.Fatalf("%+v %v", sandbox, err)
	}
	driver.onExec = func(_ execCall, request ExecRequest) (int, error) {
		_, _ = io.WriteString(request.Stdout, "ok")
		return 0, nil
	}
	if result, err := api.Exec(context.Background(), sandbox.ID, client.ExecRequest{Command: "x"}); err != nil || result.Stdout != "ok" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := api.GitDiff(context.Background(), sandbox.ID); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("DELETE", server.URL+"/sandboxes/"+sandbox.ID, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", response.StatusCode)
	}
	if err := api.Delete(context.Background(), sandbox.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Exec(context.Background(), sandbox.ID, client.ExecRequest{Command: "x"}); client.Code(err) != client.CodeNotFound {
		t.Fatalf("%v", err)
	}
	if !client.IsUnavailable(errors.New("connection refused")) || client.IsUnavailable(&client.Error{Code: client.CodeNotFound}) || !client.IsUnavailable(&client.Error{Code: client.CodeUnavailable}) {
		t.Fatal("IsUnavailable")
	}
}

func TestBlockedAddresses(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1", "127.8.8.8", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.100.100.200",
		"0.0.0.0", "224.0.0.1", "240.0.0.1", "255.255.255.255", "198.18.0.1", "168.63.129.16",
		"::", "::1", "fe80::1", "fc00::1", "fd00:ec2::254", "ff02::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a00:1", "2002:a00:1::",
	} {
		if !blockedAddress(netip.MustParseAddr(raw)) {
			t.Errorf("%s must be blocked", raw)
		}
	}
	for _, raw := range []string{"140.82.112.3", "8.8.8.8", "172.32.0.1", "192.169.0.1", "2606:50c0:8000::153", "::ffff:140.82.112.3"} {
		if blockedAddress(netip.MustParseAddr(raw)) {
			t.Errorf("%s must be allowed", raw)
		}
	}
	if !blockedAddress(netip.Addr{}) {
		t.Error("an invalid address must be blocked")
	}
}

func TestPrivateResolutionNeverStartsAHelper(t *testing.T) {
	cases := map[string][]netip.Addr{
		"private":         {netip.MustParseAddr("10.0.0.5")},
		"metadata":        {netip.MustParseAddr("169.254.169.254")},
		"loopback":        {netip.MustParseAddr("127.0.0.1")},
		"mapped":          {netip.MustParseAddr("::ffff:192.168.0.1")},
		"one bad of many": {netip.MustParseAddr("140.82.112.3"), netip.MustParseAddr("10.0.0.5")},
	}
	for name, answers := range cases {
		t.Run(name, func(t *testing.T) {
			service, driver, _ := newTestService(t, nil)
			id := gitFixture(t, service, driver)
			service.resolve = func(context.Context, string) ([]netip.Addr, error) { return answers, nil }
			_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r", Credential: "secret-value"})
			wantStatus(t, err, http.StatusForbidden, client.CodeHostNotAllowed)
			_, err = service.GitPush(context.Background(), id, client.GitPushRequest{Repository: "https://github.com/o/r", Branch: "kionga/" + testRun + "/x", Credential: "secret-value"})
			wantStatus(t, err, http.StatusForbidden, client.CodeHostNotAllowed)
			if len(driver.specNamed(RoleGitHelper)) != 0 {
				t.Fatal("a helper started for a private address")
			}
		})
	}
}

func TestPrivateLiteralRefusedAndTestAllowanceWorks(t *testing.T) {
	service, driver, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_GIT_HOSTS": "10.0.0.5:8443"})
	id := gitFixture(t, service, driver)
	_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://10.0.0.5:8443/o/r"})
	wantStatus(t, err, http.StatusForbidden, client.CodeHostNotAllowed)
	if len(driver.specNamed(RoleGitHelper)) != 0 {
		t.Fatal("a helper started for a private literal")
	}

	allowed, driver, logs := newTestService(t, map[string]string{"KIONGA_SANDBOX_GIT_HOSTS": "10.0.0.5:8443", "KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS": "1"})
	id = gitFixture(t, allowed, driver)
	if _, err := allowed.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://10.0.0.5:8443/o/r"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "WARNING") {
		t.Fatal("the test allowance must be loud")
	}
}

func TestResolutionFailureFailsClosed(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := gitFixture(t, service, driver)
	for _, resolve := range []func(context.Context, string) ([]netip.Addr, error){
		func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no route to resolver") },
		func(context.Context, string) ([]netip.Addr, error) { return nil, nil },
	} {
		service.resolve = resolve
		_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r"})
		wantStatus(t, err, http.StatusBadGateway, client.CodeGitFailed)
	}
	if len(driver.specNamed(RoleGitHelper)) != 0 {
		t.Fatal("a helper started with an unresolved host")
	}
}

func TestHelperNetworkFailureFailsClosed(t *testing.T) {
	service, driver, _ := newTestService(t, nil)
	id := gitFixture(t, service, driver)
	driver.networkErr = errors.New("network exists and was not created by this manager")
	_, err := service.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r"})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(driver.specNamed(RoleGitHelper)) != 0 {
		t.Fatal("a helper started without its network")
	}
}
