package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/redact"
)

func bytesReader(raw []byte) *bytes.Reader { return bytes.NewReader(raw) }

func marshalEvent(value any) ([]byte, error) { return json.Marshal(value) }

// --- Runner: POST /agent-turns, one model call, stored nowhere ---

type turnToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type turnMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []turnToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// turnRequest is contracts agent-team/runner-turn-request.v1. It has no field
// that could carry a credential.
type turnRequest struct {
	RunID           string           `json:"run_id"`
	WorkerID        string           `json:"worker_id"`
	Turn            int              `json:"turn"`
	Model           string           `json:"model"`
	System          string           `json:"system"`
	Messages        []turnMessage    `json:"messages"`
	Tools           []agentteam.Tool `json:"tools"`
	MaxOutputTokens int              `json:"max_output_tokens"`
}

type turnError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

// turnResponse is contracts agent-team/runner-turn-response.v1.
type turnResponse struct {
	Content   string                `json:"content"`
	ToolCalls []agentteam.ModelCall `json:"tool_calls"`
	Usage     agentteam.Usage       `json:"usage"`
	Model     string                `json:"model"`
	Error     *turnError            `json:"error,omitempty"`
}

// teamRunner performs one model call. A transport failure is an error; a provider
// failure is a 200 with Error set, as the contract defines.
type teamRunner interface {
	Turn(ctx context.Context, request turnRequest) (turnResponse, error)
}

type httpTeamRunner struct{}

// teamTurnTimeout outlasts the runner's own limit, so the runner reports a slow
// provider as an error body instead of the gateway cutting the call off.
func teamTurnTimeout() time.Duration {
	seconds := 600
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KIONGA_AGENT_TURN_TIMEOUT_SECONDS"))); err == nil && value > 0 && value <= 86400 {
		seconds = value
	}
	return time.Duration(seconds+30) * time.Second
}

func (httpTeamRunner) Turn(ctx context.Context, request turnRequest) (turnResponse, error) {
	base := runnerURL()
	if base == "" {
		return turnResponse{}, errors.New("KIONGA_AGENT_DESIGNER_RUNNER_URL is not set")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return turnResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, teamTurnTimeout())
	defer cancel()
	call, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/agent-turns", bytes.NewReader(body)) // #nosec G704 -- runner URL is operator configuration
	if err != nil {
		return turnResponse{}, err
	}
	call.Header.Set("Content-Type", "application/json")
	call.Header.Set("Authorization", "Bearer "+os.Getenv("KIONGA_AGENT_DESIGNER_RUNNER_TOKEN"))
	response, err := runner.client.Do(call) // #nosec G704 -- fixed operator-configured runner, redirects disabled
	if err != nil {
		return turnResponse{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return turnResponse{}, err
	}
	if response.StatusCode != http.StatusOK {
		return turnResponse{}, fmt.Errorf("runner returned %d", response.StatusCode)
	}
	var parsed turnResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return turnResponse{}, errors.New("runner returned an invalid response")
	}
	return parsed, nil
}

// --- Sandbox manager: contracts agent-team/sandbox-api.v1 ---

type sandboxExecRequest struct {
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type sandboxExecResponse struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

type sandboxEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type sandboxFiles struct {
	Content   *string        `json:"content,omitempty"`
	Entries   []sandboxEntry `json:"entries,omitempty"`
	Truncated bool           `json:"truncated"`
}

type sandboxDiff struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

type sandboxPushResult struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

type sandboxHealth struct {
	Runtime         string `json:"runtime"`
	RuntimeVerified bool   `json:"runtime_verified"`
	Driver          string `json:"driver"`
	UnsafeRuntime   bool   `json:"unsafe_runtime"`
}

// sandboxError is the manager's {error,message}. Unavailable means the manager or
// its runtime cannot serve, which the model sees as sandbox_unavailable.
type sandboxError struct{ Code, Message string }

func (e *sandboxError) Error() string { return e.Code + ": " + e.Message }

// teamSandbox is the gateway's view of the sandbox manager.
type teamSandbox interface {
	Health(ctx context.Context) (sandboxHealth, error)
	Create(ctx context.Context, runID, workerID, projectID string) (string, error)
	Exec(ctx context.Context, id string, request sandboxExecRequest) (sandboxExecResponse, error)
	Files(ctx context.Context, id, path string) (sandboxFiles, error)
	Write(ctx context.Context, id, path, content string) error
	Clone(ctx context.Context, id, repository, ref, credential string) (string, error)
	Push(ctx context.Context, id, repository, branch, credential string) (sandboxPushResult, error)
	Diff(ctx context.Context, id string) (sandboxDiff, error)
	Delete(ctx context.Context, id string) error
}

type httpTeamSandbox struct{ client *http.Client }

var defaultTeamSandbox = &httpTeamSandbox{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

func sandboxBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("KIONGA_SANDBOX_URL")), "/")
}

