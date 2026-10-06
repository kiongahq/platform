package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifierAndRBAC(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	exponent := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "key-1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(exponent),
		}}})
	}))
	defer jwks.Close()
	now := time.Now()
	claims := Claims{Email: "engineer@example.com", Tenant: "team-a", Roles: []string{"engineer"}, RegisteredClaims: jwt.RegisteredClaims{
		Subject: "user-1", Issuer: "https://issuer.example", Audience: []string{"mlaiops"},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now),
	}}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "key-1"
	raw, _ := token.SignedString(privateKey)
	verifier := New(Config{Issuer: "https://issuer.example", Audience: "mlaiops", JWKSURL: jwks.URL, Tenant: "team-a"})
	principal, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Email != claims.Email || !Allowed(principal, http.MethodPost, "/api/v1/models") {
		t.Fatalf("unexpected principal: %#v", principal)
	}
	if Allowed(principal, http.MethodPost, "/api/v1/connections") {
		t.Fatal("engineer must not manage connections")
	}
}

func TestTenantMismatchFails(t *testing.T) {
	principal := Principal{Roles: []string{"viewer"}}
	if Allowed(principal, http.MethodPost, "/api/v1/projects") {
		t.Fatal("viewer must be read-only")
	}
}

func TestAllowedMatrix(t *testing.T) {
	cases := []struct {
		role, method, path string
		want               bool
	}{
		{RoleAdmin, http.MethodPost, "/api/v1/connections", true},
		{RoleOperator, http.MethodPost, "/api/v1/connections", true},
		{RoleViewer, http.MethodGet, "/api/v1/models", true},
		{RoleViewer, http.MethodPost, "/api/v1/models", false},
		{RoleViewer, http.MethodPost, "/api/v1/agents/agt-1/invoke", false},
		{RoleEngineer, http.MethodPost, "/api/v1/models/mdl-1/deploy", true},
		{RoleEngineer, http.MethodPost, "/api/v1/functions", true},
		{RoleEngineer, http.MethodPost, "/api/v1/connections", false},
		{RoleService, http.MethodPost, "/api/v1/traces", true},
		{RoleService, http.MethodPost, "/api/v1/pipelines/runs/run-1/steps", true},
		{RoleService, http.MethodPost, "/api/v1/features/customer_profile/materialized", true},
		{RoleService, http.MethodPost, "/api/v1/realtime/fraud", true},
		{RoleService, http.MethodPost, "/api/v1/models", true},
		{RoleService, http.MethodPost, "/api/v1/connections", false},
		{RoleService, http.MethodGet, "/api/v1/agents", true},
	}
	for _, test := range cases {
		principal := Principal{Roles: []string{test.role}}
		if got := Allowed(principal, test.method, test.path); got != test.want {
			t.Errorf("Allowed(%s, %s %s) = %v, want %v", test.role, test.method, test.path, got, test.want)
		}
	}
}

func TestPermissionsMatchAllowed(t *testing.T) {
	engineer := Permissions(Principal{Roles: []string{RoleEngineer}})
	if !engineer["models_write"] || engineer["connections_write"] {
		t.Fatalf("engineer permissions wrong: %#v", engineer)
	}
	viewer := Permissions(Principal{Roles: []string{RoleViewer}})
	for key, allowed := range viewer {
		if allowed {
			t.Fatalf("viewer must not hold %s", key)
		}
	}
}

func TestProvisionedUserRequiresGitServiceForRepositoryBinding(t *testing.T) {
	principal := Principal{Roles: []string{RoleUser}, Provisioned: true, Services: []string{"projects"}}
	if Allowed(principal, http.MethodPut, "/api/v1/projects/prj-1/repository") {
		t.Fatal("project access alone must not grant Git repository management")
	}
	principal.Services = append(principal.Services, "git")
	if !Allowed(principal, http.MethodPut, "/api/v1/projects/prj-1/repository") {
		t.Fatal("Git service should grant repository management")
	}
	if !Permissions(principal)["git_write"] {
		t.Fatal("Git service should surface git_write permission")
	}
}

func TestProvisionedProjectUserCanReadTemplateCatalog(t *testing.T) {
	principal := Principal{Roles: []string{RoleUser}, Provisioned: true, Services: []string{"projects"}}
	for _, path := range []string{"/api/v1/project-templates", "/api/v1/project-templates/production-ml"} {
		if !Allowed(principal, http.MethodGet, path) {
			t.Fatalf("projects service should allow template catalog read at %s", path)
		}
	}
}

func TestRBACLocalRole(t *testing.T) {
	handler := RBAC(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Fatal("principal missing from context")
		}
		_ = json.NewEncoder(w).Encode(principal.Subject)
	}))

	t.Setenv("MLAIOPS_LOCAL_ROLE", "viewer")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("viewer POST should be 403, got %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("viewer GET should be 200, got %d", recorder.Code)
	}

	t.Setenv("MLAIOPS_LOCAL_ROLE", "")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("default local role must be admin, got %d", recorder.Code)
	}
}

func TestRBACInternalToken(t *testing.T) {
	t.Setenv("MLAIOPS_INTERNAL_TOKEN", "platform-secret")
	t.Setenv("MLAIOPS_LOCAL_ROLE", "viewer")
	handler := RBAC(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/traces", nil)
	request.Header.Set("Authorization", "Bearer platform-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("service token must report traces, got %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/connections", nil)
	request.Header.Set("Authorization", "Bearer platform-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("service token must not manage connections, got %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/traces", nil)
	request.Header.Set("Authorization", "Bearer wrong-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("wrong token must fall back to local viewer and be denied, got %d", recorder.Code)
	}
}
