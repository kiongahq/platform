package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is an OIDC broker (Dex stand-in): JWKS plus a token endpoint that
// checks PKCE and returns an ID token for the user it was told to log in.
type fakeIdP struct {
	server     *httptest.Server
	key        *rsa.PrivateKey
	mu         sync.Mutex
	challenge  string // from the authorization request
	nonce      string // from the authorization request
	badNonce   bool   // return a token with the wrong nonce
	groups     []string
	pkceFailed bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, groups: []string{"ml-admins"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.mu.Lock()
		defer idp.mu.Unlock()
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != idp.challenge {
			idp.pkceFailed = true
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		nonce := idp.nonce
		if idp.badNonce {
			nonce = "someone-else"
		}
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, Claims{
			Email: "ada@example.com", Groups: idp.groups, Nonce: nonce,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject: "CgNhZGESBGxkYXA", Issuer: idp.server.URL, Audience: []string{"kionga"},
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now),
			},
		})
		token.Header["kid"] = "k1"
		raw, _ := token.SignedString(key)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw, "expires_in": 3600})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

type authSetup struct {
	idp     *fakeIdP
	handler http.Handler
	seen    *Principal
}

func newAuthSetup(t *testing.T, withLocal bool, providers []Provider) *authSetup {
	t.Helper()
	idp := newFakeIdP(t)
	verifier := New(Config{Issuer: idp.server.URL, Audience: "kionga", JWKSURL: idp.server.URL + "/keys",
		GroupRoles: map[string]string{"ml-admins": RoleAdmin}})
	sso, err := NewSessionManager(SessionConfig{
		ClientID: "kionga", ClientSecret: "secret",
		AuthURL: idp.server.URL + "/auth", TokenURL: idp.server.URL + "/token",
		RedirectURL: "https://kionga.example/auth/callback", Secure: true,
	}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	var local *LocalSessionManager
	if withLocal {
		local = NewLocalSessionManager("admin", "break-glass-password")
	}
	authenticator, err := NewAuthenticator(sso, verifier, local, providers)
	if err != nil {
		t.Fatal(err)
	}
	setup := &authSetup{idp: idp}
	setup.handler = authenticator.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal, ok := PrincipalFrom(r.Context()); ok {
			setup.seen = &principal
		}
		w.WriteHeader(http.StatusOK)
	}))
	return setup
}

func (s *authSetup) do(method, target string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	var request *http.Request
	if form != nil {
		request = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		request = httptest.NewRequest(method, target, nil)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	s.seen = nil
	s.handler.ServeHTTP(response, request)
	return response
}

// ssoLogin runs /auth/sso/login and the callback; it returns the callback
// response and the cookies the browser would hold afterwards.
func (s *authSetup) ssoLogin(t *testing.T, connector string) (*httptest.ResponseRecorder, []*http.Cookie) {
	t.Helper()
	start := s.do(http.MethodGet, "/auth/sso/login?connector="+connector+"&return_to=%2Fconsole.html%3Fview%3Dmodels", nil, nil)
	if start.Code != http.StatusFound {
		t.Fatalf("sso login returned %d: %s", start.Code, start.Body)
	}
	location, _ := url.Parse(start.Header().Get("Location"))
	query := location.Query()
	if query.Get("connector_id") != connector {
		t.Fatalf("connector_id = %q, want %q (%s)", query.Get("connector_id"), connector, location)
	}
	s.idp.mu.Lock()
	s.idp.challenge, s.idp.nonce = query.Get("code_challenge"), query.Get("nonce")
	s.idp.mu.Unlock()
	cookies := start.Result().Cookies()
	callback := s.do(http.MethodGet, "/auth/callback?code=abc&state="+url.QueryEscape(query.Get("state")), cookies, nil)
	return callback, append(cookies, callback.Result().Cookies()...)
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	var found *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == name {
			found = cookie // last write wins, like a browser
		}
	}
	return found
}

