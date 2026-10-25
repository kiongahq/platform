package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestAgentAPIConnectionKeepsTokenServerSideAndChecksProject(t *testing.T) {
	t.Setenv("KIONGA_TEST_API_TOKEN", "private-upstream-token")
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer private-upstream-token" {
			t.Errorf("unexpected upstream authentication: %s", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"reply":"upstream result"}`))
	}))
	defer upstream.Close()
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "API project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreateProject(api.CreateProjectRequest{Name: "Other API project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	body := `{"project_id":"` + project.ID + `","name":"support-api","description":"Lookup support cases","endpoint":"` + upstream.URL + `","secret_ref":"env:KIONGA_TEST_API_TOKEN"}`
	created := call(t, admin, http.MethodPost, "/api/v1/admin/agent-api-connections", body)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var summary agentConnectionSummary
	if err := json.Unmarshal(created.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(created.Body.String(), upstream.URL) || strings.Contains(created.Body.String(), "TOKEN") {
		t.Fatalf("sensitive config returned: %s", created.Body.String())
	}
	alice := domainServer(repository, auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{project.ID}})
	listed := call(t, alice, http.MethodGet, "/api/v1/agent-api-connections", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), summary.ID) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	response := call(t, alice, http.MethodPost, "/api/v1/agent-api-connections/"+summary.ID+"/invoke", `{"case_id":"123"}`)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "upstream result") {
		t.Fatalf("invoke: %d %s", response.Code, response.Body.String())
	}
	bob := domainServer(repository, auth.Principal{Subject: "bob", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{other.ID}})
	denied := call(t, bob, http.MethodPost, "/api/v1/agent-api-connections/"+summary.ID+"/invoke", `{"case_id":"123"}`)
	if denied.Code != 404 || calls != 1 {
		t.Fatalf("cross-project invoke: %d backend calls=%d", denied.Code, calls)
	}
}
