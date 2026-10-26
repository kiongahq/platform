package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/agentgraph"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

const runnableDesign = `{"project_id":"prj-demo","name":"Support agent","revision":0,"nodes":[
{"id":"input","kind":"input","name":"Input","x":10,"y":20},
{"id":"prompt","kind":"prompt","name":"Prompt","x":200,"y":20,"config":{"template":"Answer: {input}"}},
{"id":"model","kind":"model","name":"Model","x":400,"y":20},
{"id":"output","kind":"output","name":"Output","x":600,"y":20}],
"edges":[{"from":"input","to":"prompt"},{"from":"prompt","to":"model"},{"from":"model","to":"output"}]}`

// studio is a gateway wired to a fake designer runner. The fake runner
// streams NDJSON events and calls back through the internal listener with
// the run credential, like agent-runtime's designer service.
type studio struct {
	t         *testing.T
	api       http.Handler
	repo      *store.Store
	internal  *httptest.Server
	runner    *httptest.Server
	mu        sync.Mutex
	callbacks []int
	payloads  []runnerPayload
}

func newStudio(t *testing.T) *studio {
	t.Helper()
	server, repo := accessServer(t)
	if _, err := repo.UpsertUserAccess("user-1", api.UpsertUserAccessRequest{Email: "user@example.com", Role: "user", Services: []string{"agents", "langflow"}, ProjectIDs: []string{"prj-demo"}, Storage: api.StorageGrant{SizeGB: 10}, Compute: api.ComputeGrant{VCPUs: 4, MemoryGB: 8, MaxVMs: 2, MaxProjects: 1, MaxRuns: 1}}, "admin"); err != nil {
		t.Fatal(err)
	}
	s := &studio{t: t, api: server, repo: repo}
	reject := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "workspace credential required", 401) })
	s.internal = httptest.NewServer(DesignerRunIdentity(reject, server, repo))
	s.runner = httptest.NewServer(http.HandlerFunc(s.fakeRunner))
	t.Cleanup(s.internal.Close)
	t.Cleanup(s.runner.Close)
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", s.runner.URL)
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_TOKEN", "runner-secret-token")
	t.Setenv("KIONGA_AGENT_DESIGNER_CALLBACK_URL", s.internal.URL+"/api/v1")
	return s
}

func (s *studio) fakeRunner(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer runner-secret-token" {
		http.Error(w, "unauthorized", 401)
		return
	}
	var payload runnerPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.mu.Lock()
	s.payloads = append(s.payloads, payload)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher := w.(http.Flusher)
	seq := 0
	emit := func(event map[string]any) {
		event["seq"], event["run_id"], event["ts"] = seq, payload.RunID, time.Now().UTC().Format(time.RFC3339Nano)
		seq++
		raw, _ := json.Marshal(event)
		_, _ = w.Write(append(raw, '\n'))
		flusher.Flush()
	}
	emit(map[string]any{"type": "run.started"})
	switch payload.Input {
	case "block":
		<-r.Context().Done()
		return
	case "fail":
		emit(map[string]any{"type": "run.failed", "error": map[string]string{"code": "node_error", "message": "model exploded"}})
		return
	case "silent":
		return
	case "stream":
		time.Sleep(300 * time.Millisecond) // lets the test's event stream subscribe
		for _, word := range []string{"Hel", "lo ", "there"} {
			emit(map[string]any{"type": "node.delta", "node_id": "model", "kind": "model", "delta": word})
		}
		emit(map[string]any{"type": "run.completed", "output": "Hello there", "duration_ms": 5, "usage": map[string]any{"input_tokens": 1, "output_tokens": 3, "cost_usd": 0}})
		return
	}
	request, _ := http.NewRequest(http.MethodGet, payload.Gateway.URL+"/knowledge-bases", nil)
	request.Header.Set("X-Kionga-Run-ID", payload.RunID)
	request.Header.Set("X-Kionga-Run-Token", payload.Gateway.RunToken)
	status := 0
	if response, err := http.DefaultClient.Do(request); err == nil {
		status = response.StatusCode
		response.Body.Close()
	}
	s.mu.Lock()
	s.callbacks = append(s.callbacks, status)
	s.mu.Unlock()
	emit(map[string]any{"type": "node.completed", "node_id": "model", "kind": "model", "duration_ms": 5, "output_preview": "echo"})
	emit(map[string]any{"type": "run.completed", "output": "echo: " + payload.Input, "duration_ms": 7,
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "cost_usd": 0.0001}})
}

func (s *studio) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	request := designRequest(method, path, body, auth.RoleUser)
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	recorder := httptest.NewRecorder()
	s.api.ServeHTTP(recorder, request)
	return recorder
}