func TestSSOLoginThroughConnectorUsesPKCENonceAndMapsGroupsToRoles(t *testing.T) {
	setup := newAuthSetup(t, false, []Provider{{ID: "ldap", Name: "Company directory", Type: "ldap"}})
	callback, cookies := setup.ssoLogin(t, "ldap")
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/console.html?view=models" {
		t.Fatalf("callback = %d %q: %s", callback.Code, callback.Header().Get("Location"), callback.Body)
	}
	if setup.idp.pkceFailed {
		t.Fatal("token endpoint did not receive the matching PKCE verifier")
	}
	session := cookieNamed(cookies, SessionCookieName)
	if session == nil || session.Value == "" || !session.HttpOnly || !session.Secure {
		t.Fatalf("no hardened session cookie: %+v", session)
	}
	api := setup.do(http.MethodGet, "/api/v1/projects", []*http.Cookie{session}, nil)
	if api.Code != http.StatusOK || setup.seen == nil {
		t.Fatalf("API with SSO session = %d", api.Code)
	}
	if setup.seen.Subject != "CgNhZGESBGxkYXA" || len(setup.seen.Roles) != 1 || setup.seen.Roles[0] != RoleAdmin ||
		len(setup.seen.Groups) != 1 || setup.seen.Groups[0] != "ml-admins" {
		t.Fatalf("principal = %+v; want subject from IdP, groups kept, ml-admins mapped to admin", setup.seen)
	}
}

func TestSSOCallbackRejectsATokenIssuedForAnotherLogin(t *testing.T) {
	setup := newAuthSetup(t, false, []Provider{{ID: "ldap", Name: "Directory"}})
	setup.idp.badNonce = true
	callback, cookies := setup.ssoLogin(t, "ldap")
	if callback.Code != http.StatusUnauthorized {
		t.Fatalf("nonce mismatch must be rejected, got %d", callback.Code)
	}
	if session := cookieNamed(cookies, SessionCookieName); session != nil && session.Value != "" {
		t.Fatal("no session may be created for a mismatched nonce")
	}
}

func TestSSOCallbackRequiresThePKCEVerifierCookie(t *testing.T) {
	setup := newAuthSetup(t, false, nil)
	start := setup.do(http.MethodGet, "/auth/sso/login?connector=", nil, nil)
	location, _ := url.Parse(start.Header().Get("Location"))
	var withoutVerifier []*http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name != verifierCookie {
			withoutVerifier = append(withoutVerifier, cookie)
		}
	}
	callback := setup.do(http.MethodGet, "/auth/callback?code=abc&state="+url.QueryEscape(location.Query().Get("state")), withoutVerifier, nil)
	if callback.Code != http.StatusBadRequest {
		t.Fatalf("callback without PKCE verifier = %d, want 400", callback.Code)
	}
}

