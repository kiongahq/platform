package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
)

func mockGitHub(t *testing.T, reply func(*http.Request) (int, string)) {
	t.Helper()
	previous := githubHTTP
	githubHTTP = &http.Client{Transport: hubTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "api.github.com" && r.URL.Host != "github.com") {
			t.Fatalf("credential sent to unexpected host: %s", r.URL.Host)
		}
		code, body := reply(r)
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}), CheckRedirect: previous.CheckRedirect}
	t.Cleanup(func() { githubHTTP = previous })
	t.Setenv("KIONGA_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
}
func TestGitHubCredentialsAreEncryptedPersistentAndSubjectScoped(t *testing.T) {
	mockGitHub(t, func(r *http.Request) (int, string) {
		if r.Header.Get("Authorization") != "Bearer github_pat_secret" {
			t.Fatal("missing account authorization")
		}
		if r.URL.Path == "/user" {
			return 200, `{"login":"alice-gh"}`
		}
		return 200, `[{"id":1,"name":"ml","full_name":"alice/ml","html_url":"https://github.com/alice/ml","default_branch":"main"}]`
	})
	path := t.TempDir() + "/state.json"
	repo := store.New(path)
	handler := New(repo, fstest.MapFS{})
	response := hubCall(handler, "alice", "PUT", "/api/v1/settings/github", `{"token":"github_pat_secret"}`)
	if response.Code != 200 || strings.Contains(response.Body.String(), "github_pat_secret") {
		t.Fatalf("connect: %d %s", response.Code, response.Body.String())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "github_pat_secret") {
		t.Fatal("plaintext token persisted")
	}
	handler = New(store.New(path), fstest.MapFS{})
	repos := hubCall(handler, "alice", "GET", "/api/v1/settings/github/repositories", "")
	if repos.Code != 200 || !strings.Contains(repos.Body.String(), "alice/ml") {
		t.Fatalf("repositories: %d %s", repos.Code, repos.Body.String())
	}
	bob := hubCall(handler, "bob", "GET", "/api/v1/settings/github", "")
	if strings.Contains(bob.Body.String(), "alice-gh") || !strings.Contains(bob.Body.String(), `"connected":false`) {
		t.Fatal("connection crossed account boundary")
	}
	if other := hubCall(handler, "bob", "GET", "/api/v1/settings/github/repositories", ""); other.Code != 422 {
		t.Fatal("another account obtained repositories")
	}
	if deleted := hubCall(handler, "alice", "DELETE", "/api/v1/settings/github", ""); deleted.Code != 200 {
		t.Fatal("disconnect failed")
	}
	if _, _, err := (&Server{store: store.New(path)}).githubToken("alice"); err == nil {
		t.Fatal("token survived disconnect")
	}
}
func TestGitHubInstallationMustBelongToTheUserAndConfiguredApp(t *testing.T) {
	t.Setenv("KIONGA_GITHUB_APP_SLUG", "kionga-test")
	mockGitHub(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/user":
			return 200, `{"login":"alice"}`
		case "/user/installations":
			return 200, `{"installations":[{"id":42,"app_slug":"kionga-test","account":{"login":"team"},"repository_selection":"selected"},{"id":99,"app_slug":"another-app"}]}`
		case "/user/installations/42/repositories":
			return 200, `{"repositories":[{"id":8,"full_name":"team/allowed"}]}`
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
			return 500, ""
		}
	})
	handler := New(store.New(""), fstest.MapFS{})
	hubCall(handler, "alice", "PUT", "/api/v1/settings/github", `{"token":"github_pat_secret"}`)
	if denied := hubCall(handler, "alice", "PUT", "/api/v1/settings/github", `{"installation_id":99}`); denied.Code != 403 {
		t.Fatalf("foreign installation accepted: %d", denied.Code)
	}
	if listed := hubCall(handler, "alice", "GET", "/api/v1/settings/github/installations", ""); listed.Code != 200 || strings.Contains(listed.Body.String(), "another-app") {
		t.Fatal("unrelated app installations exposed")
	}
	if selected := hubCall(handler, "alice", "PUT", "/api/v1/settings/github", `{"installation_id":42}`); selected.Code != 200 {
		t.Fatalf("installation: %d %s", selected.Code, selected.Body.String())
	}
	if repos := hubCall(handler, "alice", "GET", "/api/v1/settings/github/repositories", ""); repos.Code != 200 || !strings.Contains(repos.Body.String(), "team/allowed") {
		t.Fatal("installation repository discovery failed")
	}
}
func TestGitHubOAuthStateIsBoundToThePlatformSubject(t *testing.T) {
	t.Setenv("KIONGA_GITHUB_APP_SLUG", "kionga-test")
	t.Setenv("KIONGA_GITHUB_APP_CLIENT_ID", "client")
	t.Setenv("KIONGA_GITHUB_APP_CLIENT_SECRET", "secret")
	t.Setenv("KIONGA_GITHUB_APP_CALLBACK_URL", "https://platform.test/api/v1/settings/github/callback")
	exchanges := 0
	mockGitHub(t, func(r *http.Request) (int, string) {
		if r.URL.Host == "github.com" {
			exchanges++
			if r.Method != "POST" || r.URL.Path != "/login/oauth/access_token" {
				t.Fatal("unexpected token exchange")
			}
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["client_secret"] != "secret" || payload["code"] != "valid-code" {
				t.Fatal("invalid exchange")
			}
			return 200, `{"access_token":"ghu_secret","expires_in":28800}`
		}
		return 200, `{"login":"alice"}`
	})
	handler := New(store.New(""), fstest.MapFS{})
	start := hubCall(handler, "alice", "GET", "/api/v1/settings/github/connect", "")
	if start.Code != 302 || start.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start: %s", start.Body.String())
	}
	target := start.Result().Header.Get("Location")
	parsed, _ := http.NewRequest("GET", target, nil)
	state := parsed.URL.Query().Get("state")
	cookies := start.Result().Cookies()
	if state == "" || len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatal("missing protected state cookie")
	}
	callback := func(subject, state string) *httptest.ResponseRecorder {
		req := newTestRequest("GET", "/api/v1/settings/github/callback?code=valid-code&state="+state, nil)
		req.AddCookie(cookies[0])
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Subject: subject, Roles: []string{"admin"}}))
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	if wrong := callback("bob", state); wrong.Code != 400 || exchanges != 0 {
		t.Fatal("OAuth crossed platform sessions")
	}
	if wrong := callback("alice", "wrong"); wrong.Code != 400 || exchanges != 0 {
		t.Fatal("invalid state exchanged a token")
	}
	if ok := callback("alice", state); ok.Code != 303 || exchanges != 1 || ok.Header().Get("Location") != "/console/settings?github=connected" {
		t.Fatalf("callback failed: %d %s", ok.Code, ok.Body.String())
	}
	if account := hubCall(handler, "alice", "GET", "/api/v1/settings/github", ""); !strings.Contains(account.Body.String(), `"connected":true`) || strings.Contains(account.Body.String(), "ghu_secret") {
		t.Fatal("OAuth account not connected or credential exposed")
	}
}
func TestGitHubAccountRequiresEncryptionBeforeCallingUpstream(t *testing.T) {
	mockGitHub(t, func(r *http.Request) (int, string) {
		t.Fatal("upstream called without credential encryption")
		return 500, ""
	})
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	response := hubCall(New(store.New(""), fstest.MapFS{}), "alice", "PUT", "/api/v1/settings/github", `{"token":"github_pat_secret"}`)
	if response.Code != 503 {
		t.Fatalf("encryption not enforced: %d", response.Code)
	}
}

func TestGitHubCallbackRequiresHTTPSExceptOnLoopback(t *testing.T) {
	for _, tc := range []struct {
		origin string
		valid  bool
		secure bool
	}{
		{"https://platform.test", true, true},
		{"http://localhost:8080", true, false},
		{"http://127.0.0.1:8080", true, false},
		{"http://[::1]:8080", true, false},
		{"http://platform.test", false, true},
		{"http://localhost.evil.test", false, true},
		{"http://192.168.1.1:8080", false, true},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			t.Setenv("KIONGA_GITHUB_APP_CALLBACK_URL", tc.origin+"/api/v1/settings/github/callback")
			if (githubCallback() != "") != tc.valid {
				t.Fatal("unexpected callback acceptance")
			}
			for _, age := range []int{600, -1} {
				cookie := githubOAuthCookie("state", age)
				if cookie.Secure != tc.secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != age {
					t.Fatalf("incorrect OAuth cookie attributes: %+v", cookie)
				}
			}
		})
	}
}
