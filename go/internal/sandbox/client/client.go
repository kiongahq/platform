// Package client is the typed Go client for the sandbox manager's internal HTTP
// API, the contract in contracts/agent-team/sandbox-api.v1.schema.json. It holds
// the wire types and imports nothing from the manager, so the gateway gets no
// engine code by depending on it. Tests use Fake instead of a real manager.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Error codes the manager returns in Error.Code.
const (
	CodeUnavailable     = "sandbox_unavailable"
	CodeNotFound        = "not_found"
	CodeInvalid         = "invalid_request"
	CodeUnauthorized    = "unauthorized"
	CodePathRejected    = "path_rejected"
	CodeHostNotAllowed  = "host_not_allowed"
	CodeBranchRejected  = "branch_rejected"
	CodeImageNotAllowed = "image_not_allowed"
	CodeTooLarge        = "too_large"
	CodeConflict        = "conflict"
	CodeGitFailed       = "git_failed"
	CodeInternal        = "internal_error"
	// CodeQuotaExceeded means the sandbox was stopped for its workspace disk limit. Reads
	// still work; anything that writes or runs is refused until the sandbox is deleted.
	CodeQuotaExceeded = "workspace_quota_exceeded"
)

// Client is what the gateway depends on. HTTP and Fake implement it.
type Client interface {
	Health(ctx context.Context) (Health, error)
	Create(ctx context.Context, request CreateRequest) (Sandbox, error)
	Exec(ctx context.Context, id string, request ExecRequest) (ExecResult, error)
	ReadPath(ctx context.Context, id, path string) (PathContent, error)
	WriteFile(ctx context.Context, id, path string, content []byte) (WriteResult, error)
	GitClone(ctx context.Context, id string, request GitCloneRequest) (GitCloneResult, error)
	GitPush(ctx context.Context, id string, request GitPushRequest) (GitPushResult, error)
	GitDiff(ctx context.Context, id string) (GitDiffResult, error)
	Delete(ctx context.Context, id string) error
}

// Health is GET /healthz. RuntimeVerified false means creates return 503.
// UnsafeRuntime true means KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME is in effect, so
// sandboxes may not be running under gVisor; the console shows a warning for it.
type Health struct {
	Runtime         string `json:"runtime"`
	RuntimeVerified bool   `json:"runtime_verified"`
	Driver          string `json:"driver"`
	UnsafeRuntime   bool   `json:"unsafe_runtime"`
}

// CreateRequest is POST /sandboxes. Idempotent on RunID and WorkerID. The contract
// has no field for environment, mounts, runtime, privileges or network on purpose.
type CreateRequest struct {
	RunID     string `json:"run_id"`
	WorkerID  string `json:"worker_id"`
	ProjectID string `json:"project_id"`
	Image     string `json:"image,omitempty"`
}

// Sandbox identifies a sandbox.
type Sandbox struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// ExecRequest is POST /sandboxes/{id}/exec. Zero TimeoutSeconds means the default.
type ExecRequest struct {
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// ExecResult holds untrusted output. A timed-out command has exit code -1.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

// DirEntry is one directory listing row. Type is file, directory, symlink or other.
type DirEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

// PathContent is GET /sandboxes/{id}/files: a file, or a listing when IsDirectory.
type PathContent struct {
	IsDirectory bool
	Content     string
	Entries     []DirEntry
	Truncated   bool
}

type pathContentWire struct {
	Content   *string    `json:"content,omitempty"`
	Truncated bool       `json:"truncated"`
	Entries   []DirEntry `json:"entries,omitempty"`
}

// MarshalJSON writes one of the contract's two shapes. An empty directory still
// needs "entries": [], which omitempty on the wire struct would drop.
func (p PathContent) MarshalJSON() ([]byte, error) {
	if p.IsDirectory {
		entries := p.Entries
		if entries == nil {
			entries = []DirEntry{}
		}
		return json.Marshal(struct {
			Entries   []DirEntry `json:"entries"`
			Truncated bool       `json:"truncated"`
		}{entries, p.Truncated})
	}
	return json.Marshal(struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}{p.Content, p.Truncated})
}

// UnmarshalJSON reads either shape.
func (p *PathContent) UnmarshalJSON(raw []byte) error {
	var wire pathContentWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	p.Truncated, p.Entries = wire.Truncated, wire.Entries
	if wire.Content != nil {
		p.Content = *wire.Content
		return nil
	}
	p.IsDirectory = true
	return nil
}

// WriteResult is PUT /sandboxes/{id}/files.
type WriteResult struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytes_written"`
}

// Credential is a git secret for one call. Its formatted forms never show the
// value, so a stray log line cannot leak it. Only encoding/json sees the value.
type Credential string

// String hides the value from fmt.
func (Credential) String() string { return "[REDACTED]" }

// GoString hides the value from %#v.
func (Credential) GoString() string { return "[REDACTED]" }

// GitCloneRequest is POST /sandboxes/{id}/git/clone. The clone lands in
// /workspace/<repository name>.
type GitCloneRequest struct {
	Repository string     `json:"repository"`
	Ref        string     `json:"ref,omitempty"`
	Credential Credential `json:"credential,omitempty"`
}

// GitCloneResult carries the commit that was checked out. It is empty-free except
// for an empty remote repository, which the manager reports as an error.
type GitCloneResult struct {
	Commit string `json:"commit"`
}

// GitPushRequest is POST /sandboxes/{id}/git/push. Branch must be
// kionga/<this sandbox's run id>/<name>.
type GitPushRequest struct {
	Repository string     `json:"repository"`
	Branch     string     `json:"branch"`
	Credential Credential `json:"credential"`
}