// do calls the manager. credential is scrubbed from anything the manager says,
// so an error can never carry it into a transcript.
func (c *httpTeamSandbox) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType, credential string, out any) error {
	base := sandboxBaseURL()
	if base == "" {
		return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "KIONGA_SANDBOX_URL is not set"}
	}
	// Read per call, so it is the value runtimeconfig.Load set from
	// KIONGA_SANDBOX_TOKEN_FILE. An empty bearer is never sent.
	token := strings.TrimSpace(os.Getenv("KIONGA_SANDBOX_TOKEN"))
	if token == "" {
		return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "KIONGA_SANDBOX_TOKEN is not set"}
	}
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body) // #nosec G704 -- sandbox manager URL is operator configuration
	if err != nil {
		return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "invalid sandbox manager request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.client.Do(request) // #nosec G704 -- fixed operator-configured manager, redirects disabled
	if err != nil {
		return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "the sandbox manager is unreachable"}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "the sandbox manager response was cut off"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}{}
		_ = json.Unmarshal(raw, &failure)
		code, message := failure.Error, failure.Message
		if code == "" || response.StatusCode == http.StatusServiceUnavailable {
			code = agentteam.ErrSandboxUnavailable
		}
		if credential != "" {
			message = strings.ReplaceAll(message, credential, redact.Mask)
		}
		return &sandboxError{Code: code, Message: redact.String(message)}
	}
	if out != nil && response.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(raw, out); err != nil {
			return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "the sandbox manager returned invalid JSON"}
		}
	}
	return nil
}

func (c *httpTeamSandbox) doJSON(ctx context.Context, method, path string, in any, credential string, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, method, path, nil, bytes.NewReader(raw), "application/json", credential, out)
}

func (c *httpTeamSandbox) Health(ctx context.Context) (sandboxHealth, error) {
	var health sandboxHealth
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return health, c.do(ctx, http.MethodGet, "/healthz", nil, nil, "", "", &health)
}

func (c *httpTeamSandbox) Create(ctx context.Context, runID, workerID, projectID string) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/sandboxes", map[string]string{"run_id": runID, "worker_id": workerID, "project_id": projectID}, "", &created)
	return created.ID, err
}

func sandboxPath(id string) string { return "/sandboxes/" + url.PathEscape(id) }

func (c *httpTeamSandbox) Exec(ctx context.Context, id string, request sandboxExecRequest) (sandboxExecResponse, error) {
	var out sandboxExecResponse
	return out, c.doJSON(ctx, http.MethodPost, sandboxPath(id)+"/exec", request, "", &out)
}

func (c *httpTeamSandbox) Files(ctx context.Context, id, path string) (sandboxFiles, error) {
	var out sandboxFiles
	return out, c.do(ctx, http.MethodGet, sandboxPath(id)+"/files", url.Values{"path": {path}}, nil, "", "", &out)
}

func (c *httpTeamSandbox) Write(ctx context.Context, id, path, content string) error {
	return c.do(ctx, http.MethodPut, sandboxPath(id)+"/files", url.Values{"path": {path}}, strings.NewReader(content), "application/octet-stream", "", nil)
}

func (c *httpTeamSandbox) Clone(ctx context.Context, id, repository, ref, credential string) (string, error) {
	in := map[string]string{"repository": repository}
	if ref != "" {
		in["ref"] = ref
	}
	if credential != "" {
		in["credential"] = credential
	}
	var out struct {
		Commit string `json:"commit"`
	}
	return out.Commit, c.doJSON(ctx, http.MethodPost, sandboxPath(id)+"/git/clone", in, credential, &out)
}

func (c *httpTeamSandbox) Push(ctx context.Context, id, repository, branch, credential string) (sandboxPushResult, error) {
	var out sandboxPushResult
	return out, c.doJSON(ctx, http.MethodPost, sandboxPath(id)+"/git/push", map[string]string{"repository": repository, "branch": branch, "credential": credential}, credential, &out)
}

func (c *httpTeamSandbox) Diff(ctx context.Context, id string) (sandboxDiff, error) {
	var out sandboxDiff
	return out, c.do(ctx, http.MethodGet, sandboxPath(id)+"/git/diff", nil, nil, "", "", &out)
}

func (c *httpTeamSandbox) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, sandboxPath(id), nil, nil, "", "", nil)
}

// --- Sandbox status on a run view ---

// sandboxStatus is contracts sandbox-api.v1 $defs/run_view_sandbox.
type sandboxStatus struct {
	Available       bool   `json:"available"`
	Runtime         string `json:"runtime,omitempty"`
	RuntimeVerified bool   `json:"runtime_verified"`
	Driver          string `json:"driver,omitempty"`
	UnsafeRuntime   bool   `json:"unsafe_runtime"`
	Reason          string `json:"reason,omitempty"`
}

const sandboxHealthTTL = 10 * time.Second

// sandboxHealthCache keeps run views from calling /healthz per request.
type sandboxHealthCache struct {
	mu    sync.Mutex
	at    time.Time
	value sandboxStatus
}

// status never reports a sandbox as available by default: only a manager that
// answered, with a verified runtime or an explicitly allowed unsafe one, is.
func (c *sandboxHealthCache) status(ctx context.Context, sandbox teamSandbox) sandboxStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < sandboxHealthTTL {
		return c.value
	}
	health, err := sandbox.Health(ctx)
	if err != nil {
		c.value = sandboxStatus{Reason: agentteam.ErrSandboxUnavailable}
	} else {
		c.value = sandboxStatus{Available: health.RuntimeVerified || health.UnsafeRuntime, Runtime: health.Runtime, RuntimeVerified: health.RuntimeVerified, Driver: health.Driver, UnsafeRuntime: health.UnsafeRuntime}
		if !c.value.Available {
			c.value.Reason = agentteam.ErrSandboxUnavailable
		}
	}
	c.at = time.Now()
	return c.value
}
