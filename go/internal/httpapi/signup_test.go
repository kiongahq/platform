package httpapi

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func newcomer(connector string) auth.Principal {
	return auth.Principal{Subject: "CgNuZXcSBmdpdGh1Yg", Email: "new@example.com", EmailVerified: true,
		Roles: []string{auth.RoleUser}, Connector: connector}
}

func mustPolicy(t *testing.T, mode, domains string) SignupPolicy {
	t.Helper()
	policy, err := ParseSignupPolicy(mode, domains, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestSignupPolicyDefaultsAndValidation(t *testing.T) {
	policy := mustPolicy(t, "", " @Example.com, ,corp.io ")
	if policy.Mode != SignupApproval || policy.Profile != "starter" {
		t.Fatalf("defaults = %q/%q, want approval/starter", policy.Mode, policy.Profile)
	}
	if !slices.Equal(policy.Domains, []string{"example.com", "corp.io"}) {
		t.Fatalf("domains = %v", policy.Domains)
	}
	if slices.Contains(policy.Services, "access") || !slices.Contains(policy.Services, "projects") {
		t.Fatalf("default services must cover building work, not administration: %v", policy.Services)
	}
	custom, err := ParseSignupPolicy("OPEN", "", "team", "projects, models")
	if err != nil || custom.Mode != SignupOpen || custom.Profile != "team" || !slices.Equal(custom.Services, []string{"projects", "models"}) {
		t.Fatalf("custom policy = %+v, %v", custom, err)
	}
	for _, bad := range [][4]string{{"anyone", "", "", ""}, {"open", "", "custom", ""}, {"open", "", "huge", ""}} {
		if _, err := ParseSignupPolicy(bad[0], bad[1], bad[2], bad[3]); err == nil {
			t.Errorf("ParseSignupPolicy%v must fail", bad)
		}
	}
}

func TestApprovalSignupFilesOneRequest(t *testing.T) {
	repository := store.New()
	policy := mustPolicy(t, "approval", "")
	outcome, err := policy.SignUp(repository, newcomer("github"))
	if err != nil || outcome != SignupRequested {
		t.Fatalf("first sign-in = %s, %v; want requested", outcome, err)
	}
	requests := repository.AccessRequestsFor("CgNuZXcSBmdpdGh1Yg")
	if len(requests) != 1 || requests[0].Status != "pending" || requests[0].Reason != "Signed up with github" || requests[0].Email != "new@example.com" {
		t.Fatalf("requests = %+v", requests)
	}
	if len(repository.UserAccess()) != 0 {
		t.Fatal("approval mode must not grant anything before review")
	}
	// Every later sign-in is a no-op, including after a rejection.
	if outcome, err := policy.SignUp(repository, newcomer("github")); err != nil || outcome != SignupSkipped {
		t.Fatalf("second sign-in = %s, %v; want skipped", outcome, err)
	}
	if _, err := repository.ReviewAccessRequest(requests[0].ID, api.ReviewAccessRequest{Status: "rejected"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if outcome, _ := policy.SignUp(repository, newcomer("github")); outcome != SignupSkipped || len(repository.AccessRequestsFor("CgNuZXcSBmdpdGh1Yg")) != 1 {
		t.Fatalf("a rejected person signing in again must not re-file: %s", outcome)
	}
}

func TestOpenSignupProvisionsAStarterUser(t *testing.T) {
	repository := store.New()
	outcome, err := mustPolicy(t, "open", "").SignUp(repository, newcomer("google"))
	if err != nil || outcome != SignupProvisioned {
		t.Fatalf("open sign-up = %s, %v", outcome, err)
	}
	access, err := repository.AccessFor("CgNuZXcSBmdpdGh1Yg")
	if err != nil {
		t.Fatal(err)
	}
	if access.Role != auth.RoleUser || access.Disabled || access.Compute.Profile != "starter" || access.Compute.VCPUs == 0 {
		t.Fatalf("provisioned access = %+v", access)
	}
	if !slices.Equal(access.Services, DefaultSignupServices) {
		t.Fatalf("services = %v", access.Services)
	}
}

func TestSignupNeverOverridesExistingAccess(t *testing.T) {
	repository := store.New()
	// A suspended account must stay suspended when the person signs in again.
	if _, err := repository.UpsertUserAccess("CgNuZXcSBmdpdGh1Yg", api.UpsertUserAccessRequest{Email: "new@example.com", Role: auth.RoleUser, Disabled: true}, "admin"); err != nil {
		t.Fatal(err)
	}
	if outcome, _ := mustPolicy(t, "open", "").SignUp(repository, newcomer("github")); outcome != SignupSkipped {
		t.Fatalf("existing profile: %s, want skipped", outcome)
	}
	if access, _ := repository.AccessFor("CgNuZXcSBmdpdGh1Yg"); !access.Disabled {
		t.Fatal("sign-up re-enabled a suspended account")
	}
}

func TestSignupSkipsDirectoryRolesAndOffMode(t *testing.T) {
	repository := store.New()
	admin := newcomer("ldap")
	admin.Roles = []string{auth.RoleAdmin}
	for name, run := range map[string]func() (SignupOutcome, error){
		"directory admin": func() (SignupOutcome, error) { return mustPolicy(t, "open", "").SignUp(repository, admin) },
		"mode off":        func() (SignupOutcome, error) { return mustPolicy(t, "off", "").SignUp(repository, newcomer("github")) },
		"no subject":      func() (SignupOutcome, error) { return mustPolicy(t, "open", "").SignUp(repository, auth.Principal{}) },
	} {
		if outcome, err := run(); err != nil || outcome != SignupSkipped {
			t.Errorf("%s: %s, %v; want skipped", name, outcome, err)
		}
	}
	if len(repository.UserAccess()) != 0 || len(repository.AccessRequests()) != 0 {
		t.Fatal("skipped sign-ups must not write anything")
	}
}

func TestSignupDomainAllowlistNeedsAVerifiedEmail(t *testing.T) {
	policy := mustPolicy(t, "open", "example.com")
	cases := []struct {
		email    string
		verified bool
		want     SignupOutcome
	}{
		{"new@example.com", true, SignupProvisioned},
		{"New@EXAMPLE.com", true, SignupProvisioned},
		{"new@example.com", false, SignupNotAllowed},     // unverified claims a company domain
		{"new@evil-example.com", true, SignupNotAllowed}, // suffix is not a match
		{"new@example.com.evil.io", true, SignupNotAllowed},
		{"", true, SignupNotAllowed},
	}
	for _, c := range cases {
		principal := newcomer("github")
		principal.Email, principal.EmailVerified = c.email, c.verified
		if outcome, _ := policy.SignUp(store.New(), principal); outcome != c.want {
			t.Errorf("%q verified=%v: %s, want %s", c.email, c.verified, outcome, c.want)
		}
	}
}

func TestOnLoginHookAndMeReportPendingSignup(t *testing.T) {
	repository := store.New()
	mustPolicy(t, "approval", "").OnLogin(repository)(context.Background(), newcomer("github"))
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	server := New(repository, static)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), newcomer("github")))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/me = %d: %s", response.Code, response.Body.String())
	}
	var me struct {
		Connector     string `json:"connector"`
		Provisioned   bool   `json:"provisioned"`
		AccessRequest *struct {
			Status string `json:"status"`
		} `json:"access_request"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Connector != "github" || me.Provisioned || me.AccessRequest == nil || me.AccessRequest.Status != "pending" {
		t.Fatalf("/me = %s", response.Body.String())
	}
	// Unprovisioned, the newcomer still cannot use any service.
	projects := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	server.ServeHTTP(projects, request.WithContext(auth.WithPrincipal(request.Context(), newcomer("github"))))
	if projects.Code != http.StatusForbidden {
		t.Fatalf("pending newcomer GET /api/v1/projects = %d, want 403", projects.Code)
	}
}

func TestMeReportsHowThisCallerSignedInNextToSingleSignOn(t *testing.T) {
	t.Setenv("OIDC_ISSUER", "http://localhost:5556/dex")
	t.Setenv("KIONGA_LOCAL_LOGIN", "true")
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	server := New(store.New(), static)
	for credential, want := range map[string]string{auth.CredentialLocalSession: "local", auth.CredentialSSOSession: "oidc", auth.CredentialOIDCToken: "oidc"} {
		principal := auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}, Credential: credential}
		request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request.WithContext(auth.WithPrincipal(request.Context(), principal)))
		var me struct {
			Mode          string `json:"mode"`
			LocalAccounts bool   `json:"local_accounts"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &me)
		if me.Mode != want || !me.LocalAccounts {
			t.Errorf("%s: mode=%q local_accounts=%v, want %q and true", credential, me.Mode, me.LocalAccounts, want)
		}
	}
}

