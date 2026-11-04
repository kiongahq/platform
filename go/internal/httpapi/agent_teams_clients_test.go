package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/runtimeconfig"
	"github.com/kiongahq/platform/internal/store"
)

func TestTeamHTTPRunnerContract(t *testing.T) {
	token := "runner-" + "test-value"
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_TOKEN", token)
	var body turnRequest
	responses := map[string]string{
		"ok":      `{"content":"hi","tool_calls":[{"id":"c1","name":"exec","arguments":{"command":"ls"}}],"usage":{"input_tokens":3,"output_tokens":4},"model":"m"}`,
		"limited": `{"error":{"code":"rate_limited","message":"slow","retry_after_seconds":30}}`,
	}
	var send string
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent-turns" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, send)
	}))
	defer server.Close()
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", server.URL)
	request := turnRequest{RunID: "atr-" + strings.Repeat("a", 24), WorkerID: "atw-" + strings.Repeat("b", 24), Turn: 1, Model: "llm", System: "s", Messages: []turnMessage{{Role: "user", Content: "x"}}, Tools: []agentteam.Tool{}, MaxOutputTokens: 8000}
	validateContract(t, "turn-request", request)
	send = responses["ok"]
	got, err := httpTeamRunner{}.Turn(context.Background(), request)
	if err != nil || got.Content != "hi" || got.ToolCalls[0].Name != "exec" || got.Usage.Total() != 7 {
		t.Fatalf("%+v %v", got, err)
	}
	if body.RunID != request.RunID || body.MaxOutputTokens != 8000 {
		t.Fatalf("%+v", body)
	}
	send = responses["limited"]
	if got, err = (httpTeamRunner{}).Turn(context.Background(), request); err != nil || got.Error == nil || got.Error.RetryAfterSeconds != 30 {
		t.Fatalf("%+v %v", got, err)
	}
	status = 500
	if _, err = (httpTeamRunner{}).Turn(context.Background(), request); err == nil {
		t.Fatal("500 accepted")
	}
	status, send = 200, "not json"
	if _, err = (httpTeamRunner{}).Turn(context.Background(), request); err == nil {
		t.Fatal("bad JSON accepted")
	}
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", "")
	if _, err = (httpTeamRunner{}).Turn(context.Background(), request); err == nil {
		t.Fatal("unconfigured runner accepted")
	}
}

