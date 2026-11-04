package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// forEachTeamStore runs a test on the file store and, when TEST_DATABASE_URL is
// set (the integration lane), on PostgreSQL with a throwaway tenant.
func forEachTeamStore(t *testing.T, test func(t *testing.T, repository store.Repository)) {
	t.Helper()
	t.Run("file", func(t *testing.T) {
		test(t, store.New(filepath.Join(t.TempDir(), "state.json")))
	})
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		repository, err := store.OpenPostgres(context.Background(), url, "teams-"+randomToken()[:16])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(repository.Close)
		test(t, repository)
	})
}

// fakeClock is shared by engines so a test can move past a lease or a retry.
type fakeClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().UTC().Add(c.offset)
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// fakeRunner scripts the model and records every call. It fails the test if one
// worker ever has two calls in flight.
type fakeRunner struct {
	mu       sync.Mutex
	script   func(context.Context, turnRequest) (turnResponse, error)
	calls    []turnRequest
	inFlight map[string]int
	overlap  []string
	seen     map[string]int
	current  int
	peak     map[string]int
	embeds   []string
	embedErr error
	// embedSkew makes Embed return the wrong number of dimensions.
	embedSkew int
}

func (f *fakeRunner) Turn(ctx context.Context, request turnRequest) (turnResponse, error) {
	f.mu.Lock()
	if f.inFlight == nil {
		f.inFlight, f.seen, f.peak = map[string]int{}, map[string]int{}, map[string]int{}
	}
	f.calls = append(f.calls, request)
	f.inFlight[request.WorkerID]++
	if f.inFlight[request.WorkerID] > 1 {
		f.overlap = append(f.overlap, request.WorkerID)
	}
	f.seen[fmt.Sprintf("%s/%d", request.WorkerID, request.Turn)]++
	script := f.script
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight[request.WorkerID]--
		f.mu.Unlock()
	}()
	return script(ctx, request)
}

// Embed is deterministic, so a test can check dimensions and the search it drove.
func (f *fakeRunner) Embed(_ context.Context, text string, dimensions int) ([]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embeds = append(f.embeds, fmt.Sprintf("%s/%d", text, dimensions))
	if f.embedErr != nil {
		return nil, f.embedErr
	}
	vector := make([]float64, dimensions+f.embedSkew)
	if text != "?!" {
		vector[0] = 1
	}
	return vector, nil
}

func (f *fakeRunner) requests() []turnRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]turnRequest(nil), f.calls...)
}

func (f *fakeRunner) set(script func(context.Context, turnRequest) (turnResponse, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = script
}

func reply(content string, calls ...agentteam.ModelCall) turnResponse {
	if calls == nil {
		calls = []agentteam.ModelCall{}
	}
	return turnResponse{Content: content, ToolCalls: calls, Usage: agentteam.Usage{InputTokens: 10, OutputTokens: 5}, Model: "fake"}
}

func toolCall(name string, arguments any) agentteam.ModelCall {
	raw, _ := json.Marshal(arguments)
	return agentteam.ModelCall{ID: "model-chosen", Name: name, Arguments: raw}
}

func isCoordinator(request turnRequest) bool {
	return strings.Contains(request.System, "You are the coordinator.")
}

// lastMessage is what the model last received.
func lastMessage(request turnRequest) string {
	return request.Messages[len(request.Messages)-1].Content
}

// fakeSandbox is the sandbox manager in memory.
type fakeSandbox struct {
	mu          sync.Mutex
	unavailable bool
	files       map[string]string
	created     int
	execs       []sandboxExecRequest
	clones      []string
	pushes      []string
	credentials []string
	deleted     []string
	health      sandboxHealth
	healthErr   error
	healthCalls int
	execHook    func(context.Context) (sandboxExecResponse, error)
}

func (f *fakeSandbox) failure() error {
	if f.unavailable {
		return &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "no runtime"}
	}
	return nil
}

func (f *fakeSandbox) Health(context.Context) (sandboxHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthCalls++
	return f.health, f.healthErr
}

func (f *fakeSandbox) Create(_ context.Context, _, workerID, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(); err != nil {
		return "", err
	}
	f.created++
	return "sbx-" + workerID, nil
}

func (f *fakeSandbox) Exec(ctx context.Context, _ string, request sandboxExecRequest) (sandboxExecResponse, error) {
	f.mu.Lock()
	f.execs = append(f.execs, request)
	hook := f.execHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return sandboxExecResponse{Stdout: "ran: " + request.Command}, nil
}

func (f *fakeSandbox) Files(_ context.Context, _, path string) (sandboxFiles, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == "." || path == "/workspace" {
		return sandboxFiles{Entries: []sandboxEntry{{Name: "a.txt", Type: "file"}}}, nil
	}
	content, ok := f.files[path]
	if !ok {
		return sandboxFiles{}, &sandboxError{Code: "not_found", Message: "no such file"}
	}
	return sandboxFiles{Content: &content}, nil
}

