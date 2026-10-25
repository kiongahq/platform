package httpapi

import (
	"net/http"
	"net/http/httptest"
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
	t.Setenv("KIONGA_TEST_STORAGE_SECRET", "ACCESS:PRIVATE")
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
		SecretRef: "env:KIONGA_TEST_STORAGE_SECRET", AllowedProjects: []string{project.ID},
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