// An in-process manager checks every request against the contract.
func TestTeamHTTPSandboxClientContract(t *testing.T) {
	token := "sandbox-" + "test-value"
	credential := "git-" + "secret-value"
	t.Setenv("KIONGA_SANDBOX_TOKEN", token)
	bodies := map[string]string{}
	var mu sync.Mutex
	files := "file body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("no bearer on %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.Method+" "+r.URL.Path] = string(raw)
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /healthz":
			_, _ = io.WriteString(w, `{"runtime":"runsc","runtime_verified":true,"driver":"docker","unsafe_runtime":false}`)
		case "POST /sandboxes":
			_, _ = io.WriteString(w, `{"id":"sbx","status":"running"}`)
		case "POST /sandboxes/sbx/exec":
			_, _ = io.WriteString(w, `{"exit_code":0,"stdout":"out","stderr":"","truncated":false,"timed_out":false,"duration_ms":1}`)
		case "GET /sandboxes/sbx/files":
			if r.URL.Query().Get("path") == "dir" {
				_, _ = io.WriteString(w, `{"entries":[{"name":"a","type":"file"}]}`)
				return
			}
			_, _ = io.WriteString(w, mustJSON(t, map[string]any{"content": files, "truncated": false}))
		case "PUT /sandboxes/sbx/files":
			_, _ = io.WriteString(w, `{"path":"a","bytes_written":1}`)
		case "POST /sandboxes/sbx/git/clone":
			_, _ = io.WriteString(w, `{"commit":"`+strings.Repeat("a", 40)+`"}`)
		case "POST /sandboxes/sbx/git/push":
			w.WriteHeader(422)
			_, _ = io.WriteString(w, mustJSON(t, map[string]string{"error": "branch_rejected", "message": "bad push with " + credential}))
		case "GET /sandboxes/sbx/git/diff":
			_, _ = io.WriteString(w, `{"diff":"d","truncated":false}`)
		case "DELETE /sandboxes/sbx":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("KIONGA_SANDBOX_URL", server.URL+"/")
	ctx := context.Background()
	client := defaultTeamSandbox
	health, err := client.Health(ctx)
	if err != nil || !health.RuntimeVerified || health.Driver != "docker" {
		t.Fatalf("%+v %v", health, err)
	}
	validateContract(t, "run-view-sandbox", sandboxStatus{Available: true, Runtime: health.Runtime, RuntimeVerified: true, Driver: health.Driver})
	run, worker := "atr-"+strings.Repeat("a", 24), "atw-"+strings.Repeat("b", 24)
	if id, err := client.Create(ctx, run, worker, "p"); err != nil || id != "sbx" {
		t.Fatalf("%s %v", id, err)
	}
	if out, err := client.Exec(ctx, "sbx", sandboxExecRequest{Command: "ls", Cwd: "src", TimeoutSeconds: 5}); err != nil || out.Stdout != "out" {
		t.Fatalf("%+v %v", out, err)
	}
	if out, err := client.Files(ctx, "sbx", "a.txt"); err != nil || *out.Content != files {
		t.Fatalf("%+v %v", out, err)
	}
	if out, err := client.Files(ctx, "sbx", "dir"); err != nil || out.Content != nil || out.Entries[0].Name != "a" {
		t.Fatalf("%+v %v", out, err)
	}
	if err := client.Write(ctx, "sbx", "a.txt", "bytes"); err != nil || bodies["PUT /sandboxes/sbx/files"] != "bytes" {
		t.Fatalf("%v %q", err, bodies["PUT /sandboxes/sbx/files"])
	}
	if commit, err := client.Clone(ctx, "sbx", "https://github.com/o/r", "main", credential); err != nil || commit == "" {
		t.Fatalf("%s %v", commit, err)
	}
	if !strings.Contains(bodies["POST /sandboxes/sbx/git/clone"], credential) {
		t.Fatal("clone lost its credential")
	}
	_, err = client.Push(ctx, "sbx", "https://github.com/o/r", "kionga/"+run+"/x", credential)
	var failure *sandboxError
	if !errors.As(err, &failure) || failure.Code != "branch_rejected" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), credential) {
		t.Fatalf("credential in an error: %v", err)
	}
	if diff, err := client.Diff(ctx, "sbx"); err != nil || diff.Diff != "d" {
		t.Fatalf("%+v %v", diff, err)
	}
	if err := client.Delete(ctx, "sbx"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bodies["POST /sandboxes"], "env") || strings.Contains(bodies["POST /sandboxes"], "runtime") {
		t.Fatalf("create body: %s", bodies["POST /sandboxes"])
	}
}

func TestTeamHTTPSandboxUnavailable(t *testing.T) {
	ctx := context.Background()
	t.Setenv("KIONGA_SANDBOX_TOKEN", "sandbox-"+"test-value")
	t.Setenv("KIONGA_SANDBOX_URL", "")
	code := func(err error) string {
		var failure *sandboxError
		if errors.As(err, &failure) {
			return failure.Code
		}
		return ""
	}
	if _, err := defaultTeamSandbox.Create(ctx, "r", "w", "p"); code(err) != agentteam.ErrSandboxUnavailable {
		t.Fatalf("unconfigured: %v", err)
	}
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":"runtime_missing","message":"no runsc"}`)
	}))
	t.Setenv("KIONGA_SANDBOX_URL", unavailable.URL)
	if _, err := defaultTeamSandbox.Create(ctx, "r", "w", "p"); code(err) != agentteam.ErrSandboxUnavailable {
		t.Fatalf("503: %v", err)
	}
	unavailable.Close()
	if _, err := defaultTeamSandbox.Create(ctx, "r", "w", "p"); code(err) != agentteam.ErrSandboxUnavailable {
		t.Fatalf("unreachable: %v", err)
	}
}

