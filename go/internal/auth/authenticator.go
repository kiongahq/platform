package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Provider is one sign-in option on the login page. With Dex as the broker,
// ID is the Dex connector id (LDAP, SAML, GitHub, Google, ...); an empty ID
// lets Dex show its own chooser.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// ParseProviders reads KIONGA_SSO_PROVIDERS: a JSON list of providers.
func ParseProviders(raw string) ([]Provider, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var providers []Provider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return nil, fmt.Errorf("KIONGA_SSO_PROVIDERS is not a JSON list of {id, name, type}: %w", err)
	}
	return providers, nil
}

// ParseGroupRoles reads KIONGA_SSO_GROUP_ROLES: a JSON object mapping
// identity-provider group names to platform roles.
func ParseGroupRoles(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var mapping map[string]string
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return nil, fmt.Errorf("KIONGA_SSO_GROUP_ROLES is not a JSON object of group -> role: %w", err)
	}
	for group, role := range mapping {
		if !knownRole(role) {
			return nil, fmt.Errorf("KIONGA_SSO_GROUP_ROLES maps %q to unknown role %q", group, role)
		}
	}
	return mapping, nil
}

// Authenticator is the single entry point for people signing in: single
// sign-on through an OIDC broker, local accounts, or both (local accounts as
// break-glass access next to SSO). It owns /auth/* and attaches the
// principal for everything else; RBAC authorizes afterwards.
type Authenticator struct {
	sso      *SessionManager
	verifier *Verifier
	local    *LocalSessionManager

	mu            sync.RWMutex
	providers     []Provider
	providersFile string
	providersMod  time.Time
}

func NewAuthenticator(sso *SessionManager, verifier *Verifier, local *LocalSessionManager, providers []Provider) (*Authenticator, error) {
	if sso == nil && local == nil {
		return nil, errors.New("enable single sign-on, local accounts, or both")
	}
	if sso != nil && verifier == nil {
		return nil, errors.New("single sign-on needs a token verifier")
	}
	providers, err := checkProviders(sso, providers)
	if err != nil {
		return nil, err
	}
	return &Authenticator{sso: sso, verifier: verifier, local: local, providers: providers}, nil
}

// UseProvidersFile makes the provider list follow a file the deployment
// writes (the local stack's Dex derives it from its rendered connectors), so
// enabling a connector needs no gateway restart. The file is re-read when
// its modification time changes; a file that no longer parses keeps the last
// good list and is logged.
func (a *Authenticator) UseProvidersFile(path string) error {
	a.providersFile = path
	return a.reloadProviders(true)
}

func (a *Authenticator) reloadProviders(force bool) error {
	info, err := os.Stat(a.providersFile)
	if err != nil {
		return fmt.Errorf("sign-in providers file: %w", err)
	}
	a.mu.RLock()
	unchanged := info.ModTime().Equal(a.providersMod)
	a.mu.RUnlock()
	if unchanged && !force {
		return nil
	}
	raw, err := os.ReadFile(a.providersFile)
	if err != nil {
		return fmt.Errorf("sign-in providers file: %w", err)
	}
	parsed, err := ParseProviders(string(raw))
	if err == nil {
		parsed, err = checkProviders(a.sso, parsed)
	}
	if err != nil {
		return fmt.Errorf("sign-in providers file %s: %w", a.providersFile, err)
	}
	a.mu.Lock()
	a.providers, a.providersMod = parsed, info.ModTime()
	a.mu.Unlock()
	return nil
}

// ProviderIDs lists the sign-in options the login page offers now.
func (a *Authenticator) ProviderIDs() []string {
	ids := []string{}
	for _, provider := range a.currentProviders() {
		ids = append(ids, provider.ID)
	}
	return ids
}

