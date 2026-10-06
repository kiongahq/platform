package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

type testEdgeRepository struct {
	store.Repository
	identity store.WorkspaceEdgeIdentity
	access   api.UserAccess
	redeemed bool
	revoked  bool
}

func (r *testEdgeRepository) AccessFor(subject string) (api.UserAccess, error) {
	if subject != r.access.Subject {
		return api.UserAccess{}, errors.New("unknown user")
	}
	return r.access, nil
}
func (r *testEdgeRepository) RedeemWorkspaceTicket(_ context.Context, token string) (store.WorkspaceEdgeIdentity, string, error) {
	if token != "one-use" || r.redeemed {
		return store.WorkspaceEdgeIdentity{}, "", errors.New("invalid ticket")
	}
	r.redeemed = true
	return r.identity, "session", nil
}
func (r *testEdgeRepository) WorkspaceEdgeSession(_ context.Context, token string) (store.WorkspaceEdgeIdentity, error) {
	if token != "session" || r.revoked {
		return store.WorkspaceEdgeIdentity{}, errors.New("invalid session")
	}
	return r.identity, nil
}
func (r *testEdgeRepository) RevokeWorkspaceEdgeSession(_ context.Context, token string) error {
	r.revoked = true
	return nil
}

func TestWorkspaceHostIsolationAndRevocation(t *testing.T) {
	t.Setenv("MLAIOPS_ALLOWED_ORIGIN", "https://console.example.test")
	t.Setenv("KIONGA_WORKSPACE_BASE_DOMAIN", "workspaces.example.test")
	t.Setenv("KIONGA_ENVIRONMENT", "production")
	repo := &testEdgeRepository{
		identity: store.WorkspaceEdgeIdentity{Subject: "alice", WorkspaceName: "ws-alice", Kind: "workbench"},
		access:   api.UserAccess{Subject: "alice", Role: "user", Services: []string{"workbench"}},
	}
	platformCalls := 0
	handler := WorkspaceHostRouter(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { platformCalls++; w.WriteHeader(200) }), repo)
	workspaceHost := "workbench-" + workspaceSlug("ws-alice") + ".workspaces.example.test"
	otherHost := "workbench-" + workspaceSlug("ws-other") + ".workspaces.example.test"
	request := func(host, path, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: edgeCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	redeem := func(next string) *httptest.ResponseRecorder {
		body := url.Values{"ticket": {"one-use"}, "next": {next}}.Encode()
		r := httptest.NewRequest(http.MethodPost, "https://"+workspaceHost+"/_kionga/redeem", strings.NewReader(body))
		r.Header.Set("Origin", "https://console.example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("evil.example.test", "/", ""); w.Code != 404 {
		t.Fatalf("unknown host: %d", w.Code)
	}
	if w := request("console.example.test", "/", ""); w.Code != 200 || platformCalls != 1 {
		t.Fatal("platform host not routed")
	}
	if w := request(workspaceHost, "/api/v1/me", ""); w.Code != 401 {
		t.Fatalf("edge exposed API: %d", w.Code)
	}
	if w := redeem("https://evil.test"); w.Code != 400 || repo.redeemed {
		t.Fatal("unsafe redirect consumed ticket")
	}
	w := redeem("/workspaces/workbench/lab")
	if w.Code != 303 || len(w.Result().Cookies()) == 0 || w.Result().Cookies()[0].Domain != "" {
		t.Fatalf("handoff: %d", w.Code)
	}
	if w := redeem("/workspaces/workbench/lab"); w.Code != 401 {
		t.Fatal("ticket replay accepted")
	}
	if w := request(otherHost, "/workspaces/workbench/lab", "session"); w.Code != 401 {
		t.Fatal("cross-workspace session accepted")
	}
	repo.access.Disabled = true
	if w := request(workspaceHost, "/workspaces/workbench/lab", "session"); w.Code != 403 || !repo.revoked {
		t.Fatal("revoked user retained access")
	}
}

func TestWorkspaceHostAndRedirectValidation(t *testing.T) {
	if kind, slug, ok := parseWorkspaceHost("workbench-"+workspaceSlug("workspace-with-a-very-long-oidc-subject")+".workspaces.example.test", "workspaces.example.test"); !ok || kind != "workbench" || slug != workspaceSlug("workspace-with-a-very-long-oidc-subject") {
		t.Fatal("valid opaque workspace hostname was rejected")
	}
	for _, host := range []string{"workbench-ws-a.workspaces.example.test.evil", "workbench-.workspaces.example.test", "ide-a.b.workspaces.example.test", strings.Repeat("x", 64) + ".workspaces.example.test"} {
		if _, _, ok := parseWorkspaceHost(host, "workspaces.example.test"); ok {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, next := range []string{"//evil.test", "/workspaces/ide/", "/workspaces/workbench/../console", "https://evil.test"} {
		if safeWorkspaceNext(next, "workbench") {
			t.Fatalf("accepted redirect %q", next)
		}
	}
}

func TestWorkspaceFramePolicyKeepsUpstreamRestrictions(t *testing.T) {
	header := http.Header{}
	header.Set("X-Frame-Options", "SAMEORIGIN")
	header.Add("Content-Security-Policy", "default-src 'self'; frame-ancestors 'self'; script-src 'self'")
	rewriteWorkspaceFramePolicy(header, "https://console.example.test")
	if header.Get("X-Frame-Options") != "" || header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("frame policy headers were not rewritten")
	}
	policy := header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "default-src 'self'") || !strings.Contains(policy, "script-src 'self'") || !strings.Contains(policy, "frame-ancestors https://console.example.test") || strings.Contains(policy, "frame-ancestors 'self'") {
		t.Fatalf("unsafe workspace CSP: %s", policy)
	}
}