// The real HTTP clients through the whole loop: only the far ends are in-process.
func TestTeamEndToEndOverHTTPClients(t *testing.T) {
	forEachTeamStore(t, func(t *testing.T, repository store.Repository) {
		h := newTeamHarness(t, repository)
		h.server.teamRunnerClient, h.server.teamSandboxClient = nil, nil
		execs := 0
		var mu sync.Mutex
		manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.URL.Path == "/healthz":
				_, _ = io.WriteString(w, `{"runtime":"runsc","runtime_verified":true,"driver":"docker","unsafe_runtime":false}`)
			case r.URL.Path == "/sandboxes":
				_, _ = io.WriteString(w, `{"id":"sbx-1","status":"running"}`)
			case strings.HasSuffix(r.URL.Path, "/exec"):
				execs++
				_, _ = io.WriteString(w, `{"exit_code":0,"stdout":"tests pass","stderr":"","truncated":false,"timed_out":false,"duration_ms":2}`)
			default:
				w.WriteHeader(204)
			}
		}))
		defer manager.Close()
		t.Setenv("KIONGA_SANDBOX_URL", manager.URL)
		t.Setenv("KIONGA_SANDBOX_TOKEN", "sandbox-"+"test-value")
		script := standardScript("bug-fixer")
		runnerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var request turnRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
			}
			validateContract(t, "turn-request", json.RawMessage(raw))
			var response turnResponse
			if !isCoordinator(request) && request.Turn == 1 {
				response = reply("", toolCall(agentteam.ToolExec, map[string]string{"command": "make test"}))
			} else {
				response, _ = script(r.Context(), request)
			}
			validateContract(t, "turn-response", response)
			_, _ = w.Write(mustRaw(t, response))
		}))
		defer runnerServer.Close()
		t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", runnerServer.URL)
		team := h.team([]string{h.definition("bug-fixer", sandboxMember(sandboxGrant))}, nil)
		runID := h.start(team, false)
		run := h.drive(runID)
		if run.Status != agentteam.RunSucceeded || execs != 1 {
			t.Fatalf("%+v execs=%d %s", run, execs, h.describeWorkers(runID))
		}
		view := decodeTeam[teamRunView](t, h.api("GET", "/api/v1/team-runs/"+runID, "", 200))
		if view.Sandbox == nil || !view.Sandbox.Available || view.Sandbox.Runtime != "runsc" {
			t.Fatalf("%+v", view.Sandbox)
		}
	})
}

func TestTeamTurnTimeoutOutlastsTheRunner(t *testing.T) {
	cases := map[string]time.Duration{"": 630 * time.Second, "600": 630 * time.Second, "900": 930 * time.Second, "junk": 630 * time.Second, "-5": 630 * time.Second}
	for value, want := range cases {
		t.Setenv("KIONGA_AGENT_TURN_TIMEOUT_SECONDS", value)
		if got := teamTurnTimeout(); got != want {
			t.Errorf("%q: %v", value, got)
		}
	}
}

// Runner rules: closed shape, tools never null, schemas never null, and only
// contract fields on messages.
func TestTeamTurnMessagesCarryContractFieldsOnly(t *testing.T) {
	started := time.Now()
	worker := agentteam.Worker{Transcript: []agentteam.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "", ToolCalls: []agentteam.ToolCall{{ID: "c1", Name: agentteam.ToolExec, Arguments: json.RawMessage(`{}`), Status: agentteam.CallDone, StartedAt: &started, ApprovalID: "apr-x"}}},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{Role: "assistant", Content: "bad args", ToolCalls: []agentteam.ToolCall{{ID: "c2", Name: "bad name!", Arguments: json.RawMessage(`[1]`), Status: agentteam.CallDone}}},
		{Role: "tool", ToolCallID: "c2", Content: "result"},
	}}
	request := turnRequest{RunID: "atr-" + strings.Repeat("a", 24), WorkerID: "atw-" + strings.Repeat("b", 24), Turn: 1, Model: "llm", System: "s", Messages: turnMessages(worker), Tools: agentteam.ToolsFor(agentteam.Snapshot{}, agentteam.RoleWorker, "ghost"), MaxOutputTokens: 8000}
	raw := string(mustRaw(t, request))
	for _, banned := range []string{"started_at", "approval_id", `"status"`, `"tool_calls":null`, `"input_schema":null`, `"tools":null`} {
		if strings.Contains(raw, banned) {
			t.Errorf("%s in %s", banned, raw)
		}
	}
	first := string(mustRaw(t, request.Messages[0]))
	if strings.Contains(first, "tool_calls") || strings.Contains(first, "tool_call_id") {
		t.Errorf("user message: %s", first)
	}
	validateContract(t, "turn-request", request)
}

