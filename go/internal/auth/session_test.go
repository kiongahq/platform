package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSessionLoginCreatesStateAndRedirects(t *testing.T) {
	manager, err := NewSessionManager(SessionConfig{
		ClientID: "console", ClientSecret: "secret",
		AuthURL: "https://identity.example/auth", TokenURL: "https://identity.example/token",
		RedirectURL: "https://kionga.example/auth/callback", Secure: true,
	}, &Verifier{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/login?return_to=%2Fconsole.html%3Fview%3Dmodels", nil)
	response := httptest.NewRecorder()
	manager.Handler(http.NotFoundHandler()).ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("login returned %d", response.Code)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Host != "identity.example" || location.Query().Get("state") == "" ||
		location.Query().Get("redirect_uri") != "https://kionga.example/auth/callback" {
		t.Fatalf("unexpected authorization redirect: %s", location)
	}
	if location.Query().Get("code_challenge_method") != "S256" || location.Query().Get("code_challenge") == "" ||
		location.Query().Get("nonce") == "" || location.Query().Has("connector_id") {
		t.Fatalf("authorization redirect lacks PKCE/nonce or sets a connector: %s", location)
	}
	cookies := response.Result().Cookies()
	names := map[string]bool{}
	for _, cookie := range cookies {
		names[cookie.Name] = true
		if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("login cookie %s is not hardened: %+v", cookie.Name, cookie)
		}
	}
	for _, name := range []string{stateCookieName, returnCookieName, verifierCookie, nonceCookieName} {
		if !names[name] {
			t.Fatalf("missing login cookie %s in %+v", name, cookies)
		}
	}
}

func TestSafeReturnToPreventsExternalRedirect(t *testing.T) {
	for _, value := range []string{"https://evil.example", "//evil.example/path", "javascript:alert(1)", ""} {
		if got := safeReturnTo(value); got != "/console.html" {
			t.Errorf("safeReturnTo(%q) = %q", value, got)
		}
	}
	if got := safeReturnTo("/console.html?view=models"); got != "/console.html?view=models" {
		t.Fatalf("valid return path changed: %s", got)
	}
}

func TestVerifierRedirectsBrowserButNotAPI(t *testing.T) {
	verifier := New(Config{})
	handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	console := httptest.NewRecorder()
	handler.ServeHTTP(console, httptest.NewRequest(http.MethodGet, "/console.html", nil))
	if console.Code != http.StatusFound || !strings.HasPrefix(console.Header().Get("Location"), "/auth/login?") {
		t.Fatalf("console must redirect to login: %d %s", console.Code, console.Header().Get("Location"))
	}
	api := httptest.NewRecorder()
	handler.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if api.Code != http.StatusUnauthorized {
		t.Fatalf("API must return 401, got %d", api.Code)
	}
	landing := httptest.NewRecorder()
	handler.ServeHTTP(landing, httptest.NewRequest(http.MethodGet, "/", nil))
	if landing.Code != http.StatusOK {
		t.Fatalf("landing must remain public, got %d", landing.Code)
	}
}

func TestPublishedBlogReadsArePublicButAdminWritesRequireAuthentication(t *testing.T) {
	verifier := New(Config{})
	handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	public := httptest.NewRecorder()
	handler.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/v1/blogs/example", nil))
	if public.Code != http.StatusOK {
		t.Fatalf("public blog read returned %d", public.Code)
	}
	admin := httptest.NewRecorder()
	handler.ServeHTTP(admin, httptest.NewRequest(http.MethodPost, "/api/v1/admin/blogs", strings.NewReader(`{}`)))
	if admin.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin blog write returned %d", admin.Code)
	}
}
