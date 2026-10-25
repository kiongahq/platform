package httpapi

import (
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLangflowOwnershipAndProjectScope(t *testing.T) {
	item := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "langflow-1234567890abcdef12345678"}, "spec": map[string]any{"subject": "alice", "projectID": "project-a", "services": []any{"langflow"}, "compute": map[string]any{"vcpus": int64(2), "memoryGB": int64(4)}, "storageGB": int64(10)}}}
	if !ownedLangflow(item, "alice") || ownedLangflow(item, "bob") {
		t.Fatal("workspace ownership was not enforced")
	}
	entry := langflowEntry(item)
	if entry.ProjectID != "project-a" || entry.CPU != 2 || entry.StorageGB != 10 {
		t.Fatalf("bad workspace metadata: %#v", entry)
	}
	repo := store.New("")
	p := auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, WorkspaceProjectID: "project-a"}
	if projectAllowed(repo, p, "project-a") {
		t.Fatal("revoked project grant must not survive through workspace identity")
	}
}

func TestLocalLangflowWorkspaceIsOnlyForBootstrapAdmin(t *testing.T) {
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "")
	t.Setenv("KIONGA_LANGFLOW_UPSTREAM", "http://langflow:7860")
	t.Setenv("OIDC_ISSUER", "")
	server, _ := accessServer(t)
	admin := httptest.NewRequest("GET", "/api/v1/langflow-workspaces", nil)
	admin = admin.WithContext(auth.WithPrincipal(admin.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
	result := httptest.NewRecorder()
	server.ServeHTTP(result, admin)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"id":"local"`) {
		t.Fatalf("admin workspace: %d %s", result.Code, result.Body.String())
	}
	other := httptest.NewRequest("GET", "/api/v1/langflow-workspaces", nil)
	other = other.WithContext(auth.WithPrincipal(other.Context(), auth.Principal{Subject: "alice", Roles: []string{auth.RoleAdmin}}))
	result = httptest.NewRecorder()
	server.ServeHTTP(result, other)
	if result.Code != 200 || strings.Contains(result.Body.String(), `"id":"local"`) {
		t.Fatalf("shared instance exposed to another account: %d %s", result.Code, result.Body.String())
	}
}
