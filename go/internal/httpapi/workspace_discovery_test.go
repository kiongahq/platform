package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestWorkspaceDiscoveryIsolatesSubjects(t *testing.T) {
	workspace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mlaiops.io/v1alpha1", "kind": "KiongaWorkspace",
		"metadata": map[string]any{"name": "alice-workspace", "namespace": "default"},
		"spec":     map[string]any{"subject": "alice", "services": []any{"workbench", "ide"}},
	}}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "alice-workspace-auth", "namespace": "default"},
		"data":     map[string]any{"token": "cHJpdmF0ZQ=="},
	}}
	for _, tc := range []struct {
		name, subject, kind                           string
		disabled, missingSecret, duplicate, wantError bool
	}{
		{name: "owner notebook", subject: "alice", kind: "workbench"},
		{name: "owner IDE", subject: "alice", kind: "ide"},
		{name: "other subject", subject: "bob", kind: "ide", wantError: true},
		{name: "suspended", subject: "alice", kind: "ide", disabled: true, wantError: true},
		{name: "unassigned tool", subject: "alice", kind: "other", wantError: true},
		{name: "missing credential", subject: "alice", kind: "ide", missingSecret: true, wantError: true},
		{name: "ambiguous assignment", subject: "alice", kind: "ide", duplicate: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := workspace.DeepCopy()
			_ = unstructured.SetNestedField(ws.Object, tc.disabled, "spec", "disabled")
			objects := []runtime.Object{ws}
			if !tc.missingSecret {
				objects = append(objects, secret.DeepCopy())
			}
			if tc.duplicate {
				other := ws.DeepCopy()
				other.SetName("duplicate")
				objects = append(objects, other)
			}
			gvr := schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaworkspaces"}
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "KiongaWorkspaceList"}, objects...)
			target, err := discoverWorkspace(t.Context(), client, "default", tc.subject, tc.kind)
			if (err != nil) != tc.wantError {
				t.Fatalf("target=%v error=%v", target, err)
			}
			if err == nil {
				port := "8888"
				if tc.kind == "ide" {
					port = "8080"
				}
				if target.String() != "http://alice-workspace.default.svc:"+port || target.token != "private" {
					t.Fatalf("wrong destination: %v", target.URL)
				}
			}
		})
	}
}

func TestIDEBackendAuthentication(t *testing.T) {
	for _, code := range []int{302, 200, 401} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/login" || r.Method != "POST" || r.FormValue("password") != "test-password" {
					t.Error("wrong IDE login request")
				}
				http.SetCookie(w, &http.Cookie{Name: "code-server-session", Value: "backend-only"})
				w.WriteHeader(code)
			}))
			defer upstream.Close()
			endpoint, _ := url.Parse(upstream.URL)
			target := &workspaceDestination{URL: endpoint, token: "test-password"}
			cookie, err := ideSession(t.Context(), target)
			if code != 302 {
				if err == nil {
					t.Fatal("accepted invalid login response")
				}
				return
			}
			if err != nil || cookie != "code-server-session=backend-only" {
				t.Fatalf("cookie=%q err=%v", cookie, err)
			}
			_, err = ideSession(t.Context(), target)
			if err != nil || calls != 1 {
				t.Fatal("backend session was not reused")
			}
		})
	}
}

// Regression for #105: scaffold jobs authenticate with the workspace's own
// job-token, never the editor login token, and are unavailable until the
// operator has generated job-token.
func TestScaffoldJobTokenNeverReusesTheLoginToken(t *testing.T) {
	t.Setenv("KIONGA_WORKSPACE_JOB_TOKEN", "")
	workspace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mlaiops.io/v1alpha1", "kind": "KiongaWorkspace",
		"metadata": map[string]any{"name": "alice-workspace", "namespace": "default"},
		"spec":     map[string]any{"subject": "alice", "services": []any{"workbench"}},
	}}
	gvr := schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaworkspaces"}
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	for _, tc := range []struct {
		name string
		data map[string]any
		want string
	}{
		{"dedicated job token", map[string]any{"token": encode("login"), "job-token": encode("job-secret")}, "job-secret"},
		{"secret predating job-token", map[string]any{"token": encode("login")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Secret",
				"metadata": map[string]any{"name": "alice-workspace-auth", "namespace": "default"},
				"data":     tc.data,
			}}
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "KiongaWorkspaceList"}, workspace.DeepCopy(), secret)
			target, err := discoverWorkspace(t.Context(), client, "default", "alice", "workbench")
			if err != nil {
				t.Fatal(err)
			}
			if target.token != "login" {
				t.Fatalf("login token = %q", target.token)
			}
			if got := workspaceJobToken(target); got != tc.want {
				t.Fatalf("job token = %q, want %q (the login token must never be used)", got, tc.want)
			}
		})
	}
	static := &workspaceDestination{token: "login"}
	if got := workspaceJobToken(static); got != "" {
		t.Fatalf("a configured workspace without KIONGA_WORKSPACE_JOB_TOKEN must not fall back to its login token, got %q", got)
	}
}