func (s *studio) create(body string) agentDesign {
	s.t.Helper()
	response := s.do("POST", "/api/v1/agent-designs", body)
	if response.Code != 201 {
		s.t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	return decodeInto[agentDesign](s.t, response)
}

func (s *studio) waitRun(designID, runID string) designRun {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := s.do("GET", "/api/v1/agent-designs/"+designID+"/runs/"+runID, "")
		run := decodeInto[designRun](s.t, response)
		if run.terminal() {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("run %s did not finish", runID)
	return designRun{}
}

func TestDesignerCatalogEndpoints(t *testing.T) {
	s := newStudio(t)
	components := s.do("GET", "/api/v1/agent-designs/components", "")
	registry := decodeInto[agentgraph.Registry](t, components)
	if components.Code != 200 || registry.Version == "" || len(registry.Components) < 10 {
		t.Fatalf("components: %d %s", components.Code, components.Body.String())
	}
	templates := s.do("GET", "/api/v1/agent-designs/templates", "")
	if templates.Code != 200 || !strings.Contains(templates.Body.String(), "rag-assistant") {
		t.Fatalf("templates: %d", templates.Code)
	}
	if auth.Allowed(auth.Principal{Roles: []string{auth.RoleService}}, http.MethodGet, "/api/v1/agent-versions/agv-1") {
		t.Fatal("machine identities must not read native agent versions")
	}
}

// Version 1 drafts are served upgraded while storage keeps exactly what was saved.
func TestVersionOneDraftIsUpgradedOnReadOnly(t *testing.T) {
	s := newStudio(t)
	legacy := `{"id":"agd-000000000000000000000001","project_id":"prj-demo","owner_subject":"user-1","name":"Old","revision":2,
"nodes":[{"id":"input","kind":"input","name":"Input","x":1,"y":1},{"id":"cond","kind":"condition","name":"C","x":2,"y":2,"config":{"expression":"route urgent"}},{"id":"output","kind":"output","name":"Output","x":3,"y":3}],
"edges":[{"from":"input","to":"cond"},{"from":"cond","to":"output"}],
"history":[{"revision":1,"name":"Old","nodes":[{"id":"input","kind":"input","name":"Input","x":1,"y":1},{"id":"output","kind":"output","name":"Output","x":3,"y":3}],"edges":[{"from":"input","to":"output"}],"saved_at":"2026-10-01T00:00:00Z"}],
"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"}`
	if _, err := s.repo.UpdateDocument(agentDesignKind, "agd-000000000000000000000001", func(json.RawMessage) (any, error) { return json.RawMessage(legacy), nil }, "", "test"); err != nil {
		t.Fatal(err)
	}
	response := s.do("GET", "/api/v1/agent-designs/agd-000000000000000000000001", "")
	design := decodeInto[agentDesign](t, response)
	if design.SchemaVersion != 2 || design.Edges[1].FromPort != "true" || design.Settings == nil || design.History[0].Edges[0].ToPort != "in" {
		t.Fatalf("not upgraded on read: %+v", design)
	}
	raw, _ := s.repo.GetDocument(agentDesignKind, "agd-000000000000000000000001")
	if strings.Contains(string(raw), "schema_version") || strings.Contains(string(raw), "from_port") {
		t.Fatal("reading a design rewrote stored revisions")
	}
	validation := s.do("POST", "/api/v1/agent-designs/agd-000000000000000000000001/validate", "")
	if !strings.Contains(validation.Body.String(), `"runnable":false`) || !strings.Contains(validation.Body.String(), "invalid_condition") {
		t.Fatalf("free-text condition must block running: %s", validation.Body.String())
	}
	// Saving the draft keeps the earlier revision in history and stores version 2.
	update := strings.Replace(strings.Replace(runnableDesign, `"revision":0`, `"revision":2`, 1), `"Support agent"`, `"Old"`, 1)
	saved := s.do("PUT", "/api/v1/agent-designs/agd-000000000000000000000001", update)
	if saved.Code != 200 {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	raw, _ = s.repo.GetDocument(agentDesignKind, "agd-000000000000000000000001")
	var stored agentDesign
	_ = json.Unmarshal(raw, &stored)
	if stored.SchemaVersion != 2 || len(stored.History) != 2 || stored.History[1].Nodes[1].Config["expression"] != "route urgent" {
		t.Fatalf("history lost or not upgraded: %+v", stored)
	}
}

func TestValidateRejectsResourcesOutsideTheProject(t *testing.T) {
	s := newStudio(t)
	body := `{"project_id":"prj-demo","nodes":[{"id":"input","kind":"input","name":"In"},{"id":"kb","kind":"knowledge","name":"KB","config":{"knowledge_base":"kb-foreign"}},{"id":"output","kind":"output","name":"Out"}],"edges":[{"from":"input","to":"kb"},{"from":"kb","to":"output"}]}`
	response := s.do("POST", "/api/v1/agent-designs/validate", body)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "resource_not_authorized") {
		t.Fatalf("foreign knowledge base accepted: %d %s", response.Code, response.Body.String())
	}
	foreign := s.do("POST", "/api/v1/agent-designs/validate", strings.Replace(body, "prj-demo", "prj-other", 1))
	if foreign.Code != 403 {
		t.Fatalf("validating in an unassigned project: %d", foreign.Code)
	}
}

func TestPlaygroundRunStreamsEventsAndConfinesTheRunCredential(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	start := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"hello","allow_side_effects":false}`, "Idempotency-Key", "k-1")
	if start.Code != 202 {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	created := decodeInto[designRun](t, start)
	if strings.Contains(start.Body.String(), "token_hash") {
		t.Fatal("run credential hash leaked to the client")
	}
	run := s.waitRun(design.ID, created.ID)
	if run.Status != "succeeded" || run.Output != "echo: hello" || run.Usage.InputTokens != 3 || len(run.Events) != 3 {
		t.Fatalf("run: %+v", run)
	}
	s.mu.Lock()
	callbacks, payload := append([]int(nil), s.callbacks...), s.payloads[0]
	s.mu.Unlock()
	if len(callbacks) != 1 || callbacks[0] != 200 {
		t.Fatalf("resource callback through the run credential: %v", callbacks)
	}
	if payload.Graph.SchemaVersion != 2 || payload.Graph.Nodes[2].Config["model"] != "llm" || payload.Gateway.RunToken == "" {
		t.Fatalf("runner payload: %+v", payload)
	}
	replay := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"hello"}`, "Idempotency-Key", "k-1")
	if replay.Code != 200 || decodeInto[designRun](t, replay).ID != created.ID {
		t.Fatalf("idempotent replay: %d", replay.Code)
	}
	// The credential is dead once the run has finished.
	request, _ := http.NewRequest("GET", s.internal.URL+"/api/v1/knowledge-bases", nil)
	request.Header.Set("X-Kionga-Run-ID", created.ID)
	request.Header.Set("X-Kionga-Run-Token", payload.Gateway.RunToken)
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != 403 {
		t.Fatalf("finished run credential still works: %v %v", err, response)
	}
	polled := s.do("GET", "/api/v1/agent-designs/"+design.ID+"/runs/"+created.ID+"/events?after=0", "")
	page := decodeInto[struct {
		Events []map[string]any `json:"events"`
		Done   bool             `json:"done"`
		Next   int              `json:"next"`
	}](t, polled)
	if !page.Done || len(page.Events) != 2 || page.Next != 2 {
		t.Fatalf("polling page: %s", polled.Body.String())
	}
	stream := s.do("GET", "/api/v1/agent-designs/"+design.ID+"/runs/"+created.ID+"/events", "", "Accept", "text/event-stream", "Last-Event-ID", "0")
	if !strings.Contains(stream.Body.String(), "id: 1\nevent: run\n") || strings.Contains(stream.Body.String(), "id: 0\n") || !strings.Contains(stream.Body.String(), "event: end") {
		t.Fatalf("SSE resume: %s", stream.Body.String())
	}
	list := s.do("GET", "/api/v1/agent-designs/"+design.ID+"/runs", "")
	if !strings.Contains(list.Body.String(), created.ID) {
		t.Fatal("run missing from history")
	}
}

