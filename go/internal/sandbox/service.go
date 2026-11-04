package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiongahq/platform/internal/redact"
	"github.com/kiongahq/platform/internal/sandbox/client"
)

const (
	internalTimeout = 30 * time.Second
	attestTimeout   = 60 * time.Second
	probeTimeout    = 5 * time.Minute
	cleanupTimeout  = 60 * time.Second
	sweepTimeout    = 10 * time.Second
	failedProbeTTL  = 15 * time.Second
	verifyInterval  = 5 * time.Minute
	// maxSweepSurvivors allows for PID 1, its sleep, the sweep shell and zombies not
	// yet reaped. More than this means something outran the kill.
	maxSweepSurvivors = 16
	// floodBytes ends an exec whose output is only being thrown away.
	floodBytes = 256 << 20
)

// Service holds every isolation and git policy. Runtime attestations are local
// cache entries: a new manager must attest existing instances again.
type Service struct {
	gitMu        sync.Mutex
	gitActive    map[string]bool // sandbox ID -> operation needs a helper
	attestations sync.Map        // container name -> *attestationState
	config       Config
	driver       Driver
	now          Clock
	logger       *log.Logger

	verifyMu   sync.Mutex
	verifiedOK bool
	checkedAt  time.Time
	probe      func(context.Context) error
	resolve    func(context.Context, string) ([]netip.Addr, error)

	// quotaStopped only speeds up the answer during a call. The next call measures again.
	quotaStopped  sync.Map
	freeMu        sync.Mutex
	freeBytes     int64
	freeCheckedAt time.Time
}

// NewService builds a Service. The caller starts Run for the background work.
func NewService(config Config, driver Driver, logger *log.Logger) *Service {
	service := &Service{config: config, driver: driver, now: time.Now, logger: logger}
	service.probe = service.runProbe
	service.resolve = resolveLookup
	return service
}

func unavailable(message string) error {
	return &client.Error{Status: http.StatusServiceUnavailable, Code: client.CodeUnavailable, Message: message}
}

func invalid(message string) error {
	return &client.Error{Status: http.StatusBadRequest, Code: client.CodeInvalid, Message: message}
}

func (s *Service) managedLabels() map[string]string {
	return map[string]string{LabelManaged: "true", LabelPrefix: strings.TrimRight(s.config.NamePrefix, "-")}
}

func (s *Service) labels(role string) map[string]string {
	labels := s.managedLabels()
	labels[LabelRole] = role
	labels[LabelCreated] = strconv.FormatInt(s.now().Unix(), 10)
	return labels
}

func (s *Service) containerName(id string) string { return s.config.NamePrefix + id }
func (s *Service) volumeName(id string) string    { return s.config.NamePrefix + id + "-workspace" }

// hardenedSpec is the base for everything the manager starts. Helpers and probes use
// the same runtime and limits as sandboxes, because a helper parses remote data.
func (s *Service) hardenedSpec(name, image, role string) ContainerSpec {
	return ContainerSpec{
		Name:        name,
		Image:       image,
		Runtime:     s.config.Runtime,
		Labels:      s.labels(role),
		Command:     []string{"/bin/sh", "-c", initScript},
		Network:     NetworkNone,
		MemoryBytes: s.config.MemoryBytes,
		NanoCPUs:    s.config.NanoCPUs,
		Pids:        s.config.Pids,
		Tmpfs:       []TmpfsMount{{Path: "/tmp", SizeBytes: s.config.TmpBytes}},

		FileSizeBytes: s.config.DiskBytes,
	}
}

