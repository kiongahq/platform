package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func TestConnectionChecksRejectHTTPFailuresAndRedirects(t *testing.T) {
	for _, code := range []int{200, 302, 401, 403, 404, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer upstream.Close()
			err := checkConnection(httptest.NewRequest("GET", "/", nil), api.Connection{Type: "mlflow", Endpoint: upstream.URL, SecretRef: "none"})
			if (err == nil) != (code == 200) {
				t.Fatalf("status %d: %v", code, err)
			}
		})
	}
}

func TestActivatedPrefectPersistsAndDrivesRuntime(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Error("missing configured credential")
		}
		switch r.URL.Path {
		case "/api/health":
			w.Write([]byte("true"))
		case "/api/deployments/name/training-pipeline/mlaiops":
			w.Write([]byte(`{"id":"deployment-1"}`))
		case "/api/deployments/deployment-1/create_flow_run":
			calls++
			w.Write([]byte(`{"id":"flow-1"}`))
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("TEST_PREFECT_SECRET", "private-test-token")
	path := t.TempDir() + "/state.json"
	repository := store.New(path)
	connection, err := repository.CreateConnection(api.CreateConnectionRequest{Name: "Test Prefect", Type: "prefect", Endpoint: upstream.URL + "/api", SecretRef: "env:TEST_PREFECT_SECRET"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	server := New(repository, fstest.MapFS{})
	activated := httptest.NewRecorder()
	server.ServeHTTP(activated, httptest.NewRequest("POST", "/api/v1/connections/"+connection.ID+"/activate", strings.NewReader("{}")))
	if activated.Code != 200 {
		t.Fatalf("activate: %d %s", activated.Code, activated.Body.String())
	}
	reloaded := store.New(path)
	s := &Server{store: reloaded}
	if s.activeConnection("prefect") == nil {
		t.Fatal("activation not persisted")
	}
	result, err := s.prefectClient().CreateFlowRun(t.Context(), "training-pipeline", "mlaiops", "test", nil)
	if err != nil || result != "flow-1" || calls != 1 {
		t.Fatalf("runtime not wired: %s %v calls=%d", result, err, calls)
	}
	t.Setenv("TEST_PREFECT_SECRET", "")
	if _, err = s.prefectClient().CreateFlowRun(t.Context(), "training-pipeline", "mlaiops", "test", nil); err == nil {
		t.Fatal("missing credential must not fall back to anonymous")
	}
}

func TestWorkspaceProxyChecksIdentityAndStripsCredentials(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Cookie") != "" {
			t.Error("forwarded platform cookie")
		}
		if r.Header.Get("Authorization") != "token notebook-secret" {
			t.Error("incorrect upstream credential")
		}
		if r.URL.Path != "/workspaces/workbench/api" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Header().Set("Set-Cookie", "untrusted=1; Path=/")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", upstream.URL)
	t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
	s := &Server{store: store.New()}
	request := httptest.NewRequest("GET", "/workspaces/workbench/api", nil)
	request.SetPathValue("kind", "workbench")
	request.Header.Set("Cookie", "kionga_local_session=private")
	request.Header.Set("Authorization", "Bearer private")
	response := httptest.NewRecorder()
	s.workspaceProxy(response, request)
	if response.Code != 401 || calls != 0 {
		t.Fatalf("anonymous: %d calls=%d", response.Code, calls)
	}
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
	response = httptest.NewRecorder()
	s.workspaceProxy(response, request)
	if response.Code != 200 || calls != 1 || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("proxy failed: %d calls=%d", response.Code, calls)
	}
	request.Header.Set("Origin", "https://attacker.example")
	response = httptest.NewRecorder()
	s.workspaceProxy(response, request)
	if response.Code != 403 || calls != 1 {
		t.Fatalf("cross-origin request forwarded: %d", response.Code)
	}
}

func TestWorkspaceUserCannotUseSharedAdminUpstream(t *testing.T) {
	t.Setenv("KIONGA_IDE_UPSTREAM", "http://ide:8080")
	request := httptest.NewRequest("GET", "/workspaces/ide/", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "user-1", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"ide"}}))
	if _, err := workspaceTarget("ide", request); err == nil {
		t.Fatal("shared admin workspace exposed to normal user")
	}
	for _, role := range []string{auth.RoleViewer, auth.RoleService} {
		if auth.Allowed(auth.Principal{Roles: []string{role}}, "GET", "/workspaces/ide/") {
			t.Fatalf("%s can execute via workspace", role)
		}
	}
}

func TestWorkspaceLaunchPreparesAssignedProjectFolder(t *testing.T) {
	requests := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/proxy/8890/prepare-project" {
			w.WriteHeader(200)
			return
		}
		if r.Method == "GET" {
			w.WriteHeader(404)
		} else {
			w.WriteHeader(201)
		}
	}))
	defer upstream.Close()
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", upstream.URL)
	t.Setenv("KIONGA_IDE_UPSTREAM", upstream.URL)
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Shared project", Template: "blank"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: repository}
	request := httptest.NewRequest("POST", "/api/v1/workspaces/ide/launch", strings.NewReader(`{"project_id":"`+project.ID+`"}`))
	request.SetPathValue("kind", "ide")
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
	response := httptest.NewRecorder()
	s.launchWorkspace(response, request)
	if response.Code != 200 {
		t.Fatalf("launch: %d %s", response.Code, response.Body.String())
	}
	var result map[string]string
	json.Unmarshal(response.Body.Bytes(), &result)
	if !strings.Contains(result["url"], "folder=%2Fworkspace%2Fprojects%2Fshared-project") || len(requests) != 1 || requests[0] != "POST /proxy/8890/prepare-project" {
		t.Fatalf("bad handoff: %v %v", result, requests)
	}
}

func TestProjectOptionsExposeOnlyAssignedMinimalMetadata(t *testing.T) {
	repository := store.New()
	one, _ := repository.CreateProject(api.CreateProjectRequest{Name: "Assigned project", Template: "blank"}, "owner")
	repository.CreateProject(api.CreateProjectRequest{Name: "Other project", Template: "blank"}, "other")
	s := &Server{store: repository}
	request := httptest.NewRequest("GET", "/api/v1/project-options", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "user", Roles: []string{auth.RoleUser}, ProjectIDs: []string{one.ID}}))
	response := httptest.NewRecorder()
	s.projectOptions(response, request)
	if strings.Contains(response.Body.String(), "Other project") || strings.Contains(response.Body.String(), "repository") || !strings.Contains(response.Body.String(), one.ID) {
		t.Fatalf("options scope: %s", response.Body.String())
	}
}