func TestLocalBootstrapAdminNextToSingleSignOn(t *testing.T) {
	local := auth.Principal{Subject: "admin", Credential: auth.CredentialLocalSession}
	ssoNamedAdmin := auth.Principal{Subject: "admin", Credential: auth.CredentialSSOSession}
	tokenNamedAdmin := auth.Principal{Subject: "admin", Credential: auth.CredentialOIDCToken}
	cases := []struct {
		issuer, localLogin string
		principal          auth.Principal
		want               bool
	}{
		{"", "", local, true},                           // local accounts only
		{"https://dex", "true", local, true},            // break-glass next to SSO
		{"https://dex", "", local, false},               // SSO only: no local accounts
		{"https://dex", "true", ssoNamedAdmin, false},   // an IdP subject that collides
		{"https://dex", "true", tokenNamedAdmin, false}, // likewise for API tokens
		{"https://dex", "true", auth.Principal{Subject: "grace", Credential: auth.CredentialLocalSession}, false},
	}
	for _, c := range cases {
		t.Setenv("OIDC_ISSUER", c.issuer)
		t.Setenv("KIONGA_LOCAL_LOGIN", c.localLogin)
		if got := localBootstrapAdmin(c.principal); got != c.want {
			t.Errorf("issuer=%q local=%q %+v: %v, want %v", c.issuer, c.localLogin, c.principal, got, c.want)
		}
	}
}

func TestAdminsIssueLocalPasswordsOnlyWhileLocalAccountsAreOn(t *testing.T) {
	repository := store.New()
	if _, err := repository.UpsertUserAccess("e2e-user", api.UpsertUserAccessRequest{Email: "e2e@example.com", Role: auth.RoleUser, Services: []string{"projects"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	server := New(repository, static)
	issue := func() int {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/e2e-user/local-password", strings.NewReader(`{"password":"a-long-local-password"}`))
		admin := auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}, Credential: auth.CredentialLocalSession}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request.WithContext(auth.WithPrincipal(request.Context(), admin)))
		return response.Code
	}
	t.Setenv("OIDC_ISSUER", "https://dex")
	t.Setenv("KIONGA_LOCAL_LOGIN", "true")
	if code := issue(); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("break-glass next to SSO: local password = %d, want success", code)
	}
	t.Setenv("KIONGA_LOCAL_LOGIN", "")
	if code := issue(); code != http.StatusConflict {
		t.Fatalf("SSO only: local password = %d, want 409", code)
	}
}
