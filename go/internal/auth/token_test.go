package auth

import (
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
