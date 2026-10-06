package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLocalConsoleRequiresLogin(t *testing.T) {
	manager := NewLocalSessionManager("admin", "correct-password")
	handler := manager.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/console.html", nil))
	if anonymous.Code != http.StatusFound || !strings.HasPrefix(anonymous.Header().Get("Location"), "/login.html") {
		t.Fatalf("anonymous console response: %d %s", anonymous.Code, anonymous.Header().Get("Location"))
	}

	form := url.Values{"username": {"admin"}, "password": {"correct-password"}, "return_to": {"/console.html"}}
	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(form.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusFound || login.Header().Get("Location") != "/console.html" {
		t.Fatalf("login response: %d %s", login.Code, login.Header().Get("Location"))
	}
	request := httptest.NewRequest(http.MethodGet, "/console.html", nil)
	for _, cookie := range login.Result().Cookies() {
		request.AddCookie(cookie)
	}
	authenticated := httptest.NewRecorder()
	handler.ServeHTTP(authenticated, request)
	if authenticated.Code != http.StatusOK {
		t.Fatalf("authenticated console response: %d", authenticated.Code)
	}
}

func TestLocalLoginRejectsInvalidCredentials(t *testing.T) {
	manager := NewLocalSessionManager("admin", "correct-password")
	form := url.Values{"username": {"admin"}, "password": {"wrong"}, "return_to": {"/workspace.html?tool=ide"}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	manager.Handler(http.NotFoundHandler()).ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login.html?error=invalid&return_to="+url.QueryEscape("/workspace.html?tool=ide") {
		t.Fatalf("invalid login response: %d %s", response.Code, response.Header().Get("Location"))
	}
}

func TestLocalAPIRequiresSessionAndLogoutRevokesIt(t *testing.T) {
	manager := NewLocalSessionManager("admin", "password")
	handler := manager.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			p, ok := PrincipalFrom(r.Context())
			if !ok || p.Subject != "admin" {
				t.Errorf("missing authenticated identity: %+v", p)
			}
		}
		w.WriteHeader(200)
	}))
	for _, path := range []string{"/api/v1/me", "/api/v1/projects", "/api/v1/settings/tokens"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 401 {
			t.Fatalf("anonymous %s = %d", path, response.Code)
		}
	}
	request := httptest.NewRequest("POST", "/auth/local/login", strings.NewReader("username=admin&password=password"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, request)
	cookie := login.Result().Cookies()[0]
	for _, test := range []struct {
		path string
		want int
	}{{"/api/v1/me", 200}, {"/auth/logout", 302}, {"/api/v1/projects", 401}} {
		request := httptest.NewRequest("GET", test.path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s = %d, want %d", test.path, response.Code, test.want)
		}
	}
}

func TestLocalAPIAllowsServiceCredentialButRejectsInvalidBearer(t *testing.T) {
	t.Setenv("MLAIOPS_INTERNAL_TOKEN", "test-internal-token")
	handler := NewLocalSessionManager("admin", "password").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		if !hasRole(p, RoleService) {
			t.Errorf("expected service identity")
		}
		w.WriteHeader(200)
	}))
	for _, test := range []struct {
		token string
		want  int
	}{{"test-internal-token", 200}, {"invalid", 401}} {
		request := httptest.NewRequest("POST", "/api/v1/features", nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("got %d want %d", response.Code, test.want)
		}
	}
}
