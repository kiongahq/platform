package auth

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Platform roles, most to least privileged. "service" is the machine identity
// used by in-platform services (agent runtime, pipeline flows, materializer,
// realtime processor) reporting back through the internal token.
const (
	RoleAdmin    = "admin"
	RoleUser     = "user"
	RoleOperator = "operator"
	RoleEngineer = "engineer"
	RoleViewer   = "viewer"
	RoleService  = "service"
)

type Principal struct {
	Subject     string
	Email       string
	Tenant      string
	Roles       []string
	Namespaces  []string
	Services    []string
	ProjectIDs  []string
	Disabled    bool
	Provisioned bool
	Credential  string
}

type contextKey struct{}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	value, ok := ctx.Value(contextKey{}).(Principal)
	return value, ok
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

type Claims struct {
	Email      string   `json:"email"`
	Tenant     string   `json:"tenant"`
	Roles      []string `json:"roles"`
	Groups     []string `json:"groups"`
	Namespaces []string `json:"namespaces"`
	jwt.RegisteredClaims
}

type Config struct {
	Issuer   string
	Audience string
	JWKSURL  string
	Tenant   string
}

type Verifier struct {
	config  Config
	client  *http.Client
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

func New(config Config) *Verifier {
	return &Verifier{config: config, client: &http.Client{Timeout: 5 * time.Second}, keys: make(map[string]*rsa.PublicKey)}
}

// Middleware authenticates requests: OIDC bearer tokens for people, the
// internal platform token for in-cluster services. Authorization (role
// checks) happens in RBAC, which runs on every deployment profile.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := PrincipalFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			if cookie, err := r.Cookie(SessionCookieName); err == nil {
				token = cookie.Value
			}
		}
		if token == "" {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				http.Redirect(w, r, "/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			deny(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if service, ok := servicePrincipal(token); ok {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), service)))
			return
		}
		principal, err := v.Verify(r.Context(), token)
		if err != nil {
			deny(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

func publicPath(method, path string) bool {
	if method == http.MethodGet && (path == "/api/v1/blogs" || strings.HasPrefix(path, "/api/v1/blogs/")) {
		return true
	}
	switch path {
	case "/", "/index.html", "/landing.css", "/landing.js", "/api/v1/health",
		"/api/openapi.json", "/api-docs.html", "/api-docs.css", "/api-docs.js",
		"/blogs.html", "/blog.html", "/blog.css", "/blog.js":
		return true
	}
	return strings.HasPrefix(path, "/auth/")
}

// servicePrincipal matches the shared internal token that platform services
// use to report state back to the control plane.
func servicePrincipal(bearer string) (Principal, bool) {
	token := os.Getenv("MLAIOPS_INTERNAL_TOKEN")
	if token == "" || subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) != 1 {
		return Principal{}, false
	}
	return Principal{Subject: "internal-service", Roles: []string{RoleService}}, true
}

// RBAC authorizes every API request. Identity comes from the OIDC middleware
// when it ran; otherwise the internal service token or the local development
// principal (role from MLAIOPS_LOCAL_ROLE, admin by default) applies. The
// resolved principal is placed on the context so handlers attribute actions.
func RBAC(next http.Handler) http.Handler {
	return RBACWithResolver(next, nil)
}

// AccessResolver returns the administrator-managed access profile for a
// subject. A missing profile is deny-by-default for normal users.
type AccessResolver func(string) (roles, services, projectIDs []string, disabled bool, ok bool)

func RBACWithResolver(next http.Handler, resolve AccessResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if (!strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/workspaces/")) || path == "/api/v1/health" || path == "/api/openapi.json" {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if service, matched := servicePrincipal(bearer); matched {
				principal = service
			} else {
				role := strings.TrimSpace(os.Getenv("MLAIOPS_LOCAL_ROLE"))
				if role == "" {
					role = RoleAdmin
				}
				principal = Principal{Subject: "local-dev", Roles: []string{role}}
			}
			r = r.WithContext(WithPrincipal(r.Context(), principal))
		}
		if resolve != nil && principal.Subject != "" && !hasRole(principal, RoleService) && principal.Credential != "api_token" {
			if roles, services, projectIDs, disabled, found := resolve(principal.Subject); found {
				principal.Roles, principal.Services, principal.ProjectIDs = roles, services, projectIDs
				principal.Disabled, principal.Provisioned = disabled, true
				r = r.WithContext(WithPrincipal(r.Context(), principal))
			}
		}
		if !Allowed(principal, r.Method, path) {
			deny(w, http.StatusForbidden, "access is not provisioned for this service")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (v *Verifier) Verify(ctx context.Context, raw string) (Principal, error) {
	claims := &Claims{}
	options := []jwt.ParserOption{jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithIssuer(v.config.Issuer)}
	if v.config.Audience != "" {
		options = append(options, jwt.WithAudience(v.config.Audience))
	}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, errors.New("only RS256 is accepted")
		}
		kid, _ := token.Header["kid"].(string)
		return v.key(ctx, kid)
	}, options...)
	if err != nil || !token.Valid {
		return Principal{}, errors.New("token validation failed")
	}
	if v.config.Tenant != "" && claims.Tenant != v.config.Tenant {
		return Principal{}, errors.New("tenant mismatch")
	}
	// Dex and most IdPs carry role membership in the "groups" claim; a
	// dedicated "roles" claim wins when both are present.
	roles := claims.Roles
	if len(roles) == 0 {
		roles = claims.Groups
	}
	return Principal{Subject: claims.Subject, Email: claims.Email, Tenant: claims.Tenant, Roles: roles, Namespaces: claims.Namespaces}, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, valid := v.keys[kid], time.Now().Before(v.expires)
	v.mu.RUnlock()
	if key != nil && valid {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	key = v.keys[kid]
	if key == nil {
		return nil, errors.New("signing key not found")
	}
	return key, nil
}

func (v *Verifier) refresh(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.config.JWKSURL, nil)
	if err != nil {
		return err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS returned %d", response.StatusCode)
	}
	var document struct {
		Keys []struct {
			KID string `json:"kid"`
			KTY string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.KTY != "RSA" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(item.N)
		if err != nil {
			continue
		}
		exponent, err := base64.RawURLEncoding.DecodeString(item.E)
		if err != nil {
			continue
		}
		e := 0
		for _, value := range exponent {
			e = e<<8 + int(value)
		}
		keys[item.KID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	if len(keys) == 0 {
		return errors.New("JWKS contained no usable RSA keys")
	}
	v.mu.Lock()
	v.keys, v.expires = keys, time.Now().Add(10*time.Minute)
	v.mu.Unlock()
	return nil
}

// engineerWrite lists what the engineer role may mutate: the ML lifecycle,
// but not platform configuration (connections stay admin/operator-only).
var engineerWrite = []string{
	"/api/v1/projects", "/api/v1/pipelines", "/api/v1/models", "/api/v1/agents",
	"/api/v1/tools", "/api/v1/features", "/api/v1/traces", "/api/v1/functions",
	"/api/v1/realtime",
}

// serviceWrite lists the reporting paths in-platform services need: session
// traces, pipeline step transitions, materialization reports and realtime
// stream statistics.
var serviceWrite = []string{
	"/api/v1/traces", "/api/v1/pipelines/runs", "/api/v1/features", "/api/v1/realtime",
}

var userWrite = []string{
	"/api/v1/projects", "/api/v1/pipelines", "/api/v1/models", "/api/v1/agents",
	"/api/v1/tools", "/api/v1/features", "/api/v1/traces", "/api/v1/functions",
	"/api/v1/realtime",
}

func Allowed(principal Principal, method, path string) bool {
	if principal.Disabled {
		return false
	}
	if strings.HasPrefix(path, "/workspaces/") || (strings.HasPrefix(path, "/api/v1/workspaces/") && strings.HasSuffix(path, "/launch")) {
		if principal.Credential == "api_token" || hasRole(principal, RoleService) || hasRole(principal, RoleViewer) {
			return false
		}
		if hasRole(principal, RoleUser) {
			return principal.Provisioned && hasService(principal.Services, serviceForPath(path))
		}
		return hasRole(principal, RoleAdmin) || hasRole(principal, RoleOperator) || hasRole(principal, RoleEngineer)
	}
	if strings.HasPrefix(path, "/api/v1/admin/") {
		return hasRole(principal, RoleAdmin) || hasRole(principal, RoleOperator)
	}
	if method == http.MethodGet && (path == "/api/v1/blogs" || strings.HasPrefix(path, "/api/v1/blogs/")) {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/settings/") {
		return !hasRole(principal, RoleService) && principal.Credential != "api_token" && len(principal.Roles) > 0
	}
	if path == "/api/v1/access-requests" && (method == http.MethodGet || method == http.MethodPost) {
		return !hasRole(principal, RoleService) && len(principal.Roles) > 0
	}
	read := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	for _, role := range principal.Roles {
		switch role {
		case RoleAdmin, RoleOperator:
			return true
		case RoleUser:
			if path == "/api/v1/me" || path == "/api/v1/workspaces" || path == "/api/v1/project-options" {
				return true
			}
			if !read && machineReportingPath(path) {
				return false
			}
			return principal.Provisioned && hasService(principal.Services, serviceForPath(path)) &&
				(read || hasAnyPrefix(path, userWrite) || strings.HasPrefix(path, "/workspaces/"))
		case RoleViewer:
			if read {
				return true
			}
		case RoleEngineer:
			if read || hasAnyPrefix(path, engineerWrite) {
				return true
			}
		case RoleService:
			if strings.HasPrefix(path, "/workspaces/") {
				return false
			}
			if method == http.MethodPost && (path == "/api/v1/models" || (strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/invoke"))) {
				return true
			}
			if read || hasAnyPrefix(path, serviceWrite) {
				return true
			}
		}
	}
	return false
}

func machineReportingPath(path string) bool {
	return path == "/api/v1/traces" ||
		(strings.HasPrefix(path, "/api/v1/pipelines/runs/") && strings.HasSuffix(path, "/steps")) ||
		(strings.HasPrefix(path, "/api/v1/features/") && strings.HasSuffix(path, "/materialized")) ||
		strings.HasPrefix(path, "/api/v1/realtime/")
}

func hasRole(principal Principal, expected string) bool {
	for _, role := range principal.Roles {
		if role == expected {
			return true
		}
	}
	return false
}

func hasService(services []string, expected string) bool {
	if expected == "" {
		return false
	}
	for _, service := range services {
		if service == expected {
			return true
		}
	}
	return false
}

func serviceForPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/workspaces/workbench/"):
		return "workbench"
	case strings.HasPrefix(path, "/workspaces/ide/"):
		return "ide"
	case strings.HasPrefix(path, "/api/v1/workspaces/workbench/"):
		return "workbench"
	case strings.HasPrefix(path, "/api/v1/workspaces/ide/"):
		return "ide"
	case path == "/api/v1/dashboard", path == "/api/v1/onboarding/readiness", path == "/api/v1/events":
		return "overview"
	case strings.HasPrefix(path, "/api/v1/project-templates"):
		return "projects"
	case strings.HasPrefix(path, "/api/v1/projects/") && strings.HasSuffix(path, "/repository"):
		return "git"
	case strings.HasPrefix(path, "/api/v1/projects"):
		return "projects"
	case strings.HasPrefix(path, "/api/v1/pipelines"):
		return "pipelines"
	case strings.HasPrefix(path, "/api/v1/models"):
		return "models"
	case strings.HasPrefix(path, "/api/v1/agents"), strings.HasPrefix(path, "/api/v1/traces"):
		return "agents"
	case strings.HasPrefix(path, "/api/v1/features"):
		return "features"
	case strings.HasPrefix(path, "/api/v1/storage"):
		return "storage"
	case strings.HasPrefix(path, "/api/v1/realtime"):
		return "realtime"
	case strings.HasPrefix(path, "/api/v1/catalog"), strings.HasPrefix(path, "/api/v1/tools"), strings.HasPrefix(path, "/api/v1/prompts"):
		return "catalog"
	case strings.HasPrefix(path, "/api/v1/components"), strings.HasPrefix(path, "/api/v1/connections"):
		return "platform"
	case strings.HasPrefix(path, "/api/v1/functions"):
		return "functions"
	}
	return ""
}

func hasAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Permissions summarizes what a principal may do, keyed the way the console
// gates its controls. Derived from Allowed so the UI can never drift from
// what the API actually enforces.
func Permissions(principal Principal) map[string]bool {
	return map[string]bool{
		"projects_write":    Allowed(principal, http.MethodPost, "/api/v1/projects"),
		"pipelines_write":   Allowed(principal, http.MethodPost, "/api/v1/pipelines/submit"),
		"models_write":      Allowed(principal, http.MethodPost, "/api/v1/models"),
		"agents_write":      Allowed(principal, http.MethodPost, "/api/v1/agents"),
		"tools_write":       Allowed(principal, http.MethodPost, "/api/v1/tools"),
		"features_write":    Allowed(principal, http.MethodPost, "/api/v1/features"),
		"functions_write":   Allowed(principal, http.MethodPost, "/api/v1/functions"),
		"git_write":         Allowed(principal, http.MethodPut, "/api/v1/projects/example/repository"),
		"connections_write": Allowed(principal, http.MethodPost, "/api/v1/connections"),
		"access_manage":     Allowed(principal, http.MethodPut, "/api/v1/admin/users/example"),
	}
}

func deny(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": http.StatusText(status), "message": message})
}