func TestRunCredentialRejectsForgeryAndOtherRoutes(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	start := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"block"}`)
	run := decodeInto[designRun](t, start)
	deadline := time.Now().Add(3 * time.Second)
	var token string
	for token == "" && time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.payloads) > 0 {
			token = s.payloads[0].Gateway.RunToken
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	call := func(method, path, id, secret string) int {
		request, _ := http.NewRequest(method, s.internal.URL+path, nil)
		request.Header.Set("X-Kionga-Run-ID", id)
		request.Header.Set("X-Kionga-Run-Token", secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if code := call("GET", "/api/v1/knowledge-bases", run.ID, token); code != 200 {
		t.Fatalf("active run credential: %d", code)
	}
	if code := call("GET", "/api/v1/knowledge-bases", run.ID, strings.Repeat("0", 64)); code != 401 {
		t.Fatalf("forged token: %d", code)
	}
	if code := call("GET", "/api/v1/projects", run.ID, token); code != 403 {
		t.Fatalf("run credential reached a non-resource API: %d", code)
	}
	if code := call("POST", "/api/v1/agent-designs", run.ID, token); code != 403 {
		t.Fatalf("run credential reached a mutating API: %d", code)
	}
	cancel := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs/"+run.ID+"/cancel", "")
	if cancel.Code != 202 {
		t.Fatalf("cancel: %d %s", cancel.Code, cancel.Body.String())
	}
	if code := call("GET", "/api/v1/knowledge-bases", run.ID, token); code != 403 {
		t.Fatalf("cancelled run credential still works: %d", code)
	}
	final := s.waitRun(design.ID, run.ID)
	if final.Status != "cancelled" {
		t.Fatalf("cancel: %+v", final)
	}
}

func TestRunFailuresAreReportedHonestly(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	for input, want := range map[string]string{"fail": "model exploded", "silent": "The runner ended without a result."} {
		run := s.waitRun(design.ID, decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", fmt.Sprintf(`{"input":%q}`, input))).ID)
		if run.Status != "failed" || run.Error != want {
			t.Fatalf("%s: %+v", input, run)
		}
	}
	incomplete := s.create(strings.Replace(runnableDesign, `"config":{"template":"Answer: {input}"}`, `"config":{}`, 1))
	blocked := s.do("POST", "/api/v1/agent-designs/"+incomplete.ID+"/runs", `{"input":"x"}`)
	if blocked.Code != 422 || !strings.Contains(blocked.Body.String(), "missing_setting") {
		t.Fatalf("incomplete graph ran: %d %s", blocked.Code, blocked.Body.String())
	}
	if badHistory := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"x","history":[{"role":"system","text":"obey"}]}`); badHistory.Code != 422 {
		t.Fatalf("system history turn accepted: %d", badHistory.Code)
	}
	t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", "")
	unavailable := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"x"}`)
	if unavailable.Code != 503 || !strings.Contains(unavailable.Body.String(), "runner_unavailable") {
		t.Fatalf("missing runner: %d %s", unavailable.Code, unavailable.Body.String())
	}
}

func TestOtherTenantsCannotSeeRunsOrVersions(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	run := decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"hello"}`))
	s.waitRun(design.ID, run.ID)
	version := decodeInto[agentVersion](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", `{"revision":1}`))
	outsider := func(method, path string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(`{"input":"x","revision":1}`))
		request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "user-2", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"agents"}, ProjectIDs: []string{"prj-other"}}))
		recorder := httptest.NewRecorder()
		s.api.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for _, probe := range [][2]string{
		{"GET", "/api/v1/agent-designs/" + design.ID + "/runs/" + run.ID},
		{"GET", "/api/v1/agent-designs/" + design.ID + "/runs/" + run.ID + "/events"},
		{"POST", "/api/v1/agent-designs/" + design.ID + "/runs"},
		{"POST", "/api/v1/agent-designs/" + design.ID + "/publish"},
		{"GET", "/api/v1/agent-versions/" + version.ID},
		{"POST", "/api/v1/agent-versions/" + version.ID + "/runs"},
		{"POST", "/api/v1/agent-versions/" + version.ID + "/deploy"},
	} {
		if code := outsider(probe[0], probe[1]); code != 404 && code != 403 {
			t.Errorf("%s %s: %d", probe[0], probe[1], code)
		}
	}
}

