package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func logFixture(t *testing.T) (http.Handler, *store.Store, api.PipelineRun, api.PipelineRun) {
	t.Helper()
	data := store.New()
	mine, _ := data.CreateProject(api.CreateProjectRequest{Name: "Mine project", OwnerSubject: "alice"}, "alice")
	theirs, _ := data.CreateProject(api.CreateProjectRequest{Name: "Theirs project"}, "admin")
	if _, err := data.UpsertUserAccess("alice", api.UpsertUserAccessRequest{Role: "user", Services: []string{"pipelines"}, ProjectIDs: []string{mine.ID}, Compute: api.ComputeGrant{MaxRuns: 5}}, "admin"); err != nil {
		t.Fatal(err)
	}
	runMine, _ := data.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: mine.ID}, "alice")
	runTheirs, _ := data.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: theirs.ID}, "admin")
	return New(data, nil), data, runMine, runTheirs
}

func as(principal auth.Principal, method, path, body string, handler http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var (
	engine = auth.Principal{Subject: "pipeline-engine", Roles: []string{auth.RoleService}}
	alice  = auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}}
)

func TestLogIngestIsMachineOnlyRedactedAndScopedToTheRun(t *testing.T) {
	handler, _, runMine, _ := logFixture(t)
	body := `{"entries":[{"node":"train","attempt":1,"stream":"stdout","message":"epoch 1 loss=0.4"},{"node":"train","stream":"stderr","message":"connecting with password=hunter2hunter2 and AKIAABCDEFGHIJKLMNOP"}]}`
	if response := as(alice, "POST", "/api/v1/pipelines/runs/"+runMine.ID+"/logs", body, handler); response.Code != http.StatusForbidden {
		t.Fatalf("user wrote logs: %d", response.Code)
	}
	if response := as(engine, "POST", "/api/v1/pipelines/runs/"+runMine.ID+"/logs", body, handler); response.Code != http.StatusAccepted {
		t.Fatalf("engine ingest: %d %s", response.Code, response.Body)
	}
	response := as(alice, "GET", "/api/v1/logs?run_id="+runMine.ID, "", handler)
	var page struct{ Items []api.LogEntry }
	_ = json.Unmarshal(response.Body.Bytes(), &page)
	if len(page.Items) != 2 || page.Items[0].ProjectID != runMine.ProjectID || page.Items[1].Severity != "warn" {
		t.Fatalf("entries: %s", response.Body)
	}
	if strings.Contains(response.Body.String(), "hunter2hunter2") || strings.Contains(response.Body.String(), "AKIAABCDEFGHIJKLMNOP") {
		t.Fatalf("secret stored: %s", response.Body)
	}
}

func TestLogQueriesAreProjectIsolated(t *testing.T) {
	handler, _, runMine, runTheirs := logFixture(t)
	for _, run := range []api.PipelineRun{runMine, runTheirs} {
		as(engine, "POST", "/api/v1/pipelines/runs/"+run.ID+"/logs", `{"entries":[{"node":"a","message":"hello from `+run.ProjectID+`"}]}`, handler)
	}
	if response := as(alice, "GET", "/api/v1/logs?run_id="+runTheirs.ID, "", handler); response.Code != http.StatusNotFound {
		t.Fatalf("other project's run readable: %d", response.Code)
	}
	if response := as(alice, "GET", "/api/v1/logs?project_id="+runTheirs.ProjectID, "", handler); response.Code != http.StatusNotFound {
		t.Fatalf("other project readable: %d", response.Code)
	}
	response := as(alice, "GET", "/api/v1/logs", "", handler)
	if strings.Contains(response.Body.String(), runTheirs.ProjectID) || !strings.Contains(response.Body.String(), runMine.ProjectID) {
		t.Fatalf("unscoped query leaked: %s", response.Body)
	}
}

func TestStepTransitionsBecomePlatformEvents(t *testing.T) {
	handler, _, runMine, _ := logFixture(t)
	code := 3
	payload, _ := json.Marshal(api.UpdateRunStepRequest{Step: "validate-data", Status: "failed", Message: "trace\nValueError: bad rows", ExitCode: &code, Attempt: 2, WorkloadKind: "docker-container", WorkloadID: "abc123"})
	if response := as(engine, "POST", "/api/v1/pipelines/runs/"+runMine.ID+"/steps", string(payload), handler); response.Code != http.StatusOK {
		t.Fatalf("step report: %d %s", response.Code, response.Body)
	}
	response := as(alice, "GET", "/api/v1/logs?run_id="+runMine.ID+"&source=platform&node=validate-data", "", handler)
	var page struct{ Items []api.LogEntry }
	_ = json.Unmarshal(response.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].Severity != "error" || page.Items[0].Message != "validate-data → failed: ValueError: bad rows" || page.Items[0].WorkloadID != "abc123" || page.Items[0].Attempt != 2 {
		t.Fatalf("platform event: %s", response.Body)
	}
}

func TestLogStreamDeliversAndResumes(t *testing.T) {
	handler, data, runMine, _ := logFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), alice)))
	}))
	defer server.Close()
	_ = data.AppendLogs([]api.LogEntry{{ProjectID: runMine.ProjectID, RunID: runMine.ID, Source: "runner", Severity: "info", Message: "first", Timestamp: time.Now()}})
	read := func(lastID string) (string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/logs/stream?run_id="+runMine.ID, nil)
		if lastID != "" {
			request.Header.Set("Last-Event-ID", lastID)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		id := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "id: ") {
				id = strings.TrimPrefix(line, "id: ")
			}
			if strings.HasPrefix(line, "data: ") {
				return id, line
			}
		}
		return id, ""
	}
	id, data1 := read("")
	if !strings.Contains(data1, `"first"`) || id == "" {
		t.Fatalf("stream first event: %q %q", id, data1)
	}
	_ = data.AppendLogs([]api.LogEntry{{ProjectID: runMine.ProjectID, RunID: runMine.ID, Source: "runner", Severity: "info", Message: "second", Timestamp: time.Now()}})
	_, data2 := read(id)
	if strings.Contains(data2, `"first"`) || !strings.Contains(data2, `"second"`) {
		t.Fatalf("resume did not continue after the cursor: %q", data2)
	}
}
