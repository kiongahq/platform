package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProjectGitBindingAndPipelineDefinitionAPI(t *testing.T) {
	server := testServer()
	repository := httptest.NewRecorder()
	server.ServeHTTP(repository, httptest.NewRequest(http.MethodPut, "/api/v1/projects/prj-demo/repository", strings.NewReader(`{"url":"https://github.com/acme/fraud.git","default_branch":"main"}`)))
	if repository.Code != http.StatusOK || !strings.Contains(repository.Body.String(), `"provider":"github"`) {
		t.Fatalf("repository: %d %s", repository.Code, repository.Body.String())
	}

	definition := httptest.NewRecorder()
	server.ServeHTTP(definition, httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/definitions", strings.NewReader(`{"project_id":"prj-demo","name":"training","version":"1","execution_mode":"prefect","jobs":[{"name":"train","kind":"container","image":"ghcr.io/acme/train:1","resources":{"cpu":"500m","memory":"1Gi"},"retries":1}]}`)))
	if definition.Code != http.StatusCreated || !strings.Contains(definition.Body.String(), `"execution_mode":"prefect"`) {
		t.Fatalf("definition: %d %s", definition.Code, definition.Body.String())
	}
}

func TestFunctionPipelineInvokesJobsInDependencyOrder(t *testing.T) {
	var mu sync.Mutex
	invocations := []string{}
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
			_ = json.NewEncoder(w).Encode(map[string]any{"function": name, "ok": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv("OPENFAAS_URL", upstream.URL)
	t.Setenv("PREFECT_API_URL", "")
	server := testServer()
	for _, function := range []string{"extract-fn", "score-fn"} {
		response := httptest.NewRecorder()
		body := `{"project_id":"prj-demo","name":"` + function + `","image":"` + function + `:1"}`
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/functions", strings.NewReader(body)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("deploy %s: %d %s", function, response.Code, response.Body.String())
		}
	}
	definitionResponse := httptest.NewRecorder()
	server.ServeHTTP(definitionResponse, httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/definitions", strings.NewReader(`{"project_id":"prj-demo","name":"event-score","version":"1","execution_mode":"functions","jobs":[{"name":"extract","kind":"function","function":"extract-fn","resources":{},"retries":0},{"name":"score","kind":"function","function":"score-fn","depends_on":["extract"],"resources":{},"retries":0}]}`)))
	if definitionResponse.Code != http.StatusCreated {
		t.Fatalf("definition: %d %s", definitionResponse.Code, definitionResponse.Body.String())
	}
	var definition map[string]any
	_ = json.Unmarshal(definitionResponse.Body.Bytes(), &definition)
	submit := httptest.NewRecorder()
	server.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/submit", strings.NewReader(`{"project_id":"prj-demo","definition_id":"`+definition["id"].(string)+`","parameters":{"event_id":"evt-1"}}`)))
	if submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body.String())
	}
	var run map[string]any
	_ = json.Unmarshal(submit.Body.Bytes(), &run)
	// Wait for the run itself to finish: a step's "succeeded" in the body
	// is not the run's status.
	deadline := time.Now().Add(10 * time.Second)
	for status := ""; status != "succeeded"; {
		if time.Now().After(deadline) {
			t.Fatalf("run did not succeed in time; last status %q", status)
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines/runs/"+run["id"].(string), nil))
		var current struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &current)
		if status = current.Status; status == "failed" {
			t.Fatalf("run failed: %s", response.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(invocations) != 2 || invocations[0] != "extract-fn" || invocations[1] != "score-fn" {
		t.Fatalf("unexpected invocation order: %v", invocations)
	}
}