// GitPushResult names what reached the remote.
type GitPushResult struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

// GitDiffResult is untrusted text for every repository in the workspace.
type GitDiffResult struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

// Error is a manager failure. Messages never contain a credential.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("sandbox manager: %s (%d): %s", e.Code, e.Status, e.Message)
}

// IsUnavailable reports whether the sandbox subsystem cannot serve the call. The
// gateway turns this into the sandbox_unavailable tool result. A transport failure
// counts: a manager that is down is unavailable.
func IsUnavailable(err error) bool {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Code == CodeUnavailable
	}
	return err != nil
}

// Code returns the manager error code, or "" for a transport failure or nil.
func Code(err error) string {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

// maxResponse bounds what the client reads from the manager. Files are capped well
// below this on the manager side.
const maxResponse = 32 << 20

// HTTP calls a real manager. Deadlines come from the caller's context.
type HTTP struct {
	BaseURL string
	Token   string
	// Client defaults to a client with no overall timeout, because exec may
	// legitimately run for 30 minutes.
	Client *http.Client
}

// New returns a client for the manager at baseURL.
func New(baseURL, token string) *HTTP {
	return &HTTP{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Client: &http.Client{}}
}

func (c *HTTP) call(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, output any) error {
	target := c.BaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	httpClient := c.Client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request) // #nosec G704 -- the URL is the operator-configured manager address
	if err != nil {
		return fmt.Errorf("sandbox manager unreachable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("sandbox manager response: %w", err)
	}
	if response.StatusCode >= 400 {
		failure := &Error{Status: response.StatusCode}
		if json.Unmarshal(raw, failure) != nil || failure.Code == "" {
			failure.Code, failure.Message = CodeInternal, "unexpected manager response"
		}
		return failure
	}
	if output != nil {
		if err := json.Unmarshal(raw, output); err != nil {
			return fmt.Errorf("sandbox manager response: %w", err)
		}
	}
	return nil
}

func (c *HTTP) callJSON(ctx context.Context, method, path string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return c.call(ctx, method, path, nil, bytes.NewReader(raw), "application/json", output)
}

func sandboxPath(id, suffix string) string {
	return "/sandboxes/" + url.PathEscape(id) + suffix
}

// Health reads GET /healthz. It sends the token, which the route ignores.
func (c *HTTP) Health(ctx context.Context) (Health, error) {
	var health Health
	err := c.call(ctx, http.MethodGet, "/healthz", nil, nil, "", &health)
	return health, err
}

// Create starts a sandbox, or returns the existing one for the run and worker.
func (c *HTTP) Create(ctx context.Context, request CreateRequest) (Sandbox, error) {
	var sandbox Sandbox
	err := c.callJSON(ctx, http.MethodPost, "/sandboxes", request, &sandbox)
	return sandbox, err
}

// Exec runs a command. The caller's context should outlive TimeoutSeconds: the
// manager enforces the limit and returns TimedOut. Cancelling early kills the command.
func (c *HTTP) Exec(ctx context.Context, id string, request ExecRequest) (ExecResult, error) {
	var result ExecResult
	err := c.callJSON(ctx, http.MethodPost, sandboxPath(id, "/exec"), request, &result)
	return result, err
}

// ReadPath reads a file or lists a directory.
func (c *HTTP) ReadPath(ctx context.Context, id, path string) (PathContent, error) {
	var content PathContent
	err := c.call(ctx, http.MethodGet, sandboxPath(id, "/files"), url.Values{"path": {path}}, nil, "", &content)
	return content, err
}

// WriteFile replaces a file, creating parent directories.
func (c *HTTP) WriteFile(ctx context.Context, id, path string, content []byte) (WriteResult, error) {
	var result WriteResult
	err := c.call(ctx, http.MethodPut, sandboxPath(id, "/files"), url.Values{"path": {path}}, bytes.NewReader(content), "application/octet-stream", &result)
	return result, err
}

// GitClone clones through a short-lived credentialed helper.
func (c *HTTP) GitClone(ctx context.Context, id string, request GitCloneRequest) (GitCloneResult, error) {
	var result GitCloneResult
	err := c.callJSON(ctx, http.MethodPost, sandboxPath(id, "/git/clone"), wireClone{request.Repository, request.Ref, string(request.Credential)}, &result)
	return result, err
}

// GitPush pushes a branch through a short-lived credentialed helper.
func (c *HTTP) GitPush(ctx context.Context, id string, request GitPushRequest) (GitPushResult, error) {
	var result GitPushResult
	err := c.callJSON(ctx, http.MethodPost, sandboxPath(id, "/git/push"), wirePush{request.Repository, request.Branch, string(request.Credential)}, &result)
	return result, err
}

// GitDiff returns the changes in the sandbox's repositories.
func (c *HTTP) GitDiff(ctx context.Context, id string) (GitDiffResult, error) {
	var result GitDiffResult
	err := c.call(ctx, http.MethodGet, sandboxPath(id, "/git/diff"), nil, nil, "", &result)
	return result, err
}

// Delete removes the sandbox and its workspace. Deleting a missing one succeeds.
func (c *HTTP) Delete(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, sandboxPath(id, ""), nil, nil, "", nil)
}

type wireClone struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
	Credential string `json:"credential,omitempty"`
}

type wirePush struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Credential string `json:"credential"`
}

// DefaultRequestTimeout is a sensible per-call deadline for non-exec calls.
const DefaultRequestTimeout = 2 * time.Minute
