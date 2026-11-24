package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const localSessionCookie = "kionga_local_session"

// LocalAccounts verifies administrator-provisioned password logins. The
// bootstrap administrator from KIONGA_LOCAL_USERNAME/PASSWORD always works;
// other subjects are normal users whose role and grants come from UserAccess.
type LocalAccounts interface {
	VerifyLocalAccount(subject, password string) bool
	LocalAccountActive(subject string) bool
}

// LocalSessionStore holds local sessions where every gateway replica can see
// them: the gateway's own store (PostgreSQL when there is more than one
// replica). A session issued by one replica is then valid on all of them,
// and signing out on one revokes it on all of them.
//
// Sessions are stored under the SHA-256 of the cookie's random token, never
// the token, so reading the store does not yield a usable session. Any error
// is treated as "not signed in": an unreachable store never lets a request
// through.
type LocalSessionStore interface {
	CreateLocalSession(ctx context.Context, tokenHash, subject string, expires time.Time) error
	// LocalSession returns found=false for an unknown or revoked session.
	LocalSession(ctx context.Context, tokenHash string) (subject string, expires time.Time, found bool, err error)
	RevokeLocalSession(ctx context.Context, tokenHash string) error
}

const localSessionTTL = 8 * time.Hour

// localSessionKey is the store key for a session cookie value.
func localSessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Failed sign-ins are throttled per account, not per client address: behind
// the TLS edge every request shares the proxy's address. After
// loginFreeFailures misses, each further attempt waits a doubling delay capped
// at loginMaxDelay, so guessing is slow while a locked-out user waits at most
// a minute.
const (
	loginFreeFailures = 5
	loginMaxDelay     = time.Minute
	loginFailureTTL   = 15 * time.Minute
)

type loginFailures struct {
	count int
	last  time.Time
}

type LocalSessionManager struct {
	username string
	password string
	accounts LocalAccounts
	sessions LocalSessionStore
	mu       sync.RWMutex
	failures map[string]loginFailures
	now      func() time.Time
}

// NewLocalSessionManager keeps sessions in the given store, which must be the
// one every gateway replica shares.
func NewLocalSessionManager(username, password string, sessions LocalSessionStore, accounts ...LocalAccounts) *LocalSessionManager {
	if sessions == nil {
		panic("auth: local sessions need a session store")
	}
	manager := &LocalSessionManager{username: username, password: password, sessions: sessions,
		failures: map[string]loginFailures{}, now: time.Now}
	if len(accounts) > 0 {
		manager.accounts = accounts[0]
	}
	return manager
}

// principalFor maps a session subject to its starting principal. The RBAC
// resolver later overlays the administrator-managed UserAccess profile.
func (s *LocalSessionManager) principalFor(subject string) Principal {
	if subject == s.username {
		role := os.Getenv("KIONGA_LOCAL_ROLE")
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
			http.Redirect(w, r, "/login?return_to="+url.QueryEscape(safeReturnTo(r.URL.Query().Get("return_to"))), http.StatusFound)
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
				http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
		}
		if _, ok := PrincipalFrom(r.Context()); !ok {
			if subject, ok := s.authenticated(r); ok {
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=invalid&return_to="+url.QueryEscape(safeReturnTo(r.FormValue("return_to"))), http.StatusFound)
		return
	}
	username := r.FormValue("username")
	if wait := s.loginDelay(username); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second).Seconds())+1))
		http.Redirect(w, r, "/login?error=throttled&return_to="+url.QueryEscape(safeReturnTo(r.FormValue("return_to"))), http.StatusSeeOther)
		return
	}
	if !s.verify(username, r.FormValue("password")) {
		s.recordFailure(username)
		http.Redirect(w, r, "/login?error=invalid&return_to="+url.QueryEscape(safeReturnTo(r.FormValue("return_to"))), http.StatusFound)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		deny(w, http.StatusInternalServerError, "could not create session")
		return
	}
	now := s.now()
	expires := now.Add(localSessionTTL)
	if err := s.sessions.CreateLocalSession(r.Context(), localSessionKey(token), username, expires); err != nil {
		log.Printf("local sign-in: could not store the session: %v", err)
		deny(w, http.StatusServiceUnavailable, "could not create session")
		return
	}
	s.mu.Lock()
	delete(s.failures, username)
	s.pruneLocked(now)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows requestIsHTTPS(r)
		Name: localSessionCookie, Value: token, Path: "/", Expires: expires, MaxAge: int(localSessionTTL.Seconds()),
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeReturnTo(r.FormValue("return_to")), http.StatusFound) // #nosec G710 -- target is safeReturnTo-sanitized
}