func TestUnknownConnectorIsRejectedBeforeRedirecting(t *testing.T) {
	setup := newAuthSetup(t, true, []Provider{{ID: "github", Name: "GitHub"}})
	response := setup.do(http.MethodGet, "/auth/sso/login?connector=evil", nil, nil)
	if response.Code != http.StatusBadRequest || response.Header().Get("Location") != "" {
		t.Fatalf("unknown connector = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestProvidersEndpointListsSignInOptionsOnly(t *testing.T) {
	setup := newAuthSetup(t, true, []Provider{{ID: "ldap", Name: "Directory", Type: "ldap"}, {ID: "github", Name: "GitHub", Type: "github"}})
	response := setup.do(http.MethodGet, "/auth/providers", nil, nil)
	var body providersResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK {
		t.Fatalf("providers = %d %s", response.Code, response.Body)
	}
	if !body.Local || len(body.Providers) != 2 || body.Providers[1].ID != "github" {
		t.Fatalf("providers body = %+v", body)
	}
	if strings.Contains(response.Body.String(), "secret") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("providers response leaks config or is cacheable: %s", response.Body)
	}
}

func TestLoginGoesStraightToTheIdPOnlyWhenItIsTheOnlyWayIn(t *testing.T) {
	ssoOnly := newAuthSetup(t, false, nil)
	direct := ssoOnly.do(http.MethodGet, "/auth/login?return_to=%2Fconsole.html", nil, nil)
	if location := direct.Header().Get("Location"); !strings.HasPrefix(location, ssoOnly.idp.server.URL+"/auth?") ||
		strings.Contains(location, "connector_id") {
		t.Fatalf("SSO-only login should go to the IdP chooser, got %q", location)
	}
	both := newAuthSetup(t, true, []Provider{{ID: "ldap", Name: "Directory"}})
	page := both.do(http.MethodGet, "/auth/login?return_to=%2Fconsole.html", nil, nil)
	if page.Header().Get("Location") != "/login.html?return_to=%2Fconsole.html" {
		t.Fatalf("with local accounts too, login shows the page; got %q", page.Header().Get("Location"))
	}
	asset := both.do(http.MethodGet, "/login.js", nil, nil)
	if asset.Code != http.StatusOK {
		t.Fatalf("login assets must load before sign-in, got %d", asset.Code)
	}
}

func TestLocalBreakGlassSignInWorksAlongsideSSOAndEverythingElseFailsClosed(t *testing.T) {
	setup := newAuthSetup(t, true, []Provider{{ID: "ldap", Name: "Directory"}})
	if api := setup.do(http.MethodGet, "/api/v1/projects", nil, nil); api.Code != http.StatusUnauthorized {
		t.Fatalf("API without identity = %d, want 401", api.Code)
	}
	if console := setup.do(http.MethodGet, "/console.html", nil, nil); console.Code != http.StatusFound ||
		!strings.HasPrefix(console.Header().Get("Location"), "/auth/login?") {
		t.Fatalf("console without identity = %d %q", console.Code, console.Header().Get("Location"))
	}
	if forged := setup.do(http.MethodGet, "/api/v1/projects", []*http.Cookie{{Name: SessionCookieName, Value: "not-a-jwt"}}, nil); forged.Code != http.StatusUnauthorized {
		t.Fatalf("forged SSO cookie = %d", forged.Code)
	}
	bad := setup.do(http.MethodPost, "/auth/local/login", nil, url.Values{"username": {"admin"}, "password": {"wrong"}})
	if !strings.Contains(bad.Header().Get("Location"), "error=invalid") {
		t.Fatalf("wrong password = %q", bad.Header().Get("Location"))
	}
	login := setup.do(http.MethodPost, "/auth/local/login", nil, url.Values{"username": {"admin"}, "password": {"break-glass-password"}, "return_to": {"/console.html"}})
	local := cookieNamed(login.Result().Cookies(), localSessionCookie)
	if local == nil || local.Value == "" {
		t.Fatalf("local login set no session: %d %q", login.Code, login.Header().Get("Location"))
	}
	if api := setup.do(http.MethodGet, "/api/v1/projects", []*http.Cookie{local}, nil); api.Code != http.StatusOK || setup.seen.Subject != "admin" {
		t.Fatalf("API with local session = %d %+v", api.Code, setup.seen)
	}
	_, ssoCookies := setup.ssoLogin(t, "ldap")
	session := cookieNamed(ssoCookies, SessionCookieName)
	logout := setup.do(http.MethodGet, "/auth/logout", []*http.Cookie{local, session}, nil)
	cleared := map[string]bool{}
	for _, cookie := range logout.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	if !cleared[localSessionCookie] || !cleared[SessionCookieName] {
		t.Fatalf("logout must clear both sessions, cleared %v", cleared)
	}
	if api := setup.do(http.MethodGet, "/api/v1/projects", []*http.Cookie{local}, nil); api.Code != http.StatusUnauthorized {
		t.Fatalf("local session must be revoked by logout, got %d", api.Code)
	}
}

func TestGroupsMapToRolesWithoutGrantingUnknownGroups(t *testing.T) {
	mapping := map[string]string{"ml-admins": RoleAdmin, "data-team": RoleEngineer}
	cases := []struct {
		claims Claims
		want   []string
	}{
		{Claims{Roles: []string{"viewer"}, Groups: []string{"ml-admins"}}, []string{"viewer"}}, // roles claim wins
		{Claims{Groups: []string{"data-team", "ml-admins", "data-team"}}, []string{RoleEngineer, RoleAdmin}},
		{Claims{Groups: []string{"viewer", "random-group"}}, []string{RoleViewer}}, // role-named groups still count
		{Claims{Groups: []string{"everyone", "service"}}, []string{}},              // never "service" from an IdP
	}
	for _, c := range cases {
		got := rolesFromClaims(&c.claims, mapping)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("groups %v roles %v -> %v, want %v", c.claims.Groups, c.claims.Roles, got, c.want)
		}
	}
}