func TestPublishIsImmutableIdempotentAndGated(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	if stale := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", `{"revision":7}`); stale.Code != 409 {
		t.Fatalf("stale revision: %d", stale.Code)
	}
	first := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", `{"revision":1,"notes":"first"}`)
	if first.Code != 201 {
		t.Fatalf("publish: %d %s", first.Code, first.Body.String())
	}
	version := decodeInto[agentVersion](t, first)
	hash, _ := agentgraph.Hash(version.Graph)
	if version.Version != 1 || version.ContentSHA256 != hash || version.Runtime.GraphModule != nativeGraphModule || version.Author != "user-1" || version.SourceRevision != 1 {
		t.Fatalf("artifact: %+v", version)
	}
	// Moving a node changes the draft but not behavior: the same version returns.
	moved := strings.Replace(strings.Replace(runnableDesign, `"revision":0`, `"revision":1`, 1), `"x":200`, `"x":250`, 1)
	if saved := s.do("PUT", "/api/v1/agent-designs/"+design.ID, moved); saved.Code != 200 {
		t.Fatalf("save: %d", saved.Code)
	}
	again := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", `{"revision":2}`)
	if again.Code != 200 || decodeInto[agentVersion](t, again).ID != version.ID {
		t.Fatalf("identical behavior should return version 1: %d %s", again.Code, again.Body.String())
	}
	changed := strings.Replace(strings.Replace(runnableDesign, `"revision":0`, `"revision":2`, 1), "Answer: {input}", "Reply briefly: {input}", 1)
	if saved := s.do("PUT", "/api/v1/agent-designs/"+design.ID, changed); saved.Code != 200 {
		t.Fatalf("save: %d", saved.Code)
	}
	second := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", `{"revision":3}`)
	if second.Code != 201 || decodeInto[agentVersion](t, second).Version != 2 {
		t.Fatalf("second version: %d %s", second.Code, second.Body.String())
	}
	stored := decodeInto[agentVersion](t, s.do("GET", "/api/v1/agent-versions/"+version.ID, ""))
	if stored.Graph.Nodes[1].Config["template"] != "Answer: {input}" {
		t.Fatal("publishing a later revision changed an earlier version")
	}
	versions := s.do("GET", "/api/v1/agent-designs/"+design.ID+"/versions", "")
	if !strings.Contains(versions.Body.String(), `"version":2`) || !strings.Contains(versions.Body.String(), `"deployments":[]`) {
		t.Fatalf("versions: %s", versions.Body.String())
	}
	// A version runs without the draft.
	run := decodeInto[designRun](t, s.do("POST", "/api/v1/agent-versions/"+version.ID+"/runs", `{"input":"from version"}`))
	if done := s.waitRun(design.ID, run.ID); done.Status != "succeeded" || done.VersionID != version.ID || done.ContentSHA256 != version.ContentSHA256 {
		t.Fatalf("version run: %+v", done)
	}
}

