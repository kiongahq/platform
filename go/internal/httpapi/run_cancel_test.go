package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Regression for #34: cancelling a functions-mode run stops the in-flight
// invocation and never invokes a downstream function; reports that arrive
// after the cancel are logged as post-cancel and leave the run cancelled.
func TestCancelledFunctionRunStopsInvokingFunctions(t *testing.T) {
	var mu sync.Mutex
	invocations := []string{}
	extractStarted, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/system/functions":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/system/functions":
			_, _ = w.Write([]byte(`[{"name":"extract-fn","image":"extract:1","replicas":1},{"name":"score-fn","image":"score:1","replicas":1}]`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/function/"):
			name := strings.TrimPrefix(r.URL.Path, "/function/")
			mu.Lock()
			invocations = append(invocations, name)
			mu.Unlock()
			if name == "extract-fn" {
				close(extractStarted)
				select { // a long-running function: only the gateway's cancel ends it
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	defer close(release)
	t.Setenv("OPENFAAS_URL", upstream.URL)
	t.Setenv("PREFECT_API_URL", "")
	server := testServer()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		var request *http.Request
		if body == "" {
			request = newTestRequest(method, path, nil)
		} else {
			request = newTestRequest(method, path, strings.NewReader(body))
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	for _, function := range []string{"extract-fn", "score-fn"} {
		if response := call(http.MethodPost, "/api/v1/functions", `{"project_id":"prj-demo","name":"`+function+`","image":"`+function+`:1"}`); response.Code != http.StatusAccepted {
			t.Fatalf("deploy %s: %d %s", function, response.Code, response.Body.String())
		}
	}
	definitionResponse := call(http.MethodPost, "/api/v1/pipelines/definitions", `{"project_id":"prj-demo","name":"event-score","version":"1","execution_mode":"functions","jobs":[{"name":"extract","kind":"function","function":"extract-fn","resources":{},"retries":2},{"name":"score","kind":"function","function":"score-fn","depends_on":["extract"],"resources":{},"retries":0}]}`)
	var definition map[string]any
	_ = json.Unmarshal(definitionResponse.Body.Bytes(), &definition)
	submit := call(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"prj-demo","definition_id":"`+definition["id"].(string)+`"}`)
	var run api.PipelineRun
	if err := json.Unmarshal(submit.Body.Bytes(), &run); err != nil || submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body.String())
	}
	select {
	case <-extractStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("extract was never invoked")
	}
	if response := call(http.MethodPost, "/api/v1/pipelines/runs/"+run.ID+"/cancel", ""); response.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	var current api.PipelineRun
	for {
		_ = json.Unmarshal(call(http.MethodGet, "/api/v1/pipelines/runs/"+run.ID, "").Body.Bytes(), &current)
		steps := map[string]string{}
		for _, step := range current.Steps {
			steps[step.Name] = step.Status
		}
		if steps["extract"] == "skipped" && steps["score"] == "skipped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("steps did not settle after cancel: %v", steps)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // give a wrongly continuing executor time to invoke score
	mu.Lock()
	defer mu.Unlock()
	if len(invocations) != 1 || invocations[0] != "extract-fn" {
		t.Fatalf("functions invoked after cancel: %v", invocations)
	}
	if current.Status != "cancelled" {
		t.Fatalf("run status after post-cancel reports = %q, want cancelled", current.Status)
	}
	postCancel := false
	for _, entry := range current.Logs {
		if strings.HasPrefix(entry.Message, "post-cancel report: ") && entry.Level == "warning" {
			postCancel = true
		}
	}
	if !postCancel {
		t.Fatalf("reports after cancel must be logged as post-cancel: %+v", current.Logs)
	}
}

// A cancelled run keeps counting against the concurrency quota until its
// in-process workloads have stopped, so cancel-and-resubmit cannot exceed it.
func TestRunQuotaHeldUntilCancelledWorkloadsStop(t *testing.T) {
	repository := store.New()
	if _, err := repository.UpsertUserAccess("user-1", api.UpsertUserAccessRequest{
		Email: "user@example.com", Role: "user", Services: []string{"projects", "pipelines"}, ProjectIDs: []string{"prj-demo"},
		Compute: api.ComputeGrant{VCPUs: 2, MemoryGB: 4, MaxVMs: 1, MaxProjects: 1, MaxRuns: 1},
	}, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, existing := range repository.Runs() {
		if _, err := repository.CancelRun(existing.ID, "test"); err != nil && existing.Status != "succeeded" {
			t.Fatal(err)
		}
	}
	server := &Server{store: repository}
	user := auth.Principal{Subject: "user-1", Roles: []string{auth.RoleUser}, Services: []string{"projects", "pipelines"}, ProjectIDs: []string{"prj-demo"}, Provisioned: true}
	run, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: "prj-demo", Name: "training-pipeline"}, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	_, done := server.startRunExecution(run.ID)
	if err := server.enforceRunQuota(user); err == nil {
		t.Fatal("a running run must use the quota")
	}
	if _, err := repository.CancelRun(run.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if !server.stopRunExecution(run.ID) {
		t.Fatal("cancel must reach the in-process execution")
	}
	if err := server.enforceRunQuota(user); err == nil {
		t.Fatal("quota freed while the cancelled run's workloads are still stopping")
	}
	done() // the executor has deleted its Jobs and returned
	if err := server.enforceRunQuota(user); err != nil {
		t.Fatalf("quota must be freed once workloads stopped: %v", err)
	}
}

// A cancellation recorded by another replica stops this replica's executor.
func TestExecutionStopsWhenAnotherReplicaCancels(t *testing.T) {
	repository := store.New()
	run, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: "prj-demo", Name: "training-pipeline"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: repository, executions: runExecutions{poll: 5 * time.Millisecond}}
	ctx, done := server.startRunExecution(run.ID)
	defer done()
	if _, err := repository.CancelRun(run.ID, "other-replica"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("execution kept running after the run was cancelled elsewhere")
	}
}
