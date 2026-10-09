package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const localSessionCookie = "kionga_local_session"

// LocalAccounts verifies administrator-provisioned password logins. The
// bootstrap administrator from MLAIOPS_LOCAL_USERNAME/PASSWORD always works;
// other subjects are normal users whose role and grants come from UserAccess.
type LocalAccounts interface {
	VerifyLocalAccount(subject, password string) bool
	LocalAccountActive(subject string) bool
}

type localSession struct {
	subject string
	expires time.Time
}

type LocalSessionManager struct {
	username string
	password string
	accounts LocalAccounts
	mu       sync.RWMutex
	sessions map[string]localSession
}

func NewLocalSessionManager(username, password string, accounts ...LocalAccounts) *LocalSessionManager {
	manager := &LocalSessionManager{username: username, password: password, sessions: map[string]localSession{}}
	if len(accounts) > 0 {
		manager.accounts = accounts[0]
	}
	return manager
}

// principalFor maps a session subject to its starting principal. The RBAC
// resolver later overlays the administrator-managed UserAccess profile.
func (s *LocalSessionManager) principalFor(subject string) Principal {
	if subject == s.username {
		role := os.Getenv("MLAIOPS_LOCAL_ROLE")
		if role == "" {
			role = RoleAdmin
		}
		return Principal{Subject: subject, Roles: []string{role}}
	}
	return Principal{Subject: subject, Roles: []string{RoleUser}}
}

func (s *LocalSessionManager) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/login":
			http.Redirect(w, r, "/login.html?return_to="+url.QueryEscape(safeReturnTo(r.URL.Query().Get("return_to"))), http.StatusFound)
			return
		case r.URL.Path == "/auth/local/login" && r.Method == http.MethodPost:
			s.login(w, r)
			return
		case r.URL.Path == "/auth/logout":
			s.logout(w, r)
			return
		case strings.HasPrefix(r.URL.Path, "/api/") && !publicPath(r.Method, r.URL.Path):
			if _, ok := PrincipalFrom(r.Context()); !ok {
				if service, matched := servicePrincipal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); matched {
					r = r.WithContext(WithPrincipal(r.Context(), service))
				} else if subject, ok := s.authenticated(r); !ok {
					deny(w, http.StatusUnauthorized, "Sign in to continue.")
					return
				} else {
					r = r.WithContext(WithPrincipal(r.Context(), s.principalFor(subject)))
				}
			}
		case localConsolePath(r.URL.Path):
			if _, ok := s.authenticated(r); !ok {
				http.Redirect(w, r, "/login.html?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
		}
		if subject, ok := s.authenticated(r); ok {
			if _, ok := PrincipalFrom(r.Context()); !ok {
				r = r.WithContext(WithPrincipal(r.Context(), s.principalFor(subject)))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *LocalSessionManager) verify(username, password string) bool {
	bootstrap := subtle.ConstantTimeCompare([]byte(username), []byte(s.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(s.password)) == 1
	if bootstrap {
		return true
	}
	return username != s.username && s.accounts != nil && s.accounts.VerifyLocalAccount(username, password)
}

func (s *LocalSessionManager) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !s.verify(r.FormValue("username"), r.FormValue("password")) {
		http.Redirect(w, r, "/login.html?error=invalid&return_to="+url.QueryEscape(safeReturnTo(r.FormValue("return_to"))), http.StatusFound)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		deny(w, http.StatusInternalServerError, "could not create session")
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	s.mu.Lock()
	s.sessions[token] = localSession{subject: r.FormValue("username"), expires: expires}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: localSessionCookie, Value: token, Path: "/", Expires: expires, MaxAge: int((8 * time.Hour).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeReturnTo(r.FormValue("return_to")), http.StatusFound)
}

func (s *LocalSessionManager) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(localSessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: localSessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// authenticated returns the session subject. A provisioned account whose
// password login was removed loses its sessions on the next request.
func (s *LocalSessionManager) authenticated(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(localSessionCookie)
	if err != nil {
		return "", false
	}
	s.mu.RLock()
	session, ok := s.sessions[cookie.Value]
	s.mu.RUnlock()
	if !ok || time.Now().After(session.expires) {
		return "", false
	}
	if session.subject != s.username && (s.accounts == nil || !s.accounts.LocalAccountActive(session.subject)) {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
		return "", false
	}
	return session.subject, true
}

func localConsolePath(path string) bool {
	return path == "/console.html" || path == "/workspace.html" || path == "/editorial.html" || strings.HasPrefix(path, "/editorial/") || strings.HasPrefix(path, "/js/") || path == "/styles.css" || strings.HasPrefix(path, "/workspaces/")
}