func TestEvaluationScoresCasesAndGatesPublishing(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	fixtures := `{"revision":0,"cases":[{"id":"greets","input":"hello","expected":{"contains":["echo","hello"]}},{"id":"strict","input":"bye","expected":{"equals":"something else"}},{"id":"fails","input":"fail","expected":{}}]}`
	if saved := s.do("PUT", "/api/v1/agent-designs/"+design.ID+"/fixtures", fixtures); saved.Code != 200 {
		t.Fatalf("fixtures: %d %s", saved.Code, saved.Body.String())
	}
	if conflict := s.do("PUT", "/api/v1/agent-designs/"+design.ID+"/fixtures", fixtures); conflict.Code != 409 {
		t.Fatalf("stale fixture save: %d", conflict.Code)
	}
	if bad := s.do("PUT", "/api/v1/agent-designs/"+design.ID+"/fixtures", `{"revision":1,"cases":[{"id":"x","input":"y","expected":{"matches":"("}}]}`); bad.Code != 422 {
		t.Fatalf("invalid pattern accepted: %d", bad.Code)
	}
	started := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/evaluations", `{"gate":0.3}`)
	if started.Code != 202 {
		t.Fatalf("evaluate: %d %s", started.Code, started.Body.String())
	}
	id := decodeInto[agentEvaluation](t, started).ID
	var evaluation agentEvaluation
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evaluation = decodeInto[agentEvaluation](t, s.do("GET", "/api/v1/agent-evaluations/"+id, ""))
		if evaluation.Status == "completed" {
			break
		}
	}
	if evaluation.Status != "completed" || evaluation.Completed != 3 || !evaluation.Cases[0].Passed || evaluation.Cases[1].Passed || evaluation.Cases[2].Passed {
		t.Fatalf("scoring: %+v", evaluation)
	}
	if evaluation.FailureReasons["not_equal"] != 1 || evaluation.FailureReasons["run_failed"] != 1 || !evaluation.Passed || evaluation.PassRate < 0.33 || evaluation.PassRate > 0.34 {
		t.Fatalf("aggregate: %+v", evaluation)
	}
	published := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", fmt.Sprintf(`{"revision":1,"evaluation_id":%q}`, id))
	if published.Code != 201 || decodeInto[agentVersion](t, published).Evaluation == nil {
		t.Fatalf("publish with evaluation: %d %s", published.Code, published.Body.String())
	}
	// An evaluation of different behavior cannot vouch for this graph.
	changed := strings.Replace(strings.Replace(runnableDesign, `"revision":0`, `"revision":1`, 1), "Answer: {input}", "Other: {input}", 1)
	s.do("PUT", "/api/v1/agent-designs/"+design.ID, changed)
	mismatch := s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", fmt.Sprintf(`{"revision":2,"evaluation_id":%q}`, id))
	if mismatch.Code != 422 || !strings.Contains(mismatch.Body.String(), "quality_gate_failed") {
		t.Fatalf("mismatched evaluation accepted: %d %s", mismatch.Code, mismatch.Body.String())
	}
	list := s.do("GET", "/api/v1/agent-designs/"+design.ID+"/evaluations", "")
	if !strings.Contains(list.Body.String(), id) {
		t.Fatal("evaluation missing from list")
	}
}

func TestDeployVersionUsesManagedRuntimeAndQualityGate(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	version := decodeInto[agentVersion](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/publish", `{"revision":1}`))
	compose := s.do("POST", "/api/v1/agent-versions/"+version.ID+"/deploy", `{}`)
	if compose.Code != 503 || !strings.Contains(compose.Body.String(), "managed_runtime_unavailable") {
		t.Fatalf("compose deploy: %d %s", compose.Code, compose.Body.String())
	}
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "kionga-workspaces")
	t.Setenv("KIONGA_NATIVE_AGENT_IMAGE", "ghcr.io/kiongahq/agent-runtime:test")
	t.Setenv("AGENT_RUNTIME_URL", "")
	gated := s.do("POST", "/api/v1/agent-versions/"+version.ID+"/deploy", `{"require_evaluation":true}`)
	if gated.Code != 422 {
		t.Fatalf("deploy without evaluation passed the gate: %d", gated.Code)
	}
	deployed := s.do("POST", "/api/v1/agent-versions/"+version.ID+"/deploy", `{}`)
	if deployed.Code != 202 {
		t.Fatalf("deploy: %d %s", deployed.Code, deployed.Body.String())
	}
	if strings.Contains(deployed.Body.String(), "flow_json") {
		t.Fatal("deploy response leaked the mounted artifact")
	}
	var agent api.Agent
	for _, item := range s.repo.Agents() {
		if item.SnapshotID == version.ID {
			agent = item
		}
	}
	var artifact agentVersion
	if err := json.Unmarshal([]byte(agent.FlowJSON), &artifact); err != nil || agent.GraphModule != nativeGraphModule || artifact.ContentSHA256 != version.ContentSHA256 || agent.Version != "v1" {
		t.Fatalf("deployed agent: %+v (%v)", agent, err)
	}
	overview := s.do("GET", "/api/v1/agent-designs/overview?project_id=prj-demo", "")
	if !strings.Contains(overview.Body.String(), `"deployments":1`) || !strings.Contains(overview.Body.String(), `"versions":1`) {
		t.Fatalf("overview: %s", overview.Body.String())
	}
}