func (f *fakeSandbox) Write(_ context.Context, _, path, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = content
	return nil
}

func (f *fakeSandbox) Clone(_ context.Context, _, repository, _, credential string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clones = append(f.clones, repository)
	f.credentials = append(f.credentials, credential)
	return strings.Repeat("a", 40), nil
}

func (f *fakeSandbox) Push(_ context.Context, _, repository, branch, credential string) (sandboxPushResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, repository+"#"+branch)
	f.credentials = append(f.credentials, credential)
	return sandboxPushResult{Branch: branch, Commit: strings.Repeat("b", 40)}, nil
}

func (f *fakeSandbox) Diff(context.Context, string) (sandboxDiff, error) {
	return sandboxDiff{Diff: "diff --git a b"}, nil
}

func (f *fakeSandbox) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

// teamHarness holds a store, an API and an engine that a test drives by hand, so
// no test depends on timing.
type teamHarness struct {
	t          *testing.T
	repository store.Repository
	server     *Server
	mux        *http.ServeMux
	runner     *fakeRunner
	sandbox    *fakeSandbox
	clock      *fakeClock
	engine     *teamEngine
	project    string
	starter    auth.Principal
}

var teamStarter = auth.Principal{Subject: "alice", Roles: []string{auth.RoleEngineer}}

func newTeamHarness(t *testing.T, repository store.Repository) *teamHarness {
	t.Helper()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Teams " + randomToken()[:8]}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h := &teamHarness{t: t, repository: repository, project: project.ID, runner: &fakeRunner{}, sandbox: &fakeSandbox{files: map[string]string{}}, clock: &fakeClock{}, starter: teamStarter}
	h.server = &Server{store: repository, teamRunnerClient: h.runner, teamSandboxClient: h.sandbox}
	h.mux = http.NewServeMux()
	for _, register := range routeRegistrars {
		register(h.server, h.mux)
	}
	h.engine = h.newEngine("gateway-a")
	return h
}

func (h *teamHarness) newEngine(owner string) *teamEngine {
	engine := newTeamEngine(h.server, owner)
	engine.now = h.clock.now
	engine.jitter = func() float64 { return 0 }
	engine.watchEvery = 5 * time.Millisecond
	return engine
}

// callAs sends an API request as a principal.
func (h *teamHarness) callAs(as auth.Principal, method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return call(h.t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), as)))
	}), method, path, body)
}

