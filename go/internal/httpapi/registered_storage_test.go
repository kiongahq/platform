package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestRegisteredStorageRespectsProjectGrantAndKeepsCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Query().Get("X-Amz-Credential"), "ACCESS/") {
			t.Errorf("missing S3 signature: %s", r.URL.String())
		}
		if r.URL.Path == "/data" {
			_, _ = w.Write([]byte(`<ListBucketResult><Contents><Key>reports/a.txt</Key><Size>5</Size></Contents></ListBucketResult>`))
			return
		}
		if r.URL.Path == "/data/reports/a.txt" {
			_, _ = w.Write([]byte("hello"))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	t.Setenv("KIONGA_STORAGE_SECRET_TEST", "ACCESS:PRIVATE")
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Storage project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreateProject(api.CreateProjectRequest{Name: "Other project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.SaveStorageConnection(repository, api.StorageConnection{
		ID: "st-demo", Name: "reports", Endpoint: upstream.URL, Bucket: "data", Region: "us-east-1", PathStyle: true,
		SecretRef: "env:KIONGA_STORAGE_SECRET_TEST", AllowedProjects: []string{project.ID},
	}, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	alice := domainServer(repository, auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{project.ID}})
	bob := domainServer(repository, auth.Principal{Subject: "bob", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{other.ID}})
	listed := call(t, alice, "GET", "/api/v1/registered-storage", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), connection.ID) || strings.Contains(listed.Body.String(), upstream.URL) || strings.Contains(listed.Body.String(), "PRIVATE") || strings.Contains(listed.Body.String(), "secret_ref") {
		t.Fatalf("unsafe catalog: %d %s", listed.Code, listed.Body.String())
	}
	objects := call(t, alice, "GET", "/api/v1/registered-storage/st-demo/objects?prefix=reports/", "")
	if objects.Code != 200 || !strings.Contains(objects.Body.String(), "reports/a.txt") {
		t.Fatalf("object browse: %d %s", objects.Code, objects.Body.String())
	}
	preview := call(t, alice, "GET", "/api/v1/registered-storage/st-demo/object?key=reports/a.txt", "")
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "hello") {
		t.Fatalf("object preview: %d %s", preview.Code, preview.Body.String())
	}
	if response := call(t, bob, "GET", "/api/v1/registered-storage/st-demo/object?key=reports/a.txt", ""); response.Code != 404 {
		t.Fatalf("other project accessed connection: %d %s", response.Code, response.Body.String())
	}
}

func TestRegisteredStorageListsOnlyProjectsTheCallerCanReach(t *testing.T) {
	repository := store.New()
	ids := map[string]string{}
	for _, name := range []string{"p1", "p2", "p3"} {
		project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Project " + name}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = project.ID
	}
	for id, projects := range map[string][]string{
		"st-one":    {ids["p1"]},
		"st-two":    {ids["p2"]},
		"st-shared": {ids["p1"], ids["p3"]},
		"st-three":  {ids["p3"]},
	} {
		if _, err := store.SaveStorageConnection(repository, api.StorageConnection{
			ID: id, Name: id, Endpoint: "https://objects.example.test", Bucket: "data", Region: "us-east-1", PathStyle: true,
			SecretRef: "env:KIONGA_STORAGE_SECRET_TEST", AllowedProjects: projects,
		}, true, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	list := func(value auth.Principal) map[string][]string {
		t.Helper()
		response := call(t, domainServer(repository, value), "GET", "/api/v1/registered-storage", "")
		if response.Code != 200 {
			t.Fatalf("list: %d %s", response.Code, response.Body.String())
		}
		var page struct {
			Items []registeredStorageSummary `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		result := map[string][]string{}
		for _, item := range page.Items {
			result[item.ID] = item.ProjectIDs
		}
		return result
	}
	member := list(auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{ids["p1"], ids["p2"]}})
	want := map[string][]string{"st-one": {ids["p1"]}, "st-two": {ids["p2"]}, "st-shared": {ids["p1"]}}
	if !reflect.DeepEqual(member, want) {
		t.Fatalf("member saw %v, want %v", member, want)
	}
	// A person viewing p1 filters on these ids: the p2-only connection does
	// not carry p1, and p3 is never disclosed.
	if slices.Contains(member["st-two"], ids["p1"]) {
		t.Fatalf("p2-only connection listed for p1: %v", member["st-two"])
	}
	for id, projects := range member {
		if slices.Contains(projects, ids["p3"]) {
			t.Fatalf("%s disclosed a project the caller is not in: %v", id, projects)
		}
	}
	admin := list(auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	if len(admin) != 4 || !reflect.DeepEqual(admin["st-shared"], []string{ids["p1"], ids["p3"]}) {
		t.Fatalf("administrator saw %v", admin)
	}
	workspace := list(auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{ids["p1"], ids["p2"]}, WorkspaceProjectID: ids["p2"]})
	if !reflect.DeepEqual(workspace, map[string][]string{"st-two": {ids["p2"]}}) {
		t.Fatalf("workspace credential saw %v", workspace)
	}
}