func TestScoreCaseAndPercentile(t *testing.T) {
	ok := designRun{Status: "succeeded", Output: "Your refund for ORDER-12 is on its way", DurationMS: 900}
	item := fixtureCase{Expected: fixtureExpected{Contains: []string{"refund"}, NotContains: []string{"sorry"}, Matches: `ORDER-\d+`, MaxLatencyMS: 1000}}
	if failures := scoreCase(item, ok); len(failures) != 0 {
		t.Fatalf("expected pass: %v", failures)
	}
	item.Expected.MaxLatencyMS = 500
	item.Expected.Contains = []string{"credit"}
	failures := scoreCase(item, ok)
	if len(failures) != 2 || !strings.HasPrefix(failures[0], "missing_text") || !strings.HasPrefix(failures[1], "too_slow") {
		t.Fatalf("failures: %v", failures)
	}
	if got := percentile([]int64{5, 1, 4, 2, 3}, 0.95); got != 5 {
		t.Fatalf("p95 of 1..5: %d", got)
	}
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatal("empty percentile")
	}
}

func TestSSEFramesAreParseable(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	run := decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"hello"}`))
	s.waitRun(design.ID, run.ID)
	stream := s.do("GET", "/api/v1/agent-designs/"+design.ID+"/runs/"+run.ID+"/events", "", "Accept", "text/event-stream")
	scanner := bufio.NewScanner(strings.NewReader(stream.Body.String()))
	events := 0
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: {\"") && !strings.Contains(line, `"status"`) {
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("bad frame %q: %v", line, err)
			}
			events++
		}
	}
	if events != 3 {
		t.Fatalf("expected 3 event frames, got %d: %s", events, stream.Body.String())
	}
}

func TestRunnerRefusalsAreActionable(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	for status, want := range map[int]string{429: "at capacity", 422: "rejected this graph", 500: "runner is unavailable"} {
		busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		t.Setenv("KIONGA_AGENT_DESIGNER_RUNNER_URL", busy.URL)
		run := s.waitRun(design.ID, decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"x"}`)).ID)
		busy.Close()
		if run.Status != "failed" || !strings.Contains(run.Error, want) {
			t.Fatalf("%d: %+v", status, run)
		}
	}
}

func TestEvaluationRetentionKeepsCitedAndNewest(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	for index := 0; index < 23; index++ {
		item := agentEvaluation{ID: fmt.Sprintf("age-%024d", index), DesignID: design.ID, ProjectID: "prj-demo", Status: "completed", CreatedAt: time.Now().Add(time.Duration(index) * time.Minute)}
		if _, err := store.UpdateDoc(s.repo, agentEvaluationKind, item.ID, func(agentEvaluation, bool) (agentEvaluation, error) { return item, nil }, "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	cited := agentVersion{ID: "agv-" + strings.Repeat("c", 24), DesignID: design.ID, ProjectID: "prj-demo", Version: 1, Evaluation: &versionEvaluation{ID: fmt.Sprintf("age-%024d", 0), Passed: true}}
	_, _ = store.UpdateDoc(s.repo, agentVersionKind, cited.ID, func(agentVersion, bool) (agentVersion, error) { return cited, nil }, "", "test")
	run := designRun{ID: "agr-" + strings.Repeat("e", 24), DesignID: design.ID, EvaluationID: fmt.Sprintf("age-%024d", 1), Mode: "evaluation", Status: "succeeded"}
	_, _ = store.UpdateDoc(s.repo, designRunKind, run.ID, func(designRun, bool) (designRun, error) { return run, nil }, "", "test")
	(&Server{store: s.repo}).pruneEvaluations(design.ID, "test")
	remaining := map[string]bool{}
	for _, item := range store.ListDocs[agentEvaluation](s.repo, agentEvaluationKind) {
		remaining[item.ID] = true
	}
	if len(remaining) != 21 || !remaining[fmt.Sprintf("age-%024d", 0)] || remaining[fmt.Sprintf("age-%024d", 1)] || !remaining[fmt.Sprintf("age-%024d", 22)] {
		t.Fatalf("retention kept %d: %v", len(remaining), remaining)
	}
	if _, err := s.repo.GetDocument(designRunKind, run.ID); err == nil {
		t.Fatal("runs of a pruned evaluation were kept")
	}
}

// An administrator whose role comes from SSO groups has no stored access
// profile; the run credential must keep that role, while a stored profile
// (re-read on every call) still wins so revocation applies mid-run.
func TestRunCredentialKeepsProfilelessAdministratorRole(t *testing.T) {
	s := newStudio(t)
	request := httptest.NewRequest("POST", "/api/v1/agent-designs", strings.NewReader(runnableDesign))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "sso-admin", Roles: []string{auth.RoleAdmin}}))
	created := httptest.NewRecorder()
	s.api.ServeHTTP(created, request)
	design := decodeInto[agentDesign](t, created)
	start := httptest.NewRequest("POST", "/api/v1/agent-designs/"+design.ID+"/runs", strings.NewReader(`{"input":"hello"}`))
	start = start.WithContext(auth.WithPrincipal(start.Context(), auth.Principal{Subject: "sso-admin", Roles: []string{auth.RoleAdmin}}))
	started := httptest.NewRecorder()
	s.api.ServeHTTP(started, start)
	if started.Code != 202 {
		t.Fatalf("start: %d %s", started.Code, started.Body.String())
	}
	run := decodeInto[designRun](t, started)
	if strings.Contains(started.Body.String(), `"roles"`) {
		t.Fatal("run roles leaked to the client")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := store.GetDoc[designRun](s.repo, designRunKind, run.ID)
		if current.terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.callbacks) != 1 || s.callbacks[0] != 200 {
		t.Fatalf("administrator's resource callback was denied: %v", s.callbacks)
	}
}

