package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

type ssoHarness struct {
	*kubeHarness
	router   http.Handler
	repo     *store.Store
	upstream *httptest.Server
	received chan *http.Request
}

// newSSOHarness deploys billing (visibility sso) in prj-demo and points its
// upstream annotation at a test server.
func newSSOHarness(t *testing.T) *ssoHarness {
	t.Helper()
	harness := newKubeHarness(t)
	t.Setenv("KIONGA_ALLOWED_ORIGIN", "https://console.example.com")
	repo := store.New()
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	harness.server = New(repo, static)
	for subject, projects := range map[string][]string{"alice": {"prj-demo"}, "bob": {"prj-other"}} {
		if _, err := repo.UpsertUserAccess(subject, api.UpsertUserAccessRequest{Email: subject + "@example.com", Role: "user", Services: []string{"applications"}, ProjectIDs: projects}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	body := strings.Replace(createAppBody("prj-demo", ""), `"visibility":"public"`, `"visibility":"sso"`, 1)
	if created := harness.do(t, http.MethodPost, "/api/v1/applications", body); created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	received := make(chan *http.Request, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Clone(context.Background())
		_, _ = w.Write([]byte("billing says hello"))
	}))
	t.Cleanup(upstream.Close)
	object := harness.object(t, applicationsGVR, "billing")
	object.SetAnnotations(map[string]string{"kionga.dev/upstream": strings.TrimPrefix(upstream.URL, "http://")})
	if _, err := harness.dynamic.Resource(applicationsGVR).Namespace(testNamespace).Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	return &ssoHarness{kubeHarness: harness, router: ApplicationHostRouter(harness.server, repo), repo: repo, upstream: upstream, received: received}
}

func (h *ssoHarness) get(target string, principal *auth.Principal, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if principal != nil {
		request = request.WithContext(auth.WithPrincipal(request.Context(), *principal))
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func person(subject, project string) *auth.Principal {
	return &auth.Principal{Subject: subject, Roles: []string{auth.RoleUser}, Services: []string{"applications"}, ProjectIDs: []string{project}, Provisioned: true}
}

func TestApplicationSignInRedirectsAndProxies(t *testing.T) {
	h := newSSOHarness(t)

	// 1. No session on the application host: off to the console.
	first := h.get("http://billing.apps.example.com/dashboard?tab=2", nil)
	if first.Code != http.StatusFound || first.Header().Get("Location") != "https://console.example.com/applications/billing/sso?next=%2Fdashboard%3Ftab%3D2" {
		t.Fatalf("unauthenticated app request: %d %s", first.Code, first.Header().Get("Location"))
	}

	// 2. The console route sends a signed-out visitor to sign-in.
	signedOut := h.get("https://console.example.com/applications/billing/sso?next=%2Fdashboard", nil)
	if signedOut.Code != http.StatusFound || !strings.HasPrefix(signedOut.Header().Get("Location"), "/auth/login?return_to=") {
		t.Fatalf("signed-out console: %d %s", signedOut.Code, signedOut.Header().Get("Location"))
	}

	// 3. Signed in with the project: a ticket for the application host.
	handoff := h.get("https://console.example.com/applications/billing/sso?next=%2Fdashboard%3Ftab%3D2", person("alice", "prj-demo"))
	location, _ := url.Parse(handoff.Header().Get("Location"))
	if handoff.Code != http.StatusFound || location.Host != "billing.apps.example.com" || location.Path != "/_kionga/sso" || location.Query().Get("ticket") == "" {
		t.Fatalf("handoff: %d %s", handoff.Code, handoff.Header().Get("Location"))
	}

	// 4. The application host turns the ticket into its own session.
	redeemed := h.get("http://billing.apps.example.com"+location.RequestURI(), nil)
	cookies := redeemed.Result().Cookies()
	if redeemed.Code != http.StatusSeeOther || redeemed.Header().Get("Location") != "/dashboard?tab=2" || len(cookies) != 1 || cookies[0].Name != "kionga_app_session" || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("redeem: %d %s %v", redeemed.Code, redeemed.Header().Get("Location"), cookies)
	}
	if again := h.get("http://other.apps.example.com"+location.RequestURI(), nil); again.Code == http.StatusSeeOther {
		t.Fatal("a ticket must only work on its own application's host")
	}

	// 5. With the session the request reaches the application, without the
	// gateway's cookie and with the caller's subject.
	proxied := h.get("http://billing.apps.example.com/dashboard", nil, cookies[0], &http.Cookie{Name: "theirs", Value: "kept"})
	if proxied.Code != http.StatusOK || proxied.Body.String() != "billing says hello" {
		t.Fatalf("proxy: %d %s", proxied.Code, proxied.Body.String())
	}
	upstreamRequest := <-h.received
	if upstreamRequest.Header.Get("X-Kionga-Subject") != "alice" || strings.Contains(upstreamRequest.Header.Get("Cookie"), "kionga_app_session") || !strings.Contains(upstreamRequest.Header.Get("Cookie"), "theirs=kept") || upstreamRequest.URL.Path != "/dashboard" {
		t.Fatalf("upstream saw: %v %s", upstreamRequest.Header, upstreamRequest.URL)
	}

	// A spoofed subject header never reaches the application.
	spoof := httptest.NewRequest(http.MethodGet, "http://billing.apps.example.com/", nil)
	spoof.AddCookie(cookies[0])
	spoof.Header.Set("X-Kionga-Subject", "admin")
	h.router.ServeHTTP(httptest.NewRecorder(), spoof)
	if got := (<-h.received).Header.Get("X-Kionga-Subject"); got != "alice" {
		t.Fatalf("spoofed subject reached the application: %s", got)
	}

	// 6. Revoking access applies on the next request.
	if _, err := h.repo.UpsertUserAccess("alice", api.UpsertUserAccessRequest{Email: "alice@example.com", Role: "user", Services: []string{"applications"}, ProjectIDs: []string{"prj-demo"}, Disabled: true}, "admin"); err != nil {
		t.Fatal(err)
	}
	if revoked := h.get("http://billing.apps.example.com/dashboard", nil, cookies[0]); revoked.Code != http.StatusForbidden {
		t.Fatalf("suspended user: %d", revoked.Code)
	}
}

func TestApplicationSignInDeniesOtherProjects(t *testing.T) {
	h := newSSOHarness(t)
	if denied := h.get("https://console.example.com/applications/billing/sso", person("bob", "prj-other")); denied.Code != http.StatusForbidden {
		t.Fatalf("console handoff for another project: %d", denied.Code)
	}
	// Even a valid session for bob is refused at the application host.
	session := signAppToken(appToken{Kind: "session", App: "billing", Subject: "bob", Expires: time.Now().Add(time.Hour).Unix()})
	if denied := h.get("http://billing.apps.example.com/", nil, &http.Cookie{Name: "kionga_app_session", Value: session}); denied.Code != http.StatusForbidden {
		t.Fatalf("session for another project: %d", denied.Code)
	}
	select {
	case <-h.received:
		t.Fatal("a denied request reached the application")
	default:
	}
}

func TestApplicationHostsServeOnlySSOApplications(t *testing.T) {
	h := newSSOHarness(t)
	public := strings.Replace(strings.Replace(createAppBody("prj-demo", ""), `"name":"billing"`, `"name":"handbook"`, 1), `"secret_refs":["billing-db"],`, "", 1)
	if created := h.do(t, http.MethodPost, "/api/v1/applications", public); created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	for _, host := range []string{"handbook.apps.example.com", "missing.apps.example.com"} {
		if got := h.get("http://"+host+"/", nil); got.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", host, got.Code)
		}
	}
	// Other hosts reach the console untouched.
	if console := h.get("https://console.example.com/api/v1/health", nil); console.Code != http.StatusOK {
		t.Fatalf("console: %d", console.Code)
	}
	if _, ok := applicationHost("workbench-0123.apps.example.com"); ok {
		t.Fatal("workspace host families are never application hosts")
	}
}

