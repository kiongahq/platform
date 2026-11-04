// Package sandbox runs model-chosen commands in isolated containers. All commands,
// file contents, repository contents and output are treated as hostile. Service
// holds every policy decision; a Driver only talks to a container engine, so the
// isolation rules are the same on Docker, Podman and Kubernetes.
package sandbox

import (
	"context"
	"errors"
	"io"
	"time"
)

// Labels on everything the manager creates. They are the only state: the manager
// keeps none of its own, and only ever touches objects carrying its own prefix.
const (
	LabelManaged = "kionga.dev/sandbox-managed"
	LabelPrefix  = "kionga.dev/sandbox-prefix"
	// LabelRole is sandbox, git-helper or probe. Kubernetes egress is allowed only
	// for git-helper, so a sandbox must never carry that value.
	LabelRole    = "kionga.dev/sandbox-role"
	LabelID      = "kionga.dev/sandbox-id"
	LabelRun     = "kionga.dev/sandbox-run"
	LabelWorker  = "kionga.dev/sandbox-worker"
	LabelProject = "kionga.dev/sandbox-project"
	LabelCreated = "kionga.dev/sandbox-created"
)

// Roles.
const (
	RoleSandbox   = "sandbox"
	RoleGitHelper = "git-helper"
	RoleProbe     = "probe"
)

// Fixed container layout.
const (
	UID           = 10001
	GID           = 10001
	WorkspacePath = "/workspace"
	NetworkNone   = "none"
)

// Driver errors.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// TmpfsMount is a memory-backed mount the sandbox user can write.
type TmpfsMount struct {
	Path      string
	SizeBytes int64
}

// VolumeMount is the per-sandbox workspace.
type VolumeMount struct {
	Name      string
	Path      string
	SizeBytes int64
}

// HostEntry pins a name to an address in the instance's hosts file. Only the manager
// builds these, from a host it resolved and checked. No request field reaches them.
type HostEntry struct {
	Host    string
	Address string
}

// ContainerSpec is everything a driver may vary. There is deliberately no field for
// environment, host mounts, capabilities or privileges: drivers hard-code the
// hardened values, so no caller can weaken them.
type ContainerSpec struct {
	Name        string
	Image       string
	Runtime     string
	Labels      map[string]string
	Command     []string
	Network     string // NetworkNone, or the helper's network name
	MemoryBytes int64
	NanoCPUs    int64
	Pids        int64
	Tmpfs       []TmpfsMount
	Volume      *VolumeMount
	ExtraHosts  []HostEntry
}

// Instance is a container or pod as the engine reports it.
type Instance struct {
	Name    string
	Labels  map[string]string
	Running bool
	// Runtime is what the engine says the instance runs on, or "" when it cannot say.
	Runtime string
}

// VolumeInfo is a labelled workspace volume.
type VolumeInfo struct {
	Name   string
	Labels map[string]string
}

// ExecRequest runs Argv as the sandbox user. Stdin may be nil. A driver must stop
// and return when ctx ends, whatever the process is doing, and must stop when a
// writer fails.
type ExecRequest struct {
	Argv   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Driver is one container engine.
type Driver interface {
	Name() string
	// RuntimeRegistered asks the engine whether the runtime is known. Treat true as
	// necessary but not sufficient: Podman lists runtimes it has no binary for.
	RuntimeRegistered(ctx context.Context, runtime string) (bool, error)
	// Create makes and starts the instance and its volume. ErrExists means an
	// instance with that name is already there and nothing was changed. On any
	// other error the caller removes what may be left.
	Create(ctx context.Context, spec ContainerSpec) error
	Inspect(ctx context.Context, name string) (Instance, error)
	List(ctx context.Context, labels map[string]string) ([]Instance, error)
	ListVolumes(ctx context.Context, labels map[string]string) ([]VolumeInfo, error)
	// EnsureNetwork makes the helpers' own network. It refuses a network of that name
	// that this manager did not create, because that could be shared with services.
	EnsureNetwork(ctx context.Context, name string, labels map[string]string) error
	// Exec returns the exit code. A non-nil error means no exit code is known.
	Exec(ctx context.Context, name string, request ExecRequest) (int, error)
	// Restart kills every process in the instance and starts it again, keeping the
	// workspace. It is the recovery path when an exec cannot even start.
	Restart(ctx context.Context, name string) error
	// Remove is force-removal. A missing instance is not an error.
	Remove(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
}

// Clock is injected so the reaper is tested without sleeping.
type Clock func() time.Time
