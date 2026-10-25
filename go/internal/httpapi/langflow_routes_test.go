package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestLangflowStableEndpointActivatesAndRollsBackReadyDeployment(t *testing.T) {
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path == "/invoke" {
			_, _ = w.Write([]byte(`{"reply":"served by ` + r.Header.Get("X-MLAIOps-Agent-ID") + `","session_id":"session-1"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer runtime.Close()
	t.Setenv("AGENT_RUNTIME_URL", runtime.URL)
	server, repository := accessServer(t)
	invoke := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		return response
	}
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Langflow route project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := langflowSnapshotRecord{ID: "lfs-1234567890abcdef12345678", ProjectID: project.ID, OwnerSubject: "admin", WorkspaceID: "local", FlowID: "flow-1", Name: "Echo", Revision: 2, Flow: json.RawMessage(`{"data":{"nodes":[],"edges":[]}}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err = store.UpdateDoc(repository, langflowSnapshotKind, snapshot.ID, func(_ langflowSnapshotRecord, _ bool) (langflowSnapshotRecord, error) { return snapshot, nil }, "langflow_snapshot.created", "admin")
	if err != nil {
		t.Fatal(err)
	}
	deploy := func(rev int) api.Agent {
		agent, err := repository.DeployAgent(api.DeployAgentRequest{ProjectID: project.ID, OwnerSubject: "admin", Name: "echo", Version: "r1", Image: "example.test/runtime:sha", GraphModule: "agents.langflow_adapter:build_langflow", FlowJSON: string(snapshot.Flow), SnapshotID: snapshot.ID, SnapshotRev: rev, Resources: api.AgentResources{CPU: "500m", Memory: "1Gi"}}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	first, second := deploy(1), deploy(2)
	path := "/api/v1/langflow-snapshots/" + snapshot.ID
	inactive := invoke(http.MethodPost, path+"/invoke", `{"message":"hello"}`)
	if inactive.Code != 503 {
		t.Fatalf("inactive endpoint: %d %s", inactive.Code, inactive.Body.String())
	}
	for _, agent := range []api.Agent{second, first} {
		activated := invoke(http.MethodPost, path+"/activate", `{"agent_id":"`+agent.ID+`"}`)
		if activated.Code != 200 {
			t.Fatalf("activate: %d %s", activated.Code, activated.Body.String())
		}
		response := invoke(http.MethodPost, path+"/invoke", `{"message":"hello"}`)
		if response.Code != 200 || !strings.Contains(response.Body.String(), agent.ID) {
			t.Fatalf("invoke active: %d %s", response.Code, response.Body.String())
		}
	}
	listed := invoke(http.MethodGet, "/api/v1/langflow-snapshots", "")
	if !strings.Contains(listed.Body.String(), `"active_revision":1`) || !strings.Contains(listed.Body.String(), first.ID) {
		t.Fatalf("rollback not reflected in snapshot list: %s", listed.Body.String())
	}
	other, err := repository.CreateProject(api.CreateProjectRequest{Name: "Unrelated route project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	outsider := httptest.NewRequest(http.MethodPost, path+"/invoke", strings.NewReader(`{"message":"hello"}`))
	outsider = outsider.WithContext(auth.WithPrincipal(outsider.Context(), auth.Principal{Subject: "bob", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow", "agents"}, ProjectIDs: []string{other.ID}}))
	denied := httptest.NewRecorder()
	server.ServeHTTP(denied, outsider)
	if denied.Code != 404 {
		t.Fatalf("cross-project route leaked: %d %s", denied.Code, denied.Body.String())
	}
}