func TestApplicationSignInForAdministratorWithoutProfile(t *testing.T) {
	h := newSSOHarness(t)
	admin := &auth.Principal{Subject: "idp-admin", Roles: []string{auth.RoleAdmin}}
	handoff := h.get("https://console.example.com/applications/billing/sso", admin)
	location, _ := url.Parse(handoff.Header().Get("Location"))
	redeemed := h.get("http://billing.apps.example.com"+location.RequestURI(), nil)
	cookies := redeemed.Result().Cookies()
	if handoff.Code != http.StatusFound || redeemed.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("admin sign-in: %d %d", handoff.Code, redeemed.Code)
	}
	if proxied := h.get("http://billing.apps.example.com/", nil, cookies[0]); proxied.Code != http.StatusOK {
		t.Fatalf("admin proxy: %d", proxied.Code)
	}
	<-h.received
	// A user role without a profile is not provisioned and gets nothing.
	if denied := h.get("https://console.example.com/applications/billing/sso", &auth.Principal{Subject: "stranger", Roles: []string{auth.RoleUser}}); denied.Code != http.StatusForbidden {
		t.Fatalf("unprovisioned user: %d", denied.Code)
	}
}

func TestAppTokens(t *testing.T) {
	now := time.Now()
	token := signAppToken(appToken{Kind: "ticket", App: "billing", Subject: "alice", Expires: now.Add(time.Minute).Unix()})
	if _, err := verifyAppToken(token, "ticket", "billing", now); err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"wrong kind": func() error { _, err := verifyAppToken(token, "session", "billing", now); return err },
		"wrong app":  func() error { _, err := verifyAppToken(token, "ticket", "docs", now); return err },
		"expired":    func() error { _, err := verifyAppToken(token, "ticket", "billing", now.Add(2*time.Minute)); return err },
		"tampered": func() error {
			_, err := verifyAppToken(token[:len(token)-2]+"xx", "ticket", "billing", now)
			return err
		},
		"not a token": func() error { _, err := verifyAppToken("nonsense", "ticket", "billing", now); return err },
	} {
		if check() == nil {
			t.Fatalf("%s token accepted", name)
		}
	}
	for next, want := range map[string]string{"/a?b=1": "/a?b=1", "//evil.test/": "/", "https://evil.test/": "/", "/_kionga/sso?ticket=x": "/", "": "/", `/\evil`: "/"} {
		if got := safeAppNext(next); got != want {
			t.Fatalf("safeAppNext(%q) = %q", next, got)
		}
	}
}
