package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestDatasetCatalogScopesProjectBucketAndPrefix(t *testing.T) {
	seen := []url.Values{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query())
		if r.URL.Path == "/object" {
			_, _ = w.Write([]byte(`{"content":"dataset text"}`))
			return
		}
		_, _ = w.Write([]byte(`{"objects":[{"key":"training/churn/part.csv"}]}`))
	}))
	defer proxy.Close()
	t.Setenv("STORAGE_PROXY_URL", proxy.URL)
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Dataset project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreateProject(api.CreateProjectRequest{Name: "Other project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	create := func(projectID, name, bucket string) projectDataset {
		t.Helper()
		body := `{"project_id":"` + projectID + `","name":"` + name + `","bucket":"` + bucket + `","prefix":"training/churn/"}`
		response := call(t, admin, "POST", "/api/v1/admin/datasets", body)
		if response.Code != 201 {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		var item projectDataset
		if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	allowed := create(project.ID, "churn-training", "team-data")
	create(project.ID, "restricted-bucket", "other-data")
	create(other.ID, "other-project", "team-data")
	_, err = repository.UpsertUserAccess("alice", api.UpsertUserAccessRequest{
		Email: "alice@example.com", Role: "user", Services: []string{"langflow"},
		ProjectIDs: []string{project.ID}, Storage: api.StorageGrant{SizeGB: 10, Buckets: []string{"team-data"}},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	alice := domainServer(repository, auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"langflow"}, ProjectIDs: []string{project.ID}})
	listed := call(t, alice, "GET", "/api/v1/datasets", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), allowed.ID) || strings.Contains(listed.Body.String(), "restricted-bucket") || strings.Contains(listed.Body.String(), "other-project") {
		t.Fatalf("catalog leaked inaccessible dataset: %d %s", listed.Code, listed.Body.String())
	}
	objects := call(t, alice, "GET", "/api/v1/datasets/"+allowed.ID+"/objects?prefix=part", "")
	if objects.Code != 200 || len(seen) != 1 || seen[0].Get("bucket") != "team-data" || seen[0].Get("prefix") != "training/churn/part" {
		t.Fatalf("object listing escaped assigned prefix: %d, upstream=%v", objects.Code, seen)
	}
	preview := call(t, alice, "GET", "/api/v1/datasets/"+allowed.ID+"/object?key=training/churn/part.csv", "")
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "dataset text") {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	for _, path := range []string{
		"/api/v1/datasets/" + allowed.ID + "/objects?prefix=../private",
		"/api/v1/datasets/" + allowed.ID + "/object?key=training/private.csv",
	} {
		response := call(t, alice, "GET", path, "")
		if response.Code != 422 || len(seen) != 2 {
			t.Fatalf("out-of-prefix read reached proxy: %d, upstream=%v", response.Code, seen)
		}
	}
}