func subflowDesign(version string) string {
	return fmt.Sprintf(`{"project_id":"prj-demo","name":"Parent","revision":0,"nodes":[
{"id":"input","kind":"input","name":"In","x":0,"y":0},
{"id":"s","kind":"subflow","name":"S","x":200,"y":0,"config":{"subflow":%q}},
{"id":"output","kind":"output","name":"Out","x":400,"y":0}],
"edges":[{"from":"input","to":"s"},{"from":"s","to":"output"}]}`, version)
}

// Nested versions reach the runner with the run, are embedded in the
// published artifact, and must belong to the project and stay shallow.
func TestFlowsResolveIntoRunsAndArtifacts(t *testing.T) {
	s := newStudio(t)
	child := s.create(runnableDesign)
	leaf := decodeInto[agentVersion](t, s.do("POST", "/api/v1/agent-designs/"+child.ID+"/publish", `{"revision":1}`))
	parent := s.create(subflowDesign(leaf.ID))
	run := s.waitRun(parent.ID, decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+parent.ID+"/runs", `{"input":"hi"}`)).ID)
	if run.Status != "succeeded" {
		t.Fatalf("run: %+v", run)
	}
	s.mu.Lock()
	payload := s.payloads[len(s.payloads)-1]
	s.mu.Unlock()
	if _, ok := payload.Subflows[leaf.ID]; !ok || len(payload.Subflows) != 1 {
		t.Fatalf("runner did not receive the nested version: %v", payload.Subflows)
	}
	published := decodeInto[agentVersion](t, s.do("POST", "/api/v1/agent-designs/"+parent.ID+"/publish", `{"revision":1}`))
	if flow := published.Subflows[leaf.ID]; flow.ContentSHA256 != leaf.ContentSHA256 || len(flow.Graph.Nodes) != 4 {
		t.Fatalf("artifact subflows: %+v", published.Subflows)
	}

	// Chain parent → middle → leaf is three levels; one more is refused.
	middle := decodeInto[agentVersion](t, s.do("POST", "/api/v1/agent-designs/"+parent.ID+"/publish", `{"revision":1}`))
	top := s.create(subflowDesign(middle.ID))
	topVersion := decodeInto[agentVersion](t, s.do("POST", "/api/v1/agent-designs/"+top.ID+"/publish", `{"revision":1}`))
	if len(topVersion.Subflows) != 2 {
		t.Fatalf("three levels should publish with both nested versions: %+v", topVersion.Subflows)
	}
	tooDeep := s.create(subflowDesign(topVersion.ID))
	validation := s.do("POST", "/api/v1/agent-designs/"+tooDeep.ID+"/validate", "")
	if !strings.Contains(validation.Body.String(), "at most 3 levels") || !strings.Contains(validation.Body.String(), `"runnable":false`) {
		t.Fatalf("four levels accepted: %s", validation.Body.String())
	}

	foreign := agentVersion{ID: "agv-" + strings.Repeat("f", 24), DesignID: "agd-x", ProjectID: "prj-other", Version: 1, Graph: published.Graph}
	_, _ = store.UpdateDoc(s.repo, agentVersionKind, foreign.ID, func(agentVersion, bool) (agentVersion, error) { return foreign, nil }, "", "test")
	for _, ref := range []string{foreign.ID, "agv-" + strings.Repeat("0", 24)} {
		bad := s.create(subflowDesign(ref))
		blocked := s.do("POST", "/api/v1/agent-designs/"+bad.ID+"/runs", `{"input":"x"}`)
		if blocked.Code != 422 {
			t.Fatalf("%s: a version outside the project ran: %d %s", ref, blocked.Code, blocked.Body.String())
		}
	}
}

