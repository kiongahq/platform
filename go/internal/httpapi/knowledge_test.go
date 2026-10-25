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

func TestQdrantKnowledgeIsProjectScopedAndCredentialsStayServerSide(t *testing.T) {
	t.Setenv("KIONGA_QDRANT_TEST_KEY", "qdrant-secret-value")
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "qdrant-secret-value" {
			t.Errorf("missing Qdrant credential")
			w.WriteHeader(401)
			return
		}
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if strings.HasSuffix(r.URL.Path, "/points/query") {
			var query map[string]any
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Error(err)
			}
			filter := query["filter"].(map[string]any)["must"].([]any)
			if len(filter) != 2 {
				t.Errorf("missing project and knowledge-base filters: %#v", query)
			}
			_, _ = w.Write([]byte(`{"result":{"points":[{"id":"point-1","score":0.9,"payload":{"text":"Authorized chunk","metadata":{"source":"doc"}}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","result":true}`))
	}))
	defer upstream.Close()
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Knowledge project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreateProject(api.CreateProjectRequest{Name: "Other project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	adminServer := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	body := `{"project_id":"` + project.ID + `","name":"support-docs","backend":"qdrant","dimensions":3,"endpoint":"` + upstream.URL + `","secret_ref":"env:KIONGA_QDRANT_TEST_KEY"}`
	created := call(t, adminServer, http.MethodPost, "/api/v1/admin/knowledge-bases", body)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var base knowledgeSummary
	if err := json.Unmarshal(created.Body.Bytes(), &base); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(created.Body.String(), "secret") || strings.Contains(created.Body.String(), upstream.URL) {
		t.Fatalf("credential or endpoint exposed: %s", created.Body.String())
	}
	projectServer := domainServer(repository, auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{project.ID}})
	listed := call(t, projectServer, http.MethodGet, "/api/v1/knowledge-bases", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), base.ID) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	point := call(t, projectServer, http.MethodPost, "/api/v1/knowledge-bases/"+base.ID+"/chunks", `{"text":"Authorized chunk","vector":[1,0,0]}`)
	if point.Code != 201 {
		t.Fatalf("add: %d %s", point.Code, point.Body.String())
	}
	found := call(t, projectServer, http.MethodPost, "/api/v1/knowledge-bases/"+base.ID+"/search", `{"vector":[1,0,0],"limit":3}`)
	if found.Code != 200 || !strings.Contains(found.Body.String(), "Authorized chunk") {
		t.Fatalf("search: %d %s", found.Code, found.Body.String())
	}
	invalid := call(t, projectServer, http.MethodPost, "/api/v1/knowledge-bases/"+base.ID+"/search", `{"vector":[1,0],"limit":3}`)
	if invalid.Code != 422 {
		t.Fatalf("invalid vector: %d %s", invalid.Code, invalid.Body.String())
	}
	otherServer := domainServer(repository, auth.Principal{Subject: "bob", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{other.ID}})
	denied := call(t, otherServer, http.MethodPost, "/api/v1/knowledge-bases/"+base.ID+"/search", `{"vector":[1,0,0],"limit":3}`)
	if denied.Code != 404 {
		t.Fatalf("cross-project search: %d %s", denied.Code, denied.Body.String())
	}
	if len(requests) != 3 {
		t.Fatalf("unexpected backend calls: %v", requests)
	}
}

func TestKnowledgeConfigurationRejectsInlineCredentials(t *testing.T) {
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Knowledge project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	server := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	body := `{"project_id":"` + project.ID + `","name":"support-docs","backend":"qdrant","dimensions":3,"endpoint":"http://user:password@example.com","secret_ref":"plain-secret"}`
	result := call(t, server, http.MethodPost, "/api/v1/admin/knowledge-bases", body)
	if result.Code != 422 {
		t.Fatalf("inline credential accepted: %d %s", result.Code, result.Body.String())
	}
}