func (a *Authenticator) currentProviders() []Provider {
	if a.providersFile != "" {
		if err := a.reloadProviders(false); err != nil {
			log.Printf("keeping the previous sign-in providers: %v", err)
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.providers
}

// checkProviders validates a provider list and fills the defaults the login
// page relies on.
func checkProviders(sso *SessionManager, providers []Provider) ([]Provider, error) {
	if sso == nil && len(providers) > 0 {
		return nil, errors.New("KIONGA_SSO_PROVIDERS is set but single sign-on (OIDC_ISSUER) is not configured")
	}
	seen := map[string]bool{}
	for _, provider := range providers {
		if !providerIDPattern.MatchString(provider.ID) || strings.TrimSpace(provider.Name) == "" {
			return nil, fmt.Errorf("invalid sign-in provider %+v: id must match %s and name is required", provider, providerIDPattern)
		}
		if seen[provider.ID] {
			return nil, fmt.Errorf("duplicate sign-in provider id %q", provider.ID)
		}
		seen[provider.ID] = true
	}
	if sso != nil && len(providers) == 0 {
		providers = []Provider{{ID: "", Name: "Single sign-on", Type: "oidc"}}
	}
	if providers == nil {
		providers = []Provider{} // serialize as [], not null
	}
	return providers, nil
}

type providersResponse struct {
	Local     bool       `json:"local"`
	Providers []Provider `json:"providers"`
}

func (a *Authenticator) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/providers":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(providersResponse{Local: a.local != nil, Providers: a.currentProviders()})
			return
		case "/auth/login":
			if providers := a.currentProviders(); a.local == nil && len(providers) == 1 {
				a.sso.loginWith(w, r, providers[0].ID)
				return
			}
			http.Redirect(w, r, "/login.html?return_to="+url.QueryEscape(safeReturnTo(r.URL.Query().Get("return_to"))), http.StatusFound)
			return
		case "/auth/sso/login":
			if a.sso == nil {
				http.NotFound(w, r)
				return
			}
			connector := r.URL.Query().Get("connector")
			if !a.knownProvider(connector) {
				deny(w, http.StatusBadRequest, "unknown sign-in provider")
				return
			}
			a.sso.loginWith(w, r, connector)
			return
		case "/auth/callback":
			if a.sso == nil {
				http.NotFound(w, r)
				return
			}
			a.sso.callback(w, r)
			return
		case "/auth/local/login":
			if a.local == nil || r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			a.local.login(w, r)
			return
		case "/auth/logout":
			if a.local != nil {
				a.local.endSession(w, r)
			}
			if a.sso != nil {
				if err := a.sso.endSession(w, r); err != nil {
					deny(w, http.StatusServiceUnavailable, "could not revoke workspace sessions")
					return
				}
			}
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if publicPath(r.Method, r.URL.Path) || loginAsset(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := PrincipalFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := a.identify(r)
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				deny(w, http.StatusUnauthorized, "Sign in to continue.")
				return
			}
			http.Redirect(w, r, "/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

// Principal.Credential values set by the authenticator (API tokens use
// "api_token", set by APITokenMiddleware).
const (
	CredentialLocalSession = "local_session"
	CredentialSSOSession   = "sso_session"
	CredentialOIDCToken    = "oidc_token"
)

// identify resolves who is calling: a bearer token (internal service token,
// or an OIDC token for API clients), then a local session, then an SSO session.
// Only the Bearer scheme is a Kionga credential: other schemes, such as the
// "token" header Jupyter's front end sends through the workspace proxy, are
// ignored so the browser session decides. An invalid Bearer token fails.
func (a *Authenticator) identify(r *http.Request) (Principal, bool) {
	if bearer, ok := bearerToken(r); ok {
		if service, ok := servicePrincipal(bearer); ok {
			return service, true
		}
		if a.verifier != nil {
			if principal, err := a.verifier.Verify(r.Context(), bearer); err == nil {
				principal.Credential = CredentialOIDCToken
				return principal, true
			}
		}
		return Principal{}, false
	}
	if a.local != nil {
		if subject, ok := a.local.authenticated(r); ok {
			principal := a.local.principalFor(subject)
			principal.Credential = CredentialLocalSession
			return principal, true
		}
	}
	if a.verifier != nil {
		if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
			if principal, err := a.verifier.Verify(r.Context(), cookie.Value); err == nil {
				principal.Credential = CredentialSSOSession
				return principal, true
			}
		}
	}
	return Principal{}, false
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func (a *Authenticator) knownProvider(id string) bool {
	for _, provider := range a.currentProviders() {
		if provider.ID == id {
			return true
		}
	}
	return false
}

// loginAsset is the sign-in page and its assets, which must load before anyone
// is signed in.
func loginAsset(path string) bool {
	switch path {
	case "/login.html", "/login.css", "/login.js":
		return true
	}
	return false
}