func TestSSOConfigurationIsValidated(t *testing.T) {
	if _, err := ParseGroupRoles(`{"ml-admins":"superuser"}`); err == nil {
		t.Error("mapping to an unknown role must be rejected")
	}
	if _, err := ParseGroupRoles(`{"svc":"service"}`); err == nil {
		t.Error("an IdP group must never map to the internal service role")
	}
	if _, err := ParseProviders(`{"id":"x"}`); err == nil {
		t.Error("providers must be a list")
	}
	local := NewLocalSessionManager("admin", "x")
	if _, err := NewAuthenticator(nil, nil, nil, nil); err == nil {
		t.Error("at least one sign-in method is required")
	}
	if _, err := NewAuthenticator(nil, nil, local, []Provider{{ID: "ldap", Name: "x"}}); err == nil {
		t.Error("providers without SSO must be rejected")
	}
	sso := &SessionManager{}
	verifier := New(Config{})
	for _, providers := range [][]Provider{
		{{ID: "ldap", Name: "a"}, {ID: "ldap", Name: "b"}},
		{{ID: "has space", Name: "a"}},
		{{ID: "ok", Name: ""}},
	} {
		if _, err := NewAuthenticator(sso, verifier, nil, providers); err == nil {
			t.Errorf("providers %+v must be rejected", providers)
		}
	}
}

func TestSingleTenantIssuerAssignsTenantOnlyWhenTheClaimIsAbsent(t *testing.T) {
	idp := newFakeIdP(t)
	sign := func(tenant string) string {
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, Claims{Tenant: tenant, RegisteredClaims: jwt.RegisteredClaims{
			Subject: "u1", Issuer: idp.server.URL, Audience: []string{"kionga"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}})
		token.Header["kid"] = "k1"
		raw, _ := token.SignedString(idp.key)
		return raw
	}
	base := Config{Issuer: idp.server.URL, Audience: "kionga", JWKSURL: idp.server.URL + "/keys", Tenant: "local"}
	strict := New(base)
	if _, err := strict.Verify(t.Context(), sign("")); err == nil {
		t.Fatal("without SingleTenantIssuer a token lacking a tenant claim must be rejected")
	}
	single := base
	single.SingleTenantIssuer = true
	verifier := New(single)
	principal, err := verifier.Verify(t.Context(), sign(""))
	if err != nil || principal.Tenant != "local" {
		t.Fatalf("single-tenant issuer: %+v %v", principal, err)
	}
	if _, err := verifier.Verify(t.Context(), sign("other")); err == nil {
		t.Fatal("a token naming another tenant must still be rejected")
	}
}

// Regression: Jupyter's front end sends "Authorization: token <jupyter token>"
// through the workspace proxy. That is not a Kionga credential, so the browser
// session must decide; before the fix such requests were sent to the login page.
func TestNonBearerAuthorizationFallsBackToTheSessionButBadBearerFails(t *testing.T) {
	setup := newAuthSetup(t, true, []Provider{{ID: "ldap", Name: "Directory"}})
	login := setup.do(http.MethodPost, "/auth/local/login", nil, url.Values{"username": {"admin"}, "password": {"break-glass-password"}})
	local := cookieNamed(login.Result().Cookies(), localSessionCookie)
	request := httptest.NewRequest(http.MethodGet, "/workspaces/workbench/api/contents", nil)
	request.Header.Set("Authorization", "token jupyter-generated-token")
	request.AddCookie(local)
	response := httptest.NewRecorder()
	setup.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || setup.seen == nil || setup.seen.Subject != "admin" {
		t.Fatalf("token-scheme header with a session = %d %q", response.Code, response.Header().Get("Location"))
	}
	bad := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	bad.Header.Set("Authorization", "Bearer not-a-valid-token")
	bad.AddCookie(local)
	denied := httptest.NewRecorder()
	setup.handler.ServeHTTP(denied, bad)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("an invalid Bearer token must not fall back to the session, got %d", denied.Code)
	}
}
