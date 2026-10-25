package httpapi

import (
	"encoding/json"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/pkg/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validDesign = `{"project_id":"prj-demo","name":"Support agent","revision":0,"nodes":[{"id":"input","kind":"input","name":"Input","x":10,"y":20},{"id":"output","kind":"output","name":"Output","x":300,"y":20}],"edges":[{"from":"input","to":"output"}]}`

func designRequest(method, path, body, role string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	return r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Subject: "user-1", Roles: []string{role}, Provisioned: true, Services: []string{"agents"}, ProjectIDs: []string{"prj-demo"}}))
}
func TestAgentDesignCRUDAndRevisionConflict(t *testing.T) {
	server, repo := accessServer(t)
	_, err := repo.UpsertUserAccess("user-1", api.UpsertUserAccessRequest{Email: "user@example.com", Role: "user", Services: []string{"agents"}, ProjectIDs: []string{"prj-demo"}, Storage: api.StorageGrant{SizeGB: 10}, Compute: api.ComputeGrant{VCPUs: 2, MemoryGB: 4, MaxVMs: 1, MaxProjects: 1, MaxRuns: 1}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRecorder()
	server.ServeHTTP(create, designRequest("POST", "/api/v1/agent-designs", validDesign, auth.RoleUser))
	if create.Code != 201 {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var item agentDesign
	if err := json.Unmarshal(create.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Revision != 1 || item.OwnerSubject != "user-1" {
		t.Fatalf("metadata: %#v", item)
	}
	list := httptest.NewRecorder()
	server.ServeHTTP(list, designRequest("GET", "/api/v1/agent-designs", "", auth.RoleUser))
	if !strings.Contains(list.Body.String(), item.ID) {
		t.Fatalf("missing design: %s", list.Body.String())
	}
	update := strings.Replace(validDesign, `"revision":0`, `"revision":1`, 1)
	put := httptest.NewRecorder()
	server.ServeHTTP(put, designRequest("PUT", "/api/v1/agent-designs/"+item.ID, update, auth.RoleUser))
	if put.Code != 200 {
		t.Fatalf("update: %d %s", put.Code, put.Body.String())
	}
	var revised agentDesign
	if err := json.Unmarshal(put.Body.Bytes(), &revised); err != nil {
		t.Fatal(err)
	}
	if revised.Revision != 2 || len(revised.History) != 1 || revised.History[0].Revision != 1 {
		t.Fatalf("revision history: %#v", revised)
	}
	stale := httptest.NewRecorder()
	server.ServeHTTP(stale, designRequest("PUT", "/api/v1/agent-designs/"+item.ID, update, auth.RoleUser))
	if stale.Code != 409 {
		t.Fatalf("stale update: %d", stale.Code)
	}
	if auth.Allowed(auth.Principal{Roles: []string{auth.RoleViewer}}, http.MethodPut, "/api/v1/agent-designs/"+item.ID) {
		t.Fatal("viewer should not be able to change a design")
	}
}
func TestAgentDesignRejectsCycleAndUnassignedProject(t *testing.T) {
	server, _ := accessServer(t)
	cyclic := strings.Replace(validDesign, `{"from":"input","to":"output"}`, `{"from":"input","to":"output"},{"from":"output","to":"input"}`, 1)
	var request saveDesignRequest
	if err := json.Unmarshal([]byte(cyclic), &request); err != nil {
		t.Fatal(err)
	}
	if len(validateAgentDesign(request)) == 0 {
		t.Fatal("cycle was accepted")
	}
	foreign := strings.Replace(validDesign, "prj-demo", "prj-other", 1)
	denied := httptest.NewRecorder()
	server.ServeHTTP(denied, designRequest("POST", "/api/v1/agent-designs", foreign, auth.RoleUser))
	if denied.Code != 403 {
		t.Fatalf("unassigned: %d", denied.Code)
	}
}
