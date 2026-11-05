package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Fake is an in-memory Client for gateway tests. It models the states the gateway
// must handle: unavailable, unsafe runtime, per-worker sandboxes, a file tree and
// scripted exec results. It does not model isolation.
type Fake struct {
	mu sync.Mutex
	// Unavailable makes every call but Health return 503 sandbox_unavailable.
	Unavailable bool
	// Unsafe is reported as Health.UnsafeRuntime.
	Unsafe bool
	// OnExec scripts exec results. Nil returns exit code 0 and empty output.
	OnExec func(id string, request ExecRequest) (ExecResult, error)
	// Err, when set, is returned by every call but Health.
	Err error

	sandboxes map[string]*fakeSandbox
	// Execs, Clones, Pushes and Deleted record calls, in order, for assertions.
	Execs   []ExecRequest
	Clones  []GitCloneRequest
	Pushes  []GitPushRequest
	Deleted []string
}

type fakeSandbox struct {
	files map[string][]byte
}

var _ Client = (*Fake)(nil)

func (f *Fake) gate() error {
	if f.Err != nil {
		return f.Err
	}
	if f.Unavailable {
		return &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: "fake sandbox manager is unavailable"}
	}
	return nil
}

func (f *Fake) sandbox(id string) (*fakeSandbox, error) {
	if err := f.gate(); err != nil {
		return nil, err
	}
	sandbox, ok := f.sandboxes[id]
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: "no such sandbox"}
	}
	return sandbox, nil
}

// Health reports the fake's configured state.
func (f *Fake) Health(context.Context) (Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Health{Runtime: "fake", RuntimeVerified: !f.Unavailable, Driver: "docker", UnsafeRuntime: f.Unsafe}, nil
}

// Create is idempotent on run and worker, like the manager.
func (f *Fake) Create(_ context.Context, request CreateRequest) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.gate(); err != nil {
		return Sandbox{}, err
	}
	sum := sha256.Sum256([]byte(request.ProjectID + "\x00" + request.RunID + "\x00" + request.WorkerID))
	id := "sbx-" + hex.EncodeToString(sum[:12])
	if f.sandboxes == nil {
		f.sandboxes = map[string]*fakeSandbox{}
	}
	if _, ok := f.sandboxes[id]; !ok {
		f.sandboxes[id] = &fakeSandbox{files: map[string][]byte{}}
	}
	return Sandbox{ID: id, Status: "running"}, nil
}

// Exec records the call and applies OnExec.
func (f *Fake) Exec(_ context.Context, id string, request ExecRequest) (ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.sandbox(id); err != nil {
		return ExecResult{}, err
	}
	f.Execs = append(f.Execs, request)
	if f.OnExec != nil {
		return f.OnExec(id, request)
	}
	return ExecResult{}, nil
}

func fakePath(path string) (string, error) {
	path = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(path, "/workspace"), "/"), "/")
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", &Error{Status: http.StatusForbidden, Code: CodePathRejected, Message: "path is outside /workspace"}
		}
	}
	if path == "." {
		path = ""
	}
	return path, nil
}

// ReadPath returns a stored file, or lists the entries directly under a directory.
func (f *Fake) ReadPath(_ context.Context, id, path string) (PathContent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sandbox, err := f.sandbox(id)
	if err != nil {
		return PathContent{}, err
	}
	key, err := fakePath(path)
	if err != nil {
		return PathContent{}, err
	}
	if content, ok := sandbox.files[key]; ok {
		return PathContent{Content: string(content)}, nil
	}
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	seen := map[string]string{}
	for name := range sandbox.files {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		head, _, nested := strings.Cut(rest, "/")
		if nested {
			seen[head] = "directory"
		} else {
			seen[head] = "file"
		}
	}
	if len(seen) == 0 && key != "" {
		return PathContent{}, &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: "no such path"}
	}
	listing := PathContent{IsDirectory: true, Entries: []DirEntry{}}
	for name, kind := range seen {
		listing.Entries = append(listing.Entries, DirEntry{Name: name, Type: kind})
	}
	sort.Slice(listing.Entries, func(i, j int) bool { return listing.Entries[i].Name < listing.Entries[j].Name })
	return listing, nil
}

// WriteFile stores content.
func (f *Fake) WriteFile(_ context.Context, id, path string, content []byte) (WriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sandbox, err := f.sandbox(id)
	if err != nil {
		return WriteResult{}, err
	}
	key, err := fakePath(path)
	if err != nil {
		return WriteResult{}, err
	}
	sandbox.files[key] = append([]byte(nil), content...)
	return WriteResult{Path: "/workspace/" + key, BytesWritten: int64(len(content))}, nil
}

// GitClone records the call.
func (f *Fake) GitClone(_ context.Context, id string, request GitCloneRequest) (GitCloneResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.sandbox(id); err != nil {
		return GitCloneResult{}, err
	}
	f.Clones = append(f.Clones, request)
	return GitCloneResult{Commit: strings.Repeat("a", 40)}, nil
}

// GitPush records the call.
func (f *Fake) GitPush(_ context.Context, id string, request GitPushRequest) (GitPushResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.sandbox(id); err != nil {
		return GitPushResult{}, err
	}
	f.Pushes = append(f.Pushes, request)
	return GitPushResult{Branch: request.Branch, Commit: strings.Repeat("b", 40)}, nil
}

// GitDiff returns an empty diff.
func (f *Fake) GitDiff(_ context.Context, id string) (GitDiffResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.sandbox(id); err != nil {
		return GitDiffResult{}, err
	}
	return GitDiffResult{}, nil
}

// Delete forgets the sandbox. Deleting a missing one succeeds.
func (f *Fake) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.gate(); err != nil {
		return err
	}
	delete(f.sandboxes, id)
	f.Deleted = append(f.Deleted, id)
	return nil
}