// vm/ delivers secrets only as files. An empty token must never reach the manager.
func TestTeamSandboxTokenFromFileAndNeverEmpty(t *testing.T) {
	token := "sandbox-" + "file-value"
	seen := []string{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"runtime":"runsc","runtime_verified":true,"driver":"docker","unsafe_runtime":false}`)
	}))
	defer server.Close()
	t.Setenv("KIONGA_SANDBOX_URL", server.URL)
	t.Setenv("KIONGA_SANDBOX_TOKEN", "")

	var failure *sandboxError
	_, err := defaultTeamSandbox.Health(context.Background())
	if !errors.As(err, &failure) || failure.Code != agentteam.ErrSandboxUnavailable || len(seen) != 0 {
		t.Fatalf("empty token: %v, requests %v", err, seen)
	}
	var cache sandboxHealthCache
	if status := cache.status(context.Background(), defaultTeamSandbox); status.Available {
		t.Fatalf("available without a token: %+v", status)
	}

	file := filepath.Join(t.TempDir(), "sandbox-token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIONGA_SANDBOX_TOKEN_FILE", file)
	if err := runtimeconfig.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultTeamSandbox.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "Bearer "+token {
		t.Fatalf("authorization: %v", seen)
	}
}

func TestTeamHTTPRunnerEmbed(t *testing.T) {
	token := "runner-" + "test-value"
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_TOKEN", token)
	reply := `{"vector":[0.5,0.5]}`
	status := 200
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		validateContract(t, "embed-request", json.RawMessage(raw))
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", server.URL)
	validateContract(t, "embed-response", json.RawMessage(reply))
	vector, err := (httpTeamRunner{}).Embed(context.Background(), "query text", 2)
	if err != nil || len(vector) != 2 || body["text"] != "query text" || body["dimensions"] != float64(2) {
		t.Fatalf("%v %v %v", vector, err, body)
	}
	if _, err := (httpTeamRunner{}).Embed(context.Background(), "x", 3); err == nil {
		t.Fatal("wrong dimensions accepted")
	}
	status = 500
	if _, err := (httpTeamRunner{}).Embed(context.Background(), "x", 2); err == nil {
		t.Fatal("500 accepted")
	}
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", "")
	if _, err := (httpTeamRunner{}).Embed(context.Background(), "x", 2); err == nil {
		t.Fatal("unconfigured runner accepted")
	}
}

// The runner is strictly typed, so turn must be a JSON number.
func TestTeamTurnIsAJSONNumber(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(mustRaw(t, turnRequest{Turn: 3}), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["turn"]) != "3" {
		t.Fatalf("turn: %s", fields["turn"])
	}
	if string(fields["tools"]) == "" || string(fields["max_output_tokens"]) != "0" {
		t.Fatalf("%v", fields)
	}
}

// git_failed arrives as a 502 and must keep its code; only a 503 means unavailable.
func TestTeamHTTPSandboxKeepsGitFailedCode(t *testing.T) {
	t.Setenv("KIONGA_SANDBOX_TOKEN", "sandbox-"+"test-value")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"git_failed","message":"non-fast-forward"}`)
	}))
	defer server.Close()
	t.Setenv("KIONGA_SANDBOX_URL", server.URL)
	_, err := defaultTeamSandbox.Push(context.Background(), "sbx", "https://github.com/o/r", "kionga/r/x", "c")
	var failure *sandboxError
	if !errors.As(err, &failure) || failure.Code != sandboxGitFailed || failure.Message != "non-fast-forward" {
		t.Fatalf("%v", err)
	}
}