// loginDelay is how long the account must wait before its next attempt.
func (s *LocalSessionManager) loginDelay(username string) time.Duration {
	s.mu.RLock()
	failed := s.failures[username]
	s.mu.RUnlock()
	if failed.count < loginFreeFailures {
		return 0
	}
	delay := loginMaxDelay
	if shift := failed.count - loginFreeFailures; shift < 6 {
		delay = min(time.Second<<shift, loginMaxDelay)
	}
	return failed.last.Add(delay).Sub(s.now())
}

func (s *LocalSessionManager) recordFailure(username string) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	failed := s.failures[username]
	if now.Sub(failed.last) > loginFailureTTL {
		failed.count = 0
	}
	s.failures[username] = loginFailures{count: failed.count + 1, last: now}
	s.pruneLocked(now)
}

// pruneLocked drops stale failure counters so the map does not grow without
// bound. The caller holds s.mu. Expired sessions are purged by the store.
func (s *LocalSessionManager) pruneLocked(now time.Time) {
	for name, failed := range s.failures {
		if now.Sub(failed.last) > loginFailureTTL {
			delete(s.failures, name)
		}
	}
}

// requestIsHTTPS reports whether the browser reached the gateway over TLS,
// directly or through the TLS edge.
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *LocalSessionManager) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.endSession(w, r); err != nil {
		deny(w, http.StatusServiceUnavailable, "could not end the session")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// endSession revokes the caller's local session for every replica and clears
// its cookie. The cookie is kept when revocation fails, so sign-out can be
// retried instead of reporting success while the session is still live.
func (s *LocalSessionManager) endSession(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(localSessionCookie); err == nil && cookie.Value != "" {
		if err := s.sessions.RevokeLocalSession(r.Context(), localSessionKey(cookie.Value)); err != nil {
			log.Printf("local sign-out: could not revoke the session: %v", err)
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: localSessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode}) // #nosec G124 -- Secure follows requestIsHTTPS(r)
	return nil
}

// authenticated returns the session subject. The session is looked up in the
// shared store on every request, so a sign-out on any replica applies at
// once. A provisioned account whose password login was removed loses its
// sessions on the next request. A store that cannot answer means not signed
// in.
func (s *LocalSessionManager) authenticated(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(localSessionCookie)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	key := localSessionKey(cookie.Value)
	subject, expires, found, err := s.sessions.LocalSession(r.Context(), key)
	if err != nil {
		log.Printf("local session check failed, treating the request as signed out: %v", err)
		return "", false
	}
	if !found || subject == "" || s.now().After(expires) {
		return "", false
	}
	if subject != s.username && (s.accounts == nil || !s.accounts.LocalAccountActive(subject)) {
		if err := s.sessions.RevokeLocalSession(r.Context(), key); err != nil {
			log.Printf("local session of a removed account could not be revoked: %v", err)
		}
		return "", false
	}
	return subject, true
}

func localConsolePath(path string) bool {
	return path == "/console" || strings.HasPrefix(path, "/console/") || path == "/workspace" || path == "/workspace/" || path == "/studio" || strings.HasPrefix(path, "/studio/") || strings.HasPrefix(path, "/workspaces/")
}
