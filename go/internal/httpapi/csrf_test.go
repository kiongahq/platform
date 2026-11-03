package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/pkg/api"
)

const consoleOrigin = "https://kionga.example.com"

// cookieRequest is a browser request from user-1 that carries the console
// session cookie, which a same-site workspace page also sends.
func cookieRequest(method, path, body string, headers map[string]string) *http.Request {
	request := userRequest(method, path, body)
	request.Host = "kionga.example.com"
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return request
}

// Regression for #32: a notebook on a same-site workspace host must not be
// able to submit pipelines or write editorial posts with the console cookie.
func TestCookieWritesFromWorkspaceHostsAreRejected(t *testing.T) {
	t.Setenv("MLAIOPS_ALLOWED_ORIGIN", consoleOrigin)
	server, _ := accessServer(t)
	submit := `{"project_id":"prj-demo","name":"training-pipeline"}`
	for _, test := range []struct {
		name    string
		headers map[string]string
		denied  bool
	}{
		{"same-origin fetch", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": consoleOrigin}, false},
		{"allowed origin without fetch metadata", map[string]string{"Origin": consoleOrigin}, false},
		{"no fetch metadata or Origin (non-browser client)", nil, false},
		{"same-site workspace host", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://ws-x.example.com"}, true},
		{"same-site without Origin", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"workspace Origin without fetch metadata", map[string]string{"Origin": "https://ws-x.example.com"}, true},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.test"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/pipelines/submit", "/api/v1/editorial/posts", "/api/v1/settings/tokens"} {
				response := httptest.NewRecorder()
				server.ServeHTTP(response, cookieRequest(http.MethodPost, path, submit, test.headers))
				forbiddenByOrigin := response.Code == http.StatusForbidden && strings.Contains(response.Body.String(), `"cross_origin"`)
				if forbiddenByOrigin != test.denied {
					t.Errorf("POST %s = %d %s, want cross-origin denial %v", path, response.Code, response.Body.String(), test.denied)
				}
			}
		})
	}
}

func TestSafeMethodsAndCookielessClientsPassTheOriginGuard(t *testing.T) {
	t.Setenv("MLAIOPS_ALLOWED_ORIGIN", consoleOrigin)
	server, _ := accessServer(t)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, cookieRequest(http.MethodGet, "/api/v1/projects", "", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://ws-x.example.com"}))
	if response.Code != http.StatusOK {
		t.Fatalf("same-site GET = %d, want 200: %s", response.Code, response.Body.String())
	}
	// A bearer or nxs_ token client carries no ambient cookie, so a stray
	// Origin header does not make it forgeable.
	request := userRequest(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"prj-demo","name":"training-pipeline"}`)
	request.Header.Set("Authorization", "Bearer nxs_token")
	request.Header.Set("Origin", "https://ws-x.example.com")
	request.Header.Set("Sec-Fetch-Site", "same-site")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code == http.StatusForbidden && strings.Contains(response.Body.String(), `"cross_origin"`) {
		t.Fatalf("cookie-less token request was rejected by the origin guard: %s", response.Body.String())
	}
}

// text/plain and form bodies are CORS simple requests (no preflight), so
// every handler that uses decode must refuse them.
func TestDecodeRequiresJSONContentType(t *testing.T) {
	server, _ := accessServer(t)
	for _, contentType := range []string{"text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		for _, path := range []string{"/api/v1/projects", "/api/v1/pipelines/submit", "/api/v1/settings/tokens", "/api/v1/access-requests"} {
			request := userRequest(http.MethodPost, path, `{"name":"x"}`)
			request.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusUnsupportedMediaType {
				t.Errorf("POST %s as %s = %d, want 415: %s", path, contentType, response.Code, response.Body.String())
			}
		}
	}
	request := userRequest(http.MethodPost, "/api/v1/projects", `{"name":"x"}`)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code == http.StatusUnsupportedMediaType {
		t.Fatalf("application/json with parameters must be accepted: %s", response.Body.String())
	}
}

func TestEditorialGuardRejectsSameSiteWrites(t *testing.T) {
	t.Setenv("MLAIOPS_ALLOWED_ORIGIN", consoleOrigin)
	e := &editorialAPI{origin: newCrossOriginProtection()}
	reached := false
	handler := e.member(func(http.ResponseWriter, *http.Request, api.EditorialMember) { reached = true })
	for _, site := range []string{"same-site", "cross-site"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/editorial/posts", strings.NewReader("{}"))
		request.Header.Set("Sec-Fetch-Site", site)
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusForbidden || reached {
			t.Fatalf("%s editorial write = %d (handler reached %v), want 403", site, response.Code, reached)
		}
	}
}