func (h *teamHarness) api(method, path, body string, want int) []byte {
	h.t.Helper()
	response := h.callAs(h.starter, method, path, body)
	if response.Code != want {
		h.t.Fatalf("%s %s: got %d want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response.Body.Bytes()
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func decodeTeam[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return value
}

// definition creates an agent definition and returns its ID.
func (h *teamHarness) definition(name string, change func(map[string]any)) string {
	h.t.Helper()
	body := map[string]any{"project_id": h.project, "name": name, "description": "Use for " + name, "instructions": "Do " + name}
	if change != nil {
		change(body)
	}
	return decodeTeam[agentDefinitionDoc](h.t, h.api("POST", "/api/v1/agent-definitions", mustJSON(h.t, body), 201)).ID
}

func (h *teamHarness) team(members []string, change func(map[string]any)) string {
	h.t.Helper()
	body := map[string]any{"project_id": h.project, "name": "team-" + randomToken()[:6], "coordinator": map[string]any{"instructions": "Lead the team"}, "members": members}
	if change != nil {
		change(body)
	}
	return decodeTeam[agentTeamDoc](h.t, h.api("POST", "/api/v1/agent-teams", mustJSON(h.t, body), 201)).ID
}

func (h *teamHarness) start(teamID string, allowSideEffects bool) string {
	h.t.Helper()
	body := map[string]any{"team_id": teamID, "input": "Do the work", "allow_side_effects": allowSideEffects}
	return decodeTeam[teamRunView](h.t, h.api("POST", "/api/v1/team-runs", mustJSON(h.t, body), 202)).ID
}

// step scans once on the engine and waits for what it launched.
func (h *teamHarness) step(engine *teamEngine) {
	engine.scan(context.Background())
	engine.wg.Wait()
}

// drive scans until the run settles.
func (h *teamHarness) drive(runID string) agentteam.Run {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		h.step(h.engine)
		if run := h.run(runID); run.SettledAt != nil {
			return run
		}
	}
	h.t.Fatalf("run did not settle: %+v workers: %s", h.run(runID), h.describeWorkers(runID))
	return agentteam.Run{}
}

func (h *teamHarness) run(id string) agentteam.Run {
	h.t.Helper()
	run, err := store.GetDoc[agentteam.Run](h.repository, teamRunKind, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return run
}

func (h *teamHarness) worker(id string) agentteam.Worker {
	h.t.Helper()
	worker, err := store.GetDoc[agentteam.Worker](h.repository, teamWorkerKind, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return worker
}

func (h *teamHarness) workers(runID string) []agentteam.Worker {
	return teamWorkers(h.repository, h.run(runID))
}

func (h *teamHarness) workerNamed(runID, name string) agentteam.Worker {
	h.t.Helper()
	for _, worker := range h.workers(runID) {
		if worker.Agent == name {
			return worker
		}
	}
	h.t.Fatalf("no worker %s: %s", name, h.describeWorkers(runID))
	return agentteam.Worker{}
}

func (h *teamHarness) describeWorkers(runID string) string {
	parts := []string{}
	for _, worker := range h.workers(runID) {
		parts = append(parts, fmt.Sprintf("%s=%s(%s turns=%d)", worker.Agent, worker.Status, worker.LastError, worker.Turns))
	}
	return strings.Join(parts, " ")
}

func (h *teamHarness) saveWorker(worker agentteam.Worker) {
	h.t.Helper()
	if _, err := store.UpdateDoc(h.repository, teamWorkerKind, worker.ID, func(agentteam.Worker, bool) (agentteam.Worker, error) { return worker, nil }, "", ""); err != nil {
		h.t.Fatal(err)
	}
}

func toolResults(worker agentteam.Worker) map[string]string {
	results := map[string]string{}
	for _, message := range worker.Transcript {
		if message.Role == "tool" {
			results[message.ToolCallID] = message.Content
		}
	}
	return results
}

// allEvents reads a run's whole log through the store.
func (h *teamHarness) allEvents(runID string) []teamEvent {
	events := []teamEvent{}
	after := 0
	for {
		page := teamEventsAfter(h.repository, runID, after)
		if len(page) == 0 {
			return events
		}
		events = append(events, page...)
		after = page[len(page)-1].Seq
	}
}

func eventTypes(events []teamEvent) string {
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	return strings.Join(types, " ")
}

// standardScript: the coordinator spawns the named agents once, waits, then
// finishes. A worker reports and finishes.
func standardScript(agents ...string) func(context.Context, turnRequest) (turnResponse, error) {
	return func(_ context.Context, request turnRequest) (turnResponse, error) {
		if !isCoordinator(request) {
			return reply("", toolCall(agentteam.ToolFinish, map[string]string{"report": "work done"})), nil
		}
		if request.Turn == 1 {
			calls := []agentteam.ModelCall{}
			for _, agent := range agents {
				calls = append(calls, toolCall(agentteam.ToolSpawnWorker, map[string]string{"agent": agent, "task": "Task for " + agent}))
			}
			return reply("starting", calls...), nil
		}
		if strings.Contains(lastMessage(request), "hand-back") {
			handbacks := 0
			for _, message := range request.Messages {
				if strings.Contains(message.Content, `kind="hand-back"`) {
					handbacks++
				}
			}
			if handbacks >= len(agents) {
				return reply("", toolCall(agentteam.ToolFinish, map[string]string{"report": "team report"})), nil
			}
		}
		return reply("waiting"), nil
	}
}

// --- contract schemas ---

var (
	teamSchemaOnce sync.Once
	teamSchemas    map[string]*jsonschema.Schema
	teamSchemaErr  error
)

func contractSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	teamSchemaOnce.Do(func() {
		teamSchemas = map[string]*jsonschema.Schema{}
		compiler := jsonschema.NewCompiler()
		base := "../../../contracts/agent-team/"
		ids := map[string]string{
			"event": "team-run-event.v1.schema.json", "turn-request": "runner-turn-request.v1.schema.json", "turn-response": "runner-turn-response.v1.schema.json",
			"embed-request": "runner-embedding-request.v1.schema.json", "embed-response": "runner-embedding-response.v1.schema.json",
			"definition": "agent-definition.v1.schema.json", "team": "team.v1.schema.json", "sandbox-api": "sandbox-api.v1.schema.json",
		}
		for key, file := range ids {
			raw, err := os.Open(base + file)
			if err != nil {
				teamSchemaErr = err
				return
			}
			document, err := jsonschema.UnmarshalJSON(raw)
			_ = raw.Close()
			if err != nil {
				teamSchemaErr = err
				return
			}
			if err := compiler.AddResource(file, document); err != nil {
				teamSchemaErr = err
				return
			}
			teamSchemas[key] = nil
			_ = key
		}
		for key, file := range ids {
			schema, err := compiler.Compile(file)
			if err != nil {
				teamSchemaErr = err
				return
			}
			teamSchemas[key] = schema
		}
		schema, err := compiler.Compile("sandbox-api.v1.schema.json#/$defs/run_view_sandbox")
		if err != nil {
			teamSchemaErr = err
			return
		}
		teamSchemas["run-view-sandbox"] = schema
	})
	if teamSchemaErr != nil {
		t.Fatal(teamSchemaErr)
	}
	schema := teamSchemas[name]
	if schema == nil {
		t.Fatalf("no schema %s", name)
	}
	return schema
}

func validateContract(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := contractSchema(t, name).Validate(document); err != nil {
		t.Errorf("%s violates the contract: %v\n%s", name, err, raw)
	}
}
