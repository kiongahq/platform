package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const localSessionCookie = "kionga_local_session"

type LocalSessionManager struct {
	username string
	password string
	mu       sync.RWMutex
	sessions map[string]time.Time
}

func NewLocalSessionManager(username, password string) *LocalSessionManager {
	return &LocalSessionManager{username: username, password: password, sessions: map[string]time.Time{}}
}

func (s *LocalSessionManager) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/local/login" && r.Method == http.MethodPost:
			s.login(w, r)
			return
		case r.URL.Path == "/auth/logout":
			s.logout(w, r)
			return
		case localConsolePath(r.URL.Path) && !s.authenticated(r):
			http.Redirect(w, r, "/login.html?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *LocalSessionManager) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil ||
		subtle.ConstantTimeCompare([]byte(r.FormValue("username")), []byte(s.username)) != 1 ||
		subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(s.password)) != 1 {
		http.Redirect(w, r, "/login.html?error=invalid", http.StatusFound)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		deny(w, http.StatusInternalServerError, "could not create session")
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	s.mu.Lock()
	s.sessions[token] = expires
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

func (s *LocalSessionManager) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(localSessionCookie)
	if err != nil {
		return false
	}
	s.mu.RLock()
	expires, ok := s.sessions[cookie.Value]
	s.mu.RUnlock()
	if !ok || time.Now().After(expires) {
		return false
	}
	return true
}

func localConsolePath(path string) bool {
	return path == "/console.html" || path == "/app.js" || path == "/styles.css"
}
