package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	platformapi "github.com/kiongahq/platform/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestWorkspaceIdentityLocal(t *testing.T) {
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("KIONGA_ENVIRONMENT", "local")
	h := WorkspaceIdentity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok || p.Subject != "admin" || p.Credential != "workspace" {
			t.Fatalf("wrong principal: %#v", p)
		}
		w.WriteHeader(200)
	}), nil, "", "secret-value", "admin")
	for _, test := range []struct {
		name, token, path string
		want              int
	}{
		{"valid", "secret-value", "/api/v1/projects", 200},
		{"missing", "", "/api/v1/projects", 401},
		{"wrong", "bad", "/api/v1/projects", 401},
		{"non-api", "secret-value", "/auth/logout", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", test.path, nil)
			r.Header.Set("X-Kionga-Workspace-Name", "local")
			r.Header.Set("X-Kionga-Workspace-Token", test.token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("got %d, want %d", w.Code, test.want)
			}
		})
	}
	t.Setenv("KIONGA_ENVIRONMENT", "production")
	r := httptest.NewRequest("GET", "/api/v1/projects", nil)
	r.Header.Set("X-Kionga-Workspace-Name", "local")
	r.Header.Set("X-Kionga-Workspace-Token", "secret-value")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("local workspace identity accepted in production: %d", w.Code)
	}
	t.Setenv("KIONGA_ENVIRONMENT", "local")
	r = httptest.NewRequest("GET", "/api/v1/projects", nil)
	r.Header.Set("X-Kionga-Workspace-Name", "local")
	r.Header.Set("X-Kionga-Workspace-Token", "secret-value")
	r.Header.Set("Authorization", "Bearer external-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("caller authorization header was accepted: %d", w.Code)
	}
}

func TestWorkspaceIdentityKubernetes(t *testing.T) {
	scheme := runtime.NewScheme()
	workspace := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "mlaiops.io/v1alpha1", "kind": "KiongaWorkspace", "metadata": map[string]interface{}{"name": "alice-ws", "namespace": "workspaces"}, "spec": map[string]interface{}{"subject": "alice", "services": []interface{}{"workbench"}}}}
	secret := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]interface{}{"name": "alice-ws-auth", "namespace": "workspaces", "annotations": map[string]interface{}{"mlaiops.io/subject": "alice"}}, "data": map[string]interface{}{"api-token": base64.StdEncoding.EncodeToString([]byte("a-long-independent-workspace-api-token"))}}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{workspaceResource: "KiongaWorkspaceList", secretResource: "SecretList"}, workspace, secret)
	h := WorkspaceIdentity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok || p.Subject != "alice" || p.Credential != "workspace" {
			t.Fatalf("wrong principal: %#v", p)
		}
		w.WriteHeader(200)
	}), client, "workspaces", "", "")
	request := func(token string) int {
		r := httptest.NewRequest("GET", "/api/v1/projects", nil)
		r.Header.Set("X-Kionga-Workspace-Name", "alice-ws")
		r.Header.Set("X-Kionga-Workspace-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("a-long-independent-workspace-api-token"); got != 200 {
		t.Fatalf("valid workspace rejected: %d", got)
	}
	if got := request("bad"); got != 401 {
		t.Fatalf("invalid credential accepted: %d", got)
	}
	_ = unstructured.SetNestedField(workspace.Object, true, "spec", "disabled")
	if _, err := client.Resource(workspaceResource).Namespace("workspaces").Update(t.Context(), workspace, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := request("a-long-independent-workspace-api-token"); got != 403 {
		t.Fatalf("disabled workspace accepted: %d", got)
	}
}

func TestWorkspaceIdentityPublishedAgent(t *testing.T) {
	name := "kionga-agent-published"
	token := "an-independent-published-agent-api-token"
	agent := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "mlaiops.io/v1alpha1", "kind": "KiongaAgent", "metadata": map[string]interface{}{"name": name, "namespace": "workspaces"}, "spec": map[string]interface{}{"flowJSON": `{"data":{"nodes":[]}}`, "ownerSubject": "alice", "projectID": "project-a"}}}
	secret := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]interface{}{"name": platformapi.AgentAPISecretName(name), "namespace": "workspaces", "annotations": map[string]interface{}{"mlaiops.io/subject": "alice"}}, "data": map[string]interface{}{"api-token": base64.StdEncoding.EncodeToString([]byte(token))}}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{workspaceResource: "KiongaWorkspaceList", agentIdentityResource: "KiongaAgentList", secretResource: "SecretList"}, agent, secret)
	h := WorkspaceIdentity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFrom(r.Context())
		if !ok || principal.Subject != "alice" || principal.WorkspaceProjectID != "project-a" || principal.Credential != "workspace" {
			t.Fatalf("wrong published agent principal: %#v", principal)
		}
		w.WriteHeader(200)
	}), client, "workspaces", "", "")
	request := func(value string) int {
		r := httptest.NewRequest("GET", "/api/v1/models", nil)
		r.Header.Set("X-Kionga-Workspace-Name", name)
		r.Header.Set("X-Kionga-Workspace-Token", value)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		return response.Code
	}
	if got := request(token); got != 200 {
		t.Fatalf("valid published agent rejected: %d", got)
	}
	if got := request("wrong"); got != 401 {
		t.Fatalf("invalid published agent token accepted: %d", got)
	}
	_ = unstructured.SetNestedField(agent.Object, "", "spec", "flowJSON")
	if _, err := client.Resource(agentIdentityResource).Namespace("workspaces").Update(t.Context(), agent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := request(token); got != 403 {
		t.Fatalf("revoked published agent accepted: %d", got)
	}
}
