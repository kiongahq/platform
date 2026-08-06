package auth

import (
	"net/http"
	"strings"

	"github.com/ml-ai-ops/platform/pkg/api"
)

type APITokenResolver func(string) (api.APIToken, error)

// APITokenMiddleware resolves Kionga personal API keys before OIDC/local role
// middleware. Token principals remain scope-limited and are not expanded by
// the user's interactive-session grants.
func APITokenMiddleware(resolve APITokenResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !strings.HasPrefix(bearer, "nxs_") {
			next.ServeHTTP(w, r)
			return
		}
		token, err := resolve(bearer)
		if err != nil {
			deny(w, http.StatusUnauthorized, "invalid API token")
			return
		}
		principal := Principal{
			Subject: token.Subject, Roles: []string{RoleUser}, Services: token.Services,
			ProjectIDs: token.ProjectIDs, Provisioned: true, Credential: "api_token",
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}