// Approvals: only the requesting run may create one for itself, only a
// signed-in person may decide it, and decisions are final.
func TestApprovalLifecycleAndAuthority(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	run := decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"block"}`))
	var token string
	for deadline := time.Now().Add(3 * time.Second); token == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		if len(s.payloads) > 0 {
			token = s.payloads[0].Gateway.RunToken
		}
		s.mu.Unlock()
	}
	machine := func(method, path, body string) *http.Response {
		request, _ := http.NewRequest(method, s.internal.URL+path, strings.NewReader(body))
		request.Header.Set("X-Kionga-Run-ID", run.ID)
		request.Header.Set("X-Kionga-Run-Token", token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := machine("POST", "/api/v1/agent-approvals", fmt.Sprintf(`{"run_id":"agr-%s","node_id":"ok","message":"m","timeout_seconds":60}`, strings.Repeat("0", 24))); response.StatusCode != 403 {
		t.Fatalf("a run requested an approval for another run: %d", response.StatusCode)
	}
	response := machine("POST", "/api/v1/agent-approvals", fmt.Sprintf(`{"run_id":%q,"node_id":"ok","message":"Send it? token=hunter2","timeout_seconds":60}`, run.ID))
	if response.StatusCode != 201 {
		t.Fatalf("create: %d", response.StatusCode)
	}
	var created agentApproval
	_ = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	if created.ProjectID != "prj-demo" || created.Status != "pending" || strings.Contains(created.Message, "hunter2") {
		t.Fatalf("approval: %+v", created)
	}
	if response := machine("POST", "/api/v1/agent-approvals/"+created.ID+"/decision", `{"decision":"approve"}`); response.StatusCode != 403 {
		t.Fatalf("the run route allowlist must stop self-approval: %d", response.StatusCode)
	}
	pending := s.do("GET", "/api/v1/agent-approvals?status=pending&project_id=prj-demo", "")
	if !strings.Contains(pending.Body.String(), created.ID) {
		t.Fatalf("pending list: %s", pending.Body.String())
	}
	if bad := s.do("POST", "/api/v1/agent-approvals/"+created.ID+"/decision", `{"decision":"maybe"}`); bad.Code != 422 {
		t.Fatalf("invalid decision: %d", bad.Code)
	}
	decided := s.do("POST", "/api/v1/agent-approvals/"+created.ID+"/decision", `{"decision":"approve","comment":"looks right"}`)
	if decided.Code != 200 || decodeInto[agentApproval](t, decided).DecidedBy != "user-1" {
		t.Fatalf("decide: %d %s", decided.Code, decided.Body.String())
	}
	if again := s.do("POST", "/api/v1/agent-approvals/"+created.ID+"/decision", `{"decision":"reject"}`); again.Code != 409 {
		t.Fatalf("a decision was overwritten: %d", again.Code)
	}
	polled := machine("GET", "/api/v1/agent-approvals/"+created.ID, "")
	var seen agentApproval
	_ = json.NewDecoder(polled.Body).Decode(&seen)
	polled.Body.Close()
	if seen.Status != "approved" {
		t.Fatalf("the run cannot read its decision: %+v", seen)
	}
	if direct := s.do("POST", "/api/v1/agent-approvals", `{"node_id":"ok","message":"m","timeout_seconds":60}`); direct.Code != 403 {
		t.Fatalf("a person created a machine approval: %d", direct.Code)
	}
	expired := agentApproval{ID: "apr-" + strings.Repeat("e", 24), ProjectID: "prj-demo", NodeID: "ok", Status: "pending", ExpiresAt: time.Now().Add(-time.Second)}
	_, _ = store.UpdateDoc(s.repo, agentApprovalKind, expired.ID, func(agentApproval, bool) (agentApproval, error) { return expired, nil }, "", "test")
	if late := s.do("POST", "/api/v1/agent-approvals/"+expired.ID+"/decision", `{"decision":"approve"}`); late.Code != 409 {
		t.Fatalf("an expired request was approved: %d", late.Code)
	}
	outsider := httptest.NewRequest("GET", "/api/v1/agent-approvals/"+created.ID, nil)
	outsider = outsider.WithContext(auth.WithPrincipal(outsider.Context(), auth.Principal{Subject: "user-2", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"agents"}, ProjectIDs: []string{"prj-other"}}))
	recorder := httptest.NewRecorder()
	s.api.ServeHTTP(recorder, outsider)
	if recorder.Code != 404 {
		t.Fatalf("another tenant read the approval: %d", recorder.Code)
	}
	s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs/"+run.ID+"/cancel", "")
}

func TestTokenDeltasStreamLiveAndAreNotStored(t *testing.T) {
	s := newStudio(t)
	design := s.create(runnableDesign)
	run := decodeInto[designRun](t, s.do("POST", "/api/v1/agent-designs/"+design.ID+"/runs", `{"input":"stream"}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied := designRequest(r.Method, r.URL.RequestURI(), "", auth.RoleUser)
		proxied.Header = r.Header.Clone()
		s.api.ServeHTTP(w, proxied.WithContext(auth.WithPrincipal(r.Context(), principal(proxied))))
	}))
	defer server.Close()
	request, _ := http.NewRequest("GET", server.URL+"/api/v1/agent-designs/"+design.ID+"/runs/"+run.ID+"/events", nil)
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	deltaText, event := "", ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") && event == "delta" {
			var payload struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload)
			deltaText += payload.Delta
		}
		if event == "end" {
			break
		}
	}
	if deltaText != "Hello there" {
		t.Fatalf("live deltas: %q", deltaText)
	}
	stored := s.waitRun(design.ID, run.ID)
	for _, raw := range stored.Events {
		if strings.Contains(string(raw), "node.delta") {
			t.Fatal("deltas must not be stored")
		}
	}
	if stored.Output != "Hello there" {
		t.Fatalf("output: %q", stored.Output)
	}
}
