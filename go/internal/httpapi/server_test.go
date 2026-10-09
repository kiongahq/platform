package httpapi

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/store"
)

func testServer() http.Handler {
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	return New(store.New(), static)
}

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestLandingAndConsoleRoutes(t *testing.T) {
	static, _ := fs.Sub(fstest.MapFS{
		"index.html":   &fstest.MapFile{Data: []byte("<h1>Kionga landing</h1>")},
		"console.html": &fstest.MapFile{Data: []byte("<h1>Kionga console</h1>")},
	}, ".")
	server := New(store.New(), static)
	for _, test := range []struct {
		path, expected string
	}{
		{"/", "Kionga landing"},
		{"/console.html", "Kionga console"},
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.expected) {
			t.Fatalf("GET %s: %d %s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestCreateProjectAndSubmitPipeline(t *testing.T) {
	server := testServer()
	create := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Churn model","template":"tabular-classification"}`))
	created := httptest.NewRecorder()
	server.ServeHTTP(created, create)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"namespace":"churn-model"`) {
		t.Fatalf("unexpected create response: %d %s", created.Code, created.Body.String())
	}
	projectID := strings.Split(strings.Split(created.Body.String(), `"id":"`)[1], `"`)[0]
	submit := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/submit", strings.NewReader(`{"project_id":"`+projectID+`","name":"train"}`))
	submitted := httptest.NewRecorder()
	server.ServeHTTP(submitted, submit)
	// Without an engine the submission is refused before a run is recorded,
	// with a reason that names the missing engine (never a silent "queued").
	if submitted.Code != http.StatusUnprocessableEntity || !strings.Contains(submitted.Body.String(), "PREFECT_API_URL") {
		t.Fatalf("unexpected submit response: %d %s", submitted.Code, submitted.Body.String())
	}
}

func TestCreateProjectRejectsUnknownFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Valid name","secret":"nope"}`))
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestProjectTemplatesExposeHeavyMLAndAgentStarters(t *testing.T) {
	server := testServer()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/project-templates", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("templates: %d %s", response.Code, response.Body.String())
	}
	for _, id := range []string{"production-ml", "distributed-training", "production-agent", "fullstack-ai"} {
		if !strings.Contains(response.Body.String(), `"id":"`+id+`"`) {
			t.Fatalf("missing template %s: %s", id, response.Body.String())
		}
	}
}

func TestProjectTemplateLookupAndProjectConflict(t *testing.T) {
	server := testServer()
	found := httptest.NewRecorder()
	server.ServeHTTP(found, httptest.NewRequest(http.MethodGet, "/api/v1/project-templates/distributed-training", nil))
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), `"recommended_profile":"gpu"`) {
		t.Fatalf("template lookup: %d %s", found.Code, found.Body.String())
	}
	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/project-templates/not-real", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing template = %d, want 404", missing.Code)
	}
	for index, want := range []int{http.StatusCreated, http.StatusConflict} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Duplicate Project","template":"blank-python"}`)))
		if response.Code != want {
			t.Fatalf("create attempt %d = %d, want %d: %s", index+1, response.Code, want, response.Body.String())
		}
	}
}

func TestAgentDeploymentRequiresRegisteredTools(t *testing.T) {
	server := testServer()
	unknown := httptest.NewRecorder()
	server.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(`{"project_id":"prj-demo","name":"support-agent","version":"1.0.0","image":"ghcr.io/acme/support:1.0.0","graph_module":"agents.support:build","tools":["knowledge-search"]}`)))
	if unknown.Code != http.StatusUnprocessableEntity || !strings.Contains(unknown.Body.String(), "unregistered tool") {
		t.Fatalf("unknown tool should fail closed: %d %s", unknown.Code, unknown.Body.String())
	}

	registered := httptest.NewRecorder()
	server.ServeHTTP(registered, httptest.NewRequest(http.MethodPost, "/api/v1/tools", strings.NewReader(`{"name":"knowledge-search","version":"1.0.0","input_schema":{"type":"object"}}`)))
	if registered.Code != http.StatusCreated {
		t.Fatalf("register tool: %d %s", registered.Code, registered.Body.String())
	}

	deployed := httptest.NewRecorder()
	server.ServeHTTP(deployed, httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(`{"project_id":"prj-demo","name":"support-agent","version":"1.0.0","image":"ghcr.io/acme/support:1.0.0","graph_module":"agents.support:build","tools":["knowledge-search"]}`)))
	if deployed.Code != http.StatusAccepted || !strings.Contains(deployed.Body.String(), `"tools":["knowledge-search"]`) {
		t.Fatalf("registered tool should deploy: %d %s", deployed.Code, deployed.Body.String())
	}
}
