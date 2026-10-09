package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
)

// workbenchReadiness runs GET /api/v1/workspaces as an admin against a fake
// Jupyter upstream and returns the JupyterLab entry.
func workbenchReadiness(t *testing.T, upstream http.HandlerFunc) map[string]any {
	t.Helper()
	server := httptest.NewServer(upstream)
	defer server.Close()
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", server.URL)
	t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
	t.Setenv("KIONGA_IDE_UPSTREAM", "http://127.0.0.1:1")
	s := &Server{store: store.New()}
	request := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
	response := httptest.NewRecorder()
	s.workspaces(response, request)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item["id"] == "workbench" {
			return item
		}
	}
	t.Fatal("workbench entry missing")
	return nil
}

func TestWorkspaceReadinessStates(t *testing.T) {
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		state     string
		available bool
		mention   string
	}{
		{
			name: "ready",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/workspaces/workbench/api" || r.Header.Get("Authorization") != "token notebook-secret" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Write([]byte(`{"version":"2.20.0"}`))
			},
			state: workspaceReady, available: true, mention: "Ready",
		},
		{
			// Regression: a stale image served Jupyter at / so every
			// prefixed probe 404'd and the console blamed credentials.
			name: "outdated image without base_url",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api" {
					w.Write([]byte(`{"version":"2.20.0"}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			state: workspaceMisrouted, mention: "outdated image",
		},
		{
			name:    "no jupyter api at all",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
			state:   workspaceMisrouted, mention: "base_url",
		},
		{
			name:    "token rejected",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
			state:   workspaceUnauthorized, mention: "JUPYTER_TOKEN",
		},
		{
			name: "login redirect",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/workspaces/workbench/login", http.StatusFound)
			},
			state: workspaceUnauthorized, mention: "token",
		},
		{
			name:    "server error",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			state:   workspaceStarting, mention: "HTTP 502",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := workbenchReadiness(t, tc.handler)
			if item["state"] != tc.state || item["available"] != tc.available {
				t.Fatalf("state=%v available=%v message=%v", item["state"], item["available"], item["message"])
			}
			if message, _ := item["message"].(string); !strings.Contains(message, tc.mention) {
				t.Fatalf("message %q does not mention %q", message, tc.mention)
			}
			if message, _ := item["message"].(string); strings.Contains(message, "notebook-secret") {
				t.Fatal("token leaked into readiness message")
			}
		})
	}
}

func TestWorkspaceReadinessOffline(t *testing.T) {
	t.Setenv("KIONGA_ENVIRONMENT", "local")
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", "http://127.0.0.1:1")
	t.Setenv("KIONGA_IDE_UPSTREAM", "http://127.0.0.1:1")
	s := &Server{store: store.New()}
	request := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
	response := httptest.NewRecorder()
	s.workspaces(response, request)
	if !strings.Contains(response.Body.String(), `"state":"offline"`) {
		t.Fatalf("offline upstream not reported: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "make -C deploy ide-up") || strings.Contains(response.Body.String(), "mlops directory") {
		t.Fatalf("IDE startup guidance is stale: %s", response.Body.String())
	}
}

// Regression: Jupyter compares a websocket's Origin with its Host. Through
// the proxy, Host is the upstream, so a browser Origin caused 403s on kernel
// and collaboration sockets. Verified same-origin requests now present the
// upstream origin; cross-origin requests are still refused before proxying.
func TestWorkspaceProxyPresentsUpstreamOrigin(t *testing.T) {
	var seenOrigin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenOrigin = r.Header.Get("Origin")
	}))
	defer upstream.Close()
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", upstream.URL)
	t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
	s := &Server{store: store.New()}
	request := httptest.NewRequest("GET", "http://localhost:18080/workspaces/workbench/api/collaboration/room/x", nil)
	request.SetPathValue("kind", "workbench")
	request.Header.Set("Origin", "http://localhost:18080")
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}))
	s.workspaceProxy(httptest.NewRecorder(), request)
	if seenOrigin != upstream.URL {
		t.Fatalf("upstream saw Origin %q, want %q", seenOrigin, upstream.URL)
	}
	seenOrigin = "unset"
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	s.workspaceProxy(response, request)
	if response.Code != http.StatusForbidden || seenOrigin != "unset" {
		t.Fatalf("cross-origin request proxied: %d %q", response.Code, seenOrigin)
	}
}
