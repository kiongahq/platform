package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

func TestAPITokenMiddlewareCreatesScopedPrincipal(t *testing.T) {
	resolver := func(secret string) (api.APIToken, error) {
		if secret != "nxs_test_secret" {
			return api.APIToken{}, errors.New("invalid")
		}
		return api.APIToken{Subject: "user-1", Services: []string{"projects"}, ProjectIDs: []string{"prj-one"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	handler := APITokenMiddleware(resolver, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok || principal.Credential != "api_token" || principal.Subject != "user-1" {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.Header.Set("Authorization", "Bearer nxs_test_secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
}

// A token whose owner is suspended is refused by RBAC even before the store
// marks the token revoked.
func TestRBACRefusesTokenOfSuspendedOwnerAndNarrowsServices(t *testing.T) {
	disabled := false
	resolver := func(string) ([]string, []string, []string, bool, bool) {
		return []string{RoleUser}, []string{"projects"}, nil, disabled, true
	}
	var seen Principal
	handler := RBACWithResolver(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
	}), resolver)
	token := Principal{Subject: "alice", Roles: []string{RoleUser}, Services: []string{"projects", "pipelines"}, Provisioned: true, Credential: "api_token"}
	serve := func(path string) int {
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(WithPrincipal(context.Background(), token))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if code := serve("/api/v1/projects"); code != http.StatusOK || len(seen.Services) != 1 || seen.Services[0] != "projects" {
		t.Fatalf("token services must be narrowed to the owner's: %d %v", code, seen.Services)
	}
	if code := serve("/api/v1/pipelines/runs"); code != http.StatusForbidden {
		t.Fatalf("service the owner lost = %d, want 403", code)
	}
	disabled = true
	if code := serve("/api/v1/projects"); code != http.StatusForbidden {
		t.Fatalf("suspended owner's token = %d, want 403", code)
	}
}
