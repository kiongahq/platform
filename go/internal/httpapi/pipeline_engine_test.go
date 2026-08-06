package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createTestProject(t *testing.T, server http.Handler) string {
	t.Helper()
	create := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Engine project"}`))
	created := httptest.NewRecorder()
	server.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("project create failed: %d", created.Code)
	}
	return strings.Split(strings.Split(created.Body.String(), `"id":"`)[1], `"`)[0]
}

func TestSubmitPipelineCreatesPrefectFlowRun(t *testing.T) {
	var deploymentPath string
	var createPayload map[string]any
	prefect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/deployments/name/"):
			deploymentPath = r.URL.Path
			_, _ = w.Write([]byte(`{"id":"dep-1"}`))
		case r.URL.Path == "/api/deployments/dep-1/create_flow_run":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode Prefect request: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"fr-77"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer prefect.Close()
	t.Setenv("PREFECT_API_URL", prefect.URL)

	server := testServer()
	projectID := createTestProject(t, server)
	submit := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/submit", strings.NewReader(`{"project_id":"`+projectID+`","name":"nightly-churn"}`))
	submitted := httptest.NewRecorder()
	server.ServeHTTP(submitted, submit)
	if submitted.Code != http.StatusAccepted || !strings.Contains(submitted.Body.String(), `"engine_run_id":"fr-77"`) {
		t.Fatalf("engine run not linked: %d %s", submitted.Code, submitted.Body.String())
	}
	if deploymentPath != "/api/deployments/name/training-pipeline/mlaiops" {
		t.Fatalf("name-only run must preserve the training deployment, got %s", deploymentPath)
	}
	if createPayload["name"] != "nightly-churn" {
		t.Fatalf("custom run name must remain a label, got %#v", createPayload)
	}
}

func TestDefinitionBackedPipelineUsesGenericPrefectDeployment(t *testing.T) {
	var deploymentPath string
	var createPayload map[string]any
	prefect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/deployments/name/"):
			deploymentPath = r.URL.Path
			_, _ = w.Write([]byte(`{"id":"definition-deployment"}`))
		case r.URL.Path == "/api/deployments/definition-deployment/create_flow_run":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode Prefect request: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"definition-flow-run"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer prefect.Close()
	t.Setenv("PREFECT_API_URL", prefect.URL)

	server := testServer()
	projectID := createTestProject(t, server)
	definitionResponse := httptest.NewRecorder()
	definitionBody := `{"project_id":"` + projectID + `","name":"feature-train","version":"1","execution_mode":"prefect","jobs":[{"name":"features","kind":"container","image":"feature:sha","command":["python","features.py"],"environment":{"MODE":"batch"},"resources":{"cpu":"500m","memory":"512Mi"},"retries":1},{"name":"train","kind":"container","image":"train:sha","command":["python","train.py"],"depends_on":["features"],"resources":{"cpu":"2","memory":"2Gi"},"retries":2}]}`
	server.ServeHTTP(
		definitionResponse,
		httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/definitions", strings.NewReader(definitionBody)),
	)
	if definitionResponse.Code != http.StatusCreated {
		t.Fatalf("definition create failed: %d %s", definitionResponse.Code, definitionResponse.Body.String())
	}
	var definition map[string]any
	if err := json.Unmarshal(definitionResponse.Body.Bytes(), &definition); err != nil {
		t.Fatal(err)
	}
	definitionID := definition["id"].(string)

	submitted := httptest.NewRecorder()
	submitBody := `{"project_id":"` + projectID + `","definition_id":"` + definitionID + `","parameters":{"window":"2026-08"}}`
	server.ServeHTTP(
		submitted,
		httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/submit", strings.NewReader(submitBody)),
	)
	if submitted.Code != http.StatusAccepted || !strings.Contains(submitted.Body.String(), `"engine_run_id":"definition-flow-run"`) {
		t.Fatalf("definition run not linked: %d %s", submitted.Code, submitted.Body.String())
	}
	if deploymentPath != "/api/deployments/name/pipeline-definition/mlaiops" {
		t.Fatalf("definition run resolved %s", deploymentPath)
	}
	parameters, ok := createPayload["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("Prefect parameters missing: %#v", createPayload)
	}
	definitionParameter, ok := parameters["definition"].(map[string]any)
	jobs, jobsOK := definitionParameter["jobs"].([]any)
	if !ok || !jobsOK || definitionParameter["id"] != definitionID || len(jobs) != 2 {
		t.Fatalf("definition payload was not forwarded intact: %#v", parameters)
	}
	userParameters, ok := parameters["parameters"].(map[string]any)
	if !ok || userParameters["window"] != "2026-08" {
		t.Fatalf("run parameters were not forwarded: %#v", userParameters)
	}
}

func TestSubmitPipelineFailsClosedWhenEngineRejects(t *testing.T) {
	prefect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer prefect.Close()
	t.Setenv("PREFECT_API_URL", prefect.URL)

	server := testServer()
	projectID := createTestProject(t, server)
	submit := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/submit", strings.NewReader(`{"project_id":"`+projectID+`","name":"training-pipeline"}`))
	submitted := httptest.NewRecorder()
	server.ServeHTTP(submitted, submit)
	if submitted.Code != http.StatusAccepted || !strings.Contains(submitted.Body.String(), `"status":"failed"`) {
		t.Fatalf("engine rejection must fail the run visibly: %d %s", submitted.Code, submitted.Body.String())
	}
}

func TestStepReportEndpoint(t *testing.T) {
	server := testServer()
	projectID := createTestProject(t, server)
	submit := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/submit", strings.NewReader(`{"project_id":"`+projectID+`"}`))
	submitted := httptest.NewRecorder()
	server.ServeHTTP(submitted, submit)
	runID := strings.Split(strings.Split(submitted.Body.String(), `"id":"`)[1], `"`)[0]

	report := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/runs/"+runID+"/steps", strings.NewReader(`{"step":"validate-data","status":"running","message":"loading rows"}`))
	reported := httptest.NewRecorder()
	server.ServeHTTP(reported, report)
	if reported.Code != http.StatusOK || !strings.Contains(reported.Body.String(), `"status":"running"`) {
		t.Fatalf("step report failed: %d %s", reported.Code, reported.Body.String())
	}
}