// Health never blocks on a probe: it reports the last one. RuntimeVerified is true
// only if the engine lists the runtime now and a real container last proved it.
func (s *Service) Health(ctx context.Context) client.Health {
	health := client.Health{Runtime: s.config.Runtime, Driver: s.driver.Name(), UnsafeRuntime: s.config.AllowUnsafe}
	if len(s.config.Images) == 0 {
		return health
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	registered, err := s.driver.RuntimeRegistered(checkCtx, s.config.Runtime)
	if err != nil || !registered {
		return health
	}
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	health.RuntimeVerified = s.verifiedOK
	return health
}

// verify is the gate in front of every create. The engine must list the runtime now,
// and a throwaway container must have proved it recently.
func (s *Service) verify(ctx context.Context, force bool) error {
	if len(s.config.Images) == 0 {
		return unavailable("KIONGA_SANDBOX_IMAGES is empty")
	}
	registered, err := s.driver.RuntimeRegistered(ctx, s.config.Runtime)
	if err != nil {
		s.logger.Printf("sandbox: runtime check failed: %v", err)
		s.recordProbe(false)
		return unavailable("the container engine could not be checked for the sandbox runtime")
	}
	if !registered {
		s.recordProbe(false)
		return unavailable("sandbox runtime " + s.config.Runtime + " is not registered")
	}
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	age := s.now().Sub(s.checkedAt)
	if !force && !s.checkedAt.IsZero() {
		if s.verifiedOK && age < 2*verifyInterval {
			return nil
		}
		if !s.verifiedOK && age < failedProbeTTL {
			return unavailable("sandbox runtime " + s.config.Runtime + " failed verification")
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	err = s.probe(probeCtx)
	s.verifiedOK, s.checkedAt = err == nil, s.now()
	if err != nil {
		s.logger.Printf("sandbox: runtime %s failed verification: %v", s.config.Runtime, err)
		return unavailable("sandbox runtime " + s.config.Runtime + " failed verification")
	}
	return nil
}

func (s *Service) recordProbe(ok bool) {
	s.verifyMu.Lock()
	s.verifiedOK, s.checkedAt = ok, s.now()
	s.verifyMu.Unlock()
}

func (s *Service) runProbe(ctx context.Context) error {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	name := s.config.NamePrefix + "probe-" + hex.EncodeToString(suffix)
	spec := s.hardenedSpec(name, s.config.HelperImage, RoleProbe)
	defer s.removeQuietly(name)
	if err := s.driver.Create(ctx, spec); err != nil {
		return err
	}
	return s.attest(ctx, name)
}

// attest proves what the sandbox runs on, from inside it, before any agent code has
// run there. An engine's list of runtime names proves nothing: Podman lists runtimes
// with no binary, and "runsc" can be registered to point at runc. Unsafe mode skips
// only this kernel comparison.
func (s *Service) attest(ctx context.Context, name string) error {
	if s.config.AllowUnsafe {
		return nil
	}
	attestCtx, cancel := context.WithTimeout(ctx, attestTimeout)
	defer cancel()
	stdout := &headBuffer{limit: 256}
	code, err := s.driver.Exec(attestCtx, name, ExecRequest{Argv: []string{"/bin/uname", "-r"}, Stdout: stdout, Stderr: io.Discard})
	if err != nil {
		return err
	}
	kernel := strings.TrimSpace(string(stdout.data))
	if code != 0 || !strings.Contains(strings.ToLower(kernel), strings.ToLower(s.config.ExpectKernel)) {
		return errors.New("kernel release " + strconv.Quote(kernel) + " does not contain " + strconv.Quote(s.config.ExpectKernel))
	}
	return nil
}

func (s *Service) removeQuietly(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := s.driver.Remove(ctx, name); err != nil {
		s.logger.Printf("sandbox: could not remove %s: %v", name, err)
	}
}

func (s *Service) removeVolumeQuietly(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := s.driver.RemoveVolume(ctx, name); err != nil {
		s.logger.Printf("sandbox: could not remove volume %s: %v", name, err)
	}
}

func (s *Service) chooseImage(requested string) (string, error) {
	if len(s.config.Images) == 0 {
		return "", unavailable("KIONGA_SANDBOX_IMAGES is empty")
	}
	if requested == "" {
		return s.config.Images[0], nil
	}
	for _, image := range s.config.Images {
		if image == requested {
			return image, nil
		}
	}
	return "", &client.Error{Status: http.StatusForbidden, Code: client.CodeImageNotAllowed, Message: "image is not in KIONGA_SANDBOX_IMAGES"}
}

// Create returns the sandbox for the run and worker, starting it if needed.
func (s *Service) Create(ctx context.Context, request client.CreateRequest) (client.Sandbox, error) {
	if !runIDPattern.MatchString(request.RunID) || !workerIDPattern.MatchString(request.WorkerID) || request.ProjectID == "" {
		return client.Sandbox{}, invalid("run_id, worker_id or project_id is malformed")
	}
	image, err := s.chooseImage(request.Image)
	if err != nil {
		return client.Sandbox{}, err
	}
	if err := s.verify(ctx, false); err != nil {
		return client.Sandbox{}, err
	}
	if s.config.AllowUnsafe {
		s.logger.Printf("WARNING: sandbox created with KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME=1 on runtime %s: not for production, model-chosen code is not isolated by gVisor", s.config.Runtime)
	}
	id := DeriveID(request.ProjectID, request.RunID, request.WorkerID)
	name := s.containerName(id)
	if _, err := s.instance(ctx, id); err == nil {
		return client.Sandbox{ID: id, Status: "running"}, nil
	} else if client.Code(err) != client.CodeNotFound {
		return client.Sandbox{}, err
	}
	existing, err := s.driver.List(ctx, s.roleLabels(RoleSandbox))
	if err != nil {
		s.logger.Printf("sandbox: list failed: %v", err)
		return client.Sandbox{}, unavailable("the container engine could not list sandboxes")
	}
	if int64(len(existing)) >= s.config.MaxSandboxes {
		return client.Sandbox{}, unavailable("the sandbox limit has been reached")
	}
	if err := s.checkFreeDisk(ctx); err != nil {
		return client.Sandbox{}, err
	}
	spec := s.hardenedSpec(name, image, RoleSandbox)
	spec.Labels[LabelID] = id
	spec.Labels[LabelRun] = request.RunID
	spec.Labels[LabelWorker] = request.WorkerID
	spec.Labels[LabelProject] = projectLabel(request.ProjectID)
	spec.Volume = &VolumeMount{Name: s.volumeName(id), Path: WorkspacePath, SizeBytes: s.config.DiskBytes}
	err = s.driver.Create(ctx, spec)
	if errors.Is(err, ErrExists) {
		// A concurrent create won. instance waits for runtime attestation.
		if _, inspectErr := s.instance(ctx, id); inspectErr != nil {
			return client.Sandbox{}, inspectErr
		}
		return client.Sandbox{ID: id, Status: "running"}, nil
	}
	if err == nil {
		_, err = s.instance(ctx, id)
	}
	if err != nil {
		s.logger.Printf("sandbox: create %s failed: %v", id, err)
		s.removeQuietly(name)
		s.removeVolumeQuietly(s.volumeName(id))
		s.recordProbe(false)
		return client.Sandbox{}, unavailable("the sandbox could not be started under the verified runtime")
	}
	return client.Sandbox{ID: id, Status: "running"}, nil
}

func (s *Service) roleLabels(role string) map[string]string {
	labels := s.managedLabels()
	labels[LabelRole] = role
	return labels
}

// instance finds a sandbox and proves it is one of ours, on the configured runtime.
// The strict id pattern means a request can never name another container or reach
// another engine path.
func (s *Service) instance(ctx context.Context, id string) (Instance, error) {
	notFound := &client.Error{Status: http.StatusNotFound, Code: client.CodeNotFound, Message: "no such sandbox"}
	if !sandboxID.MatchString(id) {
		return Instance{}, notFound
	}
	found, err := s.driver.Inspect(ctx, s.containerName(id))
	if errors.Is(err, ErrNotFound) {
		return Instance{}, notFound
	}
	if err != nil {
		s.logger.Printf("sandbox: inspect %s failed: %v", id, err)
		return Instance{}, unavailable("the container engine could not be reached")
	}
	prefix := strings.TrimRight(s.config.NamePrefix, "-")
	if found.Labels[LabelManaged] != "true" || found.Labels[LabelPrefix] != prefix || found.Labels[LabelID] != id || found.Labels[LabelRole] != RoleSandbox {
		return Instance{}, notFound
	}
	if found.Runtime != "" && found.Runtime != s.config.Runtime {
		s.logger.Printf("sandbox: %s runs on %s, expected %s", id, found.Runtime, s.config.Runtime)
		return Instance{}, unavailable("the sandbox is not on the configured runtime")
	}
	if found.Running {
		return s.ensureAttested(ctx, found.Name)
	}
	return found, nil
}

// running is instance plus recovery: PID 1 can die from a memory limit or a
// fork bomb, and the agent should get its workspace back rather than a dead sandbox.
func (s *Service) running(ctx context.Context, id string) (Instance, error) {
	found, err := s.instance(ctx, id)
	if err != nil {
		return Instance{}, err
	}
	if found.Running {
		return found, nil
	}
	if err := s.restartInstance(ctx, found.Name); err != nil {
		s.logger.Printf("sandbox: restart %s failed: %v", id, err)
		return Instance{}, unavailable("the sandbox could not be restarted")
	}
	return s.instance(ctx, id)
}

// userArgv runs a command with a cleared environment, so nothing in the image or the
// engine's environment reaches it.
func userArgv(cwd string, extraEnv []string, program ...string) []string {
	argv := []string{"/usr/bin/env", "-i", "-C", cwd, safePath, "HOME=" + WorkspacePath, "LANG=C.UTF-8", "TMPDIR=/tmp"}
	argv = append(argv, extraEnv...)
	return append(argv, program...)
}

func scriptArgv(script string, args ...string) []string {
	return userArgv(WorkspacePath, []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}, append([]string{"/bin/sh", "-c", script, "kionga"}, args...)...)
}

// Exec runs a model-chosen command. The deadline is enforced here, on the manager's
// clock, so nothing inside the sandbox can extend it.
func (s *Service) Exec(ctx context.Context, id string, request client.ExecRequest) (client.ExecResult, error) {
	if request.Command == "" {
		return client.ExecResult{}, invalid("command is required")
	}
	if len(request.Command) > maxCommand || strings.IndexByte(request.Command, 0) >= 0 {
		return client.ExecResult{}, invalid("command is too long or contains NUL")
	}
	timeout := defaultExecTimeout
	if request.TimeoutSeconds != 0 {
		timeout = time.Duration(request.TimeoutSeconds) * time.Second
		if request.TimeoutSeconds < 0 || timeout > maxExecTimeout {
			return client.ExecResult{}, invalid("timeout_seconds must be between 1 and 1800")
		}
	}
	cwd := WorkspacePath
	if request.Cwd != "" {
		var err error
		if cwd, err = workspacePath(request.Cwd); err != nil {
			return client.ExecResult{}, err
		}
	}
	found, err := s.writable(ctx, id)
	if err != nil {
		return client.ExecResult{}, err
	}
	if request.Cwd != "" {
		// The resolved directory is used as is. A later swap of that path by the agent
		// only affects its own command's starting directory.
		if cwd, err = s.resolveDir(ctx, found.Name, cwd); err != nil {
			return client.ExecResult{}, err
		}
	}
	return s.runUser(ctx, found.Name, userArgv(cwd, nil, "/bin/bash", "-c", request.Command), timeout, true)
}

func (s *Service) resolveDir(ctx context.Context, name, dir string) (string, error) {
	stdout := &headBuffer{limit: 8192}
	code, err := s.internalExec(ctx, name, scriptArgv(checkDirScript, dir), nil, stdout, io.Discard)
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
		resolved, err := workspacePath(string(stdout.data))
		if err != nil {
			return "", rejectPath("path is outside /workspace")
		}
		return resolved, nil
	case exitPathEscape, exitResolveFailed:
		return "", rejectPath("path resolves outside /workspace")
	default:
		return "", invalid("cwd is not an existing directory")
	}
}

// runUser runs argv under a deadline, killing what it started when the deadline
// passes, the caller goes away or the output floods. retry allows one restart when
// the exec could not start at all, which is what a fork bomb's survivors cause.
func (s *Service) runUser(ctx context.Context, name string, argv []string, timeout time.Duration, retry bool) (client.ExecResult, error) {
	started := s.now()
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := newCapture(outputCap), newCapture(outputCap)
	stdout.flood, stdout.onFlood = floodBytes, cancel
	stderr.flood, stderr.onFlood = floodBytes, cancel
	code, err := s.execAttested(execCtx, name, ExecRequest{Argv: argv, Stdout: stdout, Stderr: stderr})
	result := client.ExecResult{
		ExitCode:   code,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		Truncated:  stdout.truncated() || stderr.truncated(),
		DurationMS: s.now().Sub(started).Milliseconds(),
	}
	if quota := s.quotaError(name); quota != nil {
		return result, quota
	}
	if err == nil && !execDidNotStart(code, result.Stdout, result.Stderr) {
		return result, nil
	}
	if err == nil {
		err = errors.New("the runtime refused the exec")
	}
	if execCtx.Err() != nil {
		s.killAll(name)
		result.ExitCode = -1
		result.TimedOut = errors.Is(execCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	s.logger.Printf("sandbox: exec could not run in %s: %v", name, err)
	if retry {
		s.restartQuietly(name)
		return s.runUser(ctx, name, argv, timeout, false)
	}
	return result, unavailable("the command could not be started in the sandbox")
}

// execDidNotStart recognises the engine's report that the exec never ran. gVisor stops
// the whole sandbox when it hits a memory or pid limit, and an exec arriving before the
// engine notices gets this as ordinary output with an exit code. A command can print the
// same text, which only earns it a restart of its own sandbox.
func execDidNotStart(code int, stdout, stderr string) bool {
	const marker = "OCI runtime exec failed"
	return code >= 125 && code <= 128 && (strings.HasPrefix(stdout, marker) || strings.HasPrefix(stderr, marker))
}

// killAll stops everything a timed-out command started. Killing the process group
// is not enough, because a hostile command leaves the group with setsid. The cost is
// that other background processes in this sandbox die too, which a sandbox dedicated
// to one worker can accept. If a process inside cannot even be started, typically at
// the pid limit, the whole instance is restarted, which keeps the workspace.
func (s *Service) killAll(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()
	stdout := &headBuffer{limit: 64}
	code, err := s.execAttested(ctx, name, ExecRequest{Argv: []string{"/bin/sh", "-c", sweepScript}, Stdout: stdout, Stderr: io.Discard})
	survivors, parseErr := strconv.Atoi(strings.TrimSpace(string(stdout.data)))
	if err == nil && code == 0 && parseErr == nil && survivors <= maxSweepSurvivors {
		return
	}
	s.restartQuietly(name)
}

func (s *Service) restartQuietly(name string) {
	if s.quotaError(name) != nil {
		return // stopped on purpose
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := s.restartInstance(ctx, name); err != nil {
		s.logger.Printf("sandbox: restart %s failed: %v", name, err)
	}
}

// internalExec runs one of the fixed scripts for a file or git operation. It does
// not sweep on timeout: the operation failed, but the worker's processes are not at fault.
func (s *Service) internalExec(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	execCtx, cancel := context.WithTimeout(ctx, internalTimeout)
	defer cancel()
	return s.scriptExec(execCtx, name, argv, stdin, stdout, stderr)
}

func (s *Service) scriptExec(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	code, err := s.execAttested(ctx, name, ExecRequest{Argv: argv, Stdin: stdin, Stdout: stdout, Stderr: stderr})
	if err != nil {
		if ctx.Err() != nil {
			return code, unavailable("the sandbox operation timed out")
		}
		s.logger.Printf("sandbox: operation failed in %s: %v", name, err)
		return code, unavailable("the sandbox operation could not run")
	}
	return code, nil
}

func pathError(code int) error {
	switch code {
	case exitPathEscape, exitResolveFailed:
		return rejectPath("path resolves outside /workspace")
	case exitNotFound:
		return &client.Error{Status: http.StatusNotFound, Code: client.CodeNotFound, Message: "no such path"}
	case exitSpecialFile:
		return invalid("path is not a regular file or directory")
	case exitIsDirectory:
		return invalid("path is a directory")
	default:
		return &client.Error{Status: http.StatusBadGateway, Code: client.CodeInternal, Message: "the file operation failed"}
	}
}

// ReadPath reads a file or lists a directory. Resolution happens inside the sandbox
// as the sandbox user, so a race with the agent can only show it files it can read anyway.
func (s *Service) ReadPath(ctx context.Context, id, raw string) (client.PathContent, error) {
	target, err := workspacePath(raw)
	if err != nil {
		return client.PathContent{}, err
	}
	name, cleanup, err := s.readable(ctx, id)
	if err != nil {
		return client.PathContent{}, err
	}
	defer cleanup()
	stdout := &headBuffer{limit: maxFileRead + 1}
	code, err := s.internalExec(ctx, name, scriptArgv(readScript, target, strconv.Itoa(maxFileRead)), nil, stdout, io.Discard)
	if err != nil {
		return client.PathContent{}, err
	}
	if code != 0 {
		return client.PathContent{}, pathError(code)
	}
	if len(stdout.data) == 0 {
		return client.PathContent{}, pathError(-1)
	}
	body := stdout.data[1:]
	switch stdout.data[0] {
	case 'F':
		return client.PathContent{Content: strings.ToValidUTF8(string(body), "�"), Truncated: stdout.overflow || len(body) >= maxFileRead}, nil
	case 'D':
		return client.PathContent{IsDirectory: true, Entries: parseListing(body), Truncated: stdout.overflow}, nil
	}
	return client.PathContent{}, pathError(-1)
}

// parseListing reads find's NUL-terminated rows. Names are hostile, so a row it
// cannot read is dropped, and a final row cut off by the size cap is ignored.
func parseListing(raw []byte) []client.DirEntry {
	entries := []client.DirEntry{}
	rows := bytes.Split(raw, []byte{0})
	for _, row := range rows[:len(rows)-1] {
		fields := bytes.SplitN(row, []byte{'\t'}, 3)
		if len(fields) != 3 || len(fields[2]) == 0 {
			continue
		}
		entry := client.DirEntry{Name: strings.ToValidUTF8(string(fields[2]), "�")}
		switch string(fields[0]) {
		case "f":
			entry.Type = "file"
		case "d":
			entry.Type = "directory"
		case "l":
			entry.Type = "symlink"
		default:
			entry.Type = "other"
		}
		if size, err := strconv.ParseInt(string(fields[1]), 10, 64); err == nil && size >= 0 && entry.Type == "file" {
			entry.Size = size
		}
		entries = append(entries, entry)
	}
	return entries
}

// WriteFile replaces a file, creating parent directories.
func (s *Service) WriteFile(ctx context.Context, id, raw string, content []byte) (client.WriteResult, error) {
	target, err := workspacePath(raw)
	if err != nil {
		return client.WriteResult{}, err
	}
	if target == WorkspacePath {
		return client.WriteResult{}, invalid("path is a directory")
	}
	if len(content) > maxFileWrite {
		return client.WriteResult{}, &client.Error{Status: http.StatusRequestEntityTooLarge, Code: client.CodeTooLarge, Message: "the file exceeds the write limit"}
	}
	found, err := s.writable(ctx, id)
	if err != nil {
		return client.WriteResult{}, err
	}
	stdout := &headBuffer{limit: 8192}
	code, err := s.internalExec(ctx, found.Name, scriptArgv(writeScript, target, strconv.Itoa(len(content))), bytes.NewReader(content), stdout, io.Discard)
	if err != nil {
		return client.WriteResult{}, err
	}
	if code != 0 {
		return client.WriteResult{}, pathError(code)
	}
	written, err := workspacePath(string(stdout.data))
	if err != nil {
		return client.WriteResult{}, pathError(-1)
	}
	return client.WriteResult{Path: written, BytesWritten: int64(len(content))}, nil
}

// GitDiff shows each repository's changes since it was cloned.
func (s *Service) GitDiff(ctx context.Context, id string) (client.GitDiffResult, error) {
	release, err := s.admitGit(ctx, id, false)
	if err != nil {
		return client.GitDiffResult{}, err
	}
	defer release()
	name, cleanup, err := s.readable(ctx, id)
	if err != nil {
		return client.GitDiffResult{}, err
	}
	defer cleanup()
	stdout := newCapture(outputCap)
	if _, err := s.internalExec(ctx, name, scriptArgv(diffScript), nil, stdout, io.Discard); err != nil {
		return client.GitDiffResult{}, err
	}
	return client.GitDiffResult{Diff: stdout.String(), Truncated: stdout.truncated()}, nil
}

// Delete removes the sandbox, its workspace and any helper still working for it.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !sandboxID.MatchString(id) {
		return &client.Error{Status: http.StatusNotFound, Code: client.CodeNotFound, Message: "no such sandbox"}
	}
	// Helpers and meters for this sandbox go first: a meter holding the volume would
	// stop the volume being removed.
	attached := s.managedLabels()
	attached[LabelID] = id
	others, err := s.driver.List(ctx, attached)
	if err != nil {
		s.logger.Printf("sandbox: list helpers of %s failed: %v", id, err)
		return unavailable("the container engine could not be reached")
	}
	for _, other := range others {
		if other.Labels[LabelRole] != RoleSandbox {
			s.removeQuietly(other.Name)
		}
	}
	s.quotaStopped.Delete(s.containerName(id))
	if found, err := s.instance(ctx, id); err == nil {
		if err := s.driver.Remove(ctx, found.Name); err != nil {
			s.logger.Printf("sandbox: remove %s failed: %v", id, err)
			return unavailable("the sandbox could not be removed")
		}
	} else if client.Code(err) != client.CodeNotFound {
		return err
	}
	if err := s.driver.RemoveVolume(ctx, s.volumeName(id)); err != nil {
		s.logger.Printf("sandbox: remove volume of %s failed: %v", id, err)
		return unavailable("the sandbox workspace could not be removed")
	}
	return nil
}

// Reap removes what has outlived its limit, by label, so it still works after a
// manager restart. A helper's limit is short because it may hold a credential.
func (s *Service) Reap(ctx context.Context) {
	now := s.now()
	instances, err := s.driver.List(ctx, s.managedLabels())
	if err != nil {
		s.logger.Printf("sandbox: reaper could not list: %v", err)
		return
	}
	live := map[string]bool{}
	for _, item := range instances {
		limit := s.config.HelperMaxAge
		if item.Labels[LabelRole] == RoleSandbox {
			limit = s.config.TTL
		}
		if expired(item.Labels, now, limit) {
			s.removeQuietly(item.Name)
			if id := item.Labels[LabelID]; id != "" && item.Labels[LabelRole] == RoleSandbox {
				s.removeVolumeQuietly(s.volumeName(id))
			}
			continue
		}
		if item.Labels[LabelRole] == RoleSandbox {
			live[item.Labels[LabelID]] = true
		}
	}
	volumes, err := s.driver.ListVolumes(ctx, s.managedLabels())
	if err != nil {
		s.logger.Printf("sandbox: reaper could not list volumes: %v", err)
		return
	}
	for _, volume := range volumes {
		if !live[volume.Labels[LabelID]] && expired(volume.Labels, now, s.config.TTL) {
			s.removeVolumeQuietly(volume.Name)
		}
	}
}

func expired(labels map[string]string, now time.Time, limit time.Duration) bool {
	created, err := strconv.ParseInt(labels[LabelCreated], 10, 64)
	return err == nil && now.Sub(time.Unix(created, 0)) > limit
}

// Run verifies the runtime at startup and keeps verifying and reaping until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.background(ctx)
	verify := time.NewTicker(verifyInterval)
	defer verify.Stop()
	reap := time.NewTicker(time.Minute)
	defer reap.Stop()
	disk := time.NewTicker(s.config.DiskCheck)
	defer disk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-verify.C:
			if err := s.verify(ctx, true); err != nil {
				s.logger.Printf("sandbox: %v", err)
			}
		case <-reap.C:
			s.Reap(ctx)
		case <-disk.C:
			s.SweepDisk(ctx)
		}
	}
}

func (s *Service) background(ctx context.Context) {
	s.Reap(ctx)
	if err := s.verify(ctx, true); err != nil {
		s.logger.Printf("sandbox: %v", err)
		return
	}
	s.logger.Printf("sandbox: runtime %s verified", s.config.Runtime)
}

// scrub removes the credential and anything secret-shaped from text that might reach
// a log or a response. Git quotes URLs and server messages in its errors.
func scrub(text, secret string) string {
	if secret != "" {
		text = strings.ReplaceAll(text, secret, redact.Mask)
	}
	text = redact.String(text)
	if len(text) > 400 {
		text = text[:400] + "..."
	}
	return strings.ToValidUTF8(text, "�")
}
