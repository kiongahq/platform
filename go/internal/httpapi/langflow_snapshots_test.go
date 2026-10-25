package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
)

func TestLangflowSnapshotVersionsAndRejectsEmbeddedSecrets(t *testing.T) {
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "")
	t.Setenv("OIDC_ISSUER", "")
	flow := `{"id":"flow-1","name":"Support flow","data":{"nodes":[{"id":"input"}],"edges":[]}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/flows/flow-1") {
			_, _ = w.Write([]byte(flow))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/flows/") {
			_, _ = w.Write([]byte(`[ {"id":"flow-1","name":"Support flow"} ]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	t.Setenv("KIONGA_LANGFLOW_UPSTREAM", upstream.URL)
	server, repository := accessServer(t)
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		p := auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}
		return r.WithContext(auth.WithPrincipal(r.Context(), p))
	}
	list := httptest.NewRecorder()
	server.ServeHTTP(list, request("GET", "/api/v1/langflow-workspaces/local/flows", ""))
	if list.Code != 200 || !strings.Contains(list.Body.String(), "Support flow") {
		t.Fatalf("flows: %d %s", list.Code, list.Body.String())
	}
	create := httptest.NewRecorder()
	server.ServeHTTP(create, request("POST", "/api/v1/langflow-workspaces/local/snapshots", `{"flow_id":"flow-1","project_id":"prj-demo"}`))
	if create.Code != 201 {
		t.Fatalf("snapshot create: %d %s", create.Code, create.Body.String())
	}
	var saved struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	updateBody := `{"flow_id":"flow-1","project_id":"prj-demo","snapshot_id":"` + saved.ID + `","revision":1}`
	update := httptest.NewRecorder()
	server.ServeHTTP(update, request("POST", "/api/v1/langflow-workspaces/local/snapshots", updateBody))
	if update.Code != 200 || !strings.Contains(update.Body.String(), `"revision":2`) {
		t.Fatalf("snapshot update: %d %s", update.Code, update.Body.String())
	}
	stale := httptest.NewRecorder()
	server.ServeHTTP(stale, request("POST", "/api/v1/langflow-workspaces/local/snapshots", updateBody))
	if stale.Code != 409 {
		t.Fatalf("stale update: %d %s", stale.Code, stale.Body.String())
	}
	snapshot := httptest.NewRecorder()
	server.ServeHTTP(snapshot, request("GET", "/api/v1/langflow-snapshots/"+saved.ID, ""))
	if snapshot.Code != 200 || strings.Contains(snapshot.Body.String(), `"nodes"`) {
		t.Fatalf("snapshot metadata exposed graph: %d %s", snapshot.Code, snapshot.Body.String())
	}
	flow = `{"id":"flow-1","name":"Support flow","data":{"nodes":[{"id":"input","data":{"api_key":{"value":"raw-secret"}}}],"edges":[]}}`
	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, request("POST", "/api/v1/langflow-workspaces/local/snapshots", `{"flow_id":"flow-1","project_id":"prj-demo"}`))
	if rejected.Code != 422 {
		t.Fatalf("embedded credential accepted: %d %s", rejected.Code, rejected.Body.String())
	}
	for _, key := range []string{"token", "Authorization", "private_key", "client_secret"} {
		raw := `{"name":"unsafe","data":{"nodes":[{"id":"input","data":{"` + key + `":{"value":"raw-credential"}}}],"edges":[]}}`
		if _, err := validateLangflowExport([]byte(raw)); err == nil {
			t.Fatalf("embedded %s credential accepted", key)
		}
	}
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "team-a")
	t.Setenv("AGENT_RUNTIME_URL", "")
	t.Setenv("KIONGA_LANGFLOW_AGENT_IMAGE", "ghcr.io/kiongahq/langflow-agent-runtime:1.13.0")
	published := httptest.NewRecorder()
	server.ServeHTTP(published, request("POST", "/api/v1/langflow-snapshots/"+saved.ID+"/publish", `{"revision":1,"autoscaling":{"min_replicas":1,"max_replicas":2}}`))
	if published.Code != 202 || strings.Contains(published.Body.String(), `"nodes"`) || strings.Contains(published.Body.String(), `"flow_json"`) {
		t.Fatalf("publish: %d %s", published.Code, published.Body.String())
	}
	agents := repository.Agents()
	if len(agents) != 1 || agents[0].SnapshotRev != 1 || agents[0].GraphModule != "agents.langflow_adapter:build_langflow" || agents[0].FlowJSON == "" {
		t.Fatalf("published agent did not retain revision 1: %#v", agents)
	}
	visible := httptest.NewRecorder()
	server.ServeHTTP(visible, request("GET", "/api/v1/agents", ""))
	if visible.Code != 200 || strings.Contains(visible.Body.String(), `"flow_json"`) {
		t.Fatalf("agent API exposed flow: %d %s", visible.Code, visible.Body.String())
	}
}
