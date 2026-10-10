package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	SessionCookieName = "kionga_session"
	stateCookieName   = "kionga_oauth_state"
	returnCookieName  = "kionga_return_to"
	verifierCookie    = "kionga_oauth_verifier"
	nonceCookieName   = "kionga_oauth_nonce"
)

type SessionConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	RedirectURL  string
	Secure       bool
	OnLogout     func(context.Context, string) error
}

type SessionManager struct {
	config   SessionConfig
	verifier *Verifier
	oauth    oauth2.Config
}

func NewSessionManager(config SessionConfig, verifier *Verifier) (*SessionManager, error) {
	if config.ClientID == "" || config.ClientSecret == "" || config.AuthURL == "" ||
		config.TokenURL == "" || config.RedirectURL == "" {
		return nil, errors.New("OIDC browser session configuration is incomplete")
	}
	return &SessionManager{
		config: config, verifier: verifier,
		oauth: oauth2.Config{
			ClientID: config.ClientID, ClientSecret: config.ClientSecret, RedirectURL: config.RedirectURL,
			Endpoint: oauth2.Endpoint{AuthURL: config.AuthURL, TokenURL: config.TokenURL},
			Scopes:   []string{"openid", "profile", "email", "groups"},
		},
	}, nil
}

func (s *SessionManager) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			s.login(w, r)
		case "/auth/callback":
			s.callback(w, r)
		case "/auth/logout":
			s.logout(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func (s *SessionManager) login(w http.ResponseWriter, r *http.Request) {
	s.loginWith(w, r, "")
}

// loginWith starts the authorization code flow. connector selects one
// upstream identity provider at a broker such as Dex (connector_id); empty
// lets the broker show its own chooser. PKCE (S256) binds the code to this
// browser and the nonce binds the ID token to this login attempt.
func (s *SessionManager) loginWith(w http.ResponseWriter, r *http.Request, connector string) {
	state, err := randomToken(32)
	if err != nil {
		deny(w, http.StatusInternalServerError, "could not start login")
		return
	}
	nonce, err := randomToken(32)
	if err != nil {
		deny(w, http.StatusInternalServerError, "could not start login")
		return
	}
	pkce := oauth2.GenerateVerifier()
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	s.setCookie(w, stateCookieName, state, 10*time.Minute)
	s.setCookie(w, returnCookieName, returnTo, 10*time.Minute)
	s.setCookie(w, verifierCookie, pkce, 10*time.Minute)
	s.setCookie(w, nonceCookieName, nonce, 10*time.Minute)
	options := []oauth2.AuthCodeOption{oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(pkce),
		oauth2.SetAuthURLParam("nonce", nonce)}
	if connector != "" {
		options = append(options, oauth2.SetAuthURLParam("connector_id", connector))
	}
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, options...), http.StatusFound)
}

func (s *SessionManager) callback(w http.ResponseWriter, r *http.Request) {
	state, err := r.Cookie(stateCookieName)
	if err != nil || state.Value == "" || state.Value != r.URL.Query().Get("state") {
		deny(w, http.StatusBadRequest, "invalid login state")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		deny(w, http.StatusUnauthorized, "identity provider rejected login")
		return
	}
	pkce, err := r.Cookie(verifierCookie)
	if err != nil || pkce.Value == "" {
		deny(w, http.StatusBadRequest, "invalid login state")
		return
	}
	token, err := s.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(pkce.Value))
	if err != nil {
		deny(w, http.StatusUnauthorized, "login token exchange failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		deny(w, http.StatusUnauthorized, "identity provider returned no ID token")
		return
	}
	claims, err := s.verifier.verifyClaims(r.Context(), rawIDToken)
	if err != nil {
		deny(w, http.StatusUnauthorized, "identity token validation failed")
		return
	}
	nonce, err := r.Cookie(nonceCookieName)
	if err != nil || nonce.Value == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce.Value)) != 1 {
		deny(w, http.StatusUnauthorized, "identity token does not match this login")
		return
	}
	s.setCookie(w, SessionCookieName, rawIDToken, 8*time.Hour)
	s.clearCookie(w, stateCookieName)
	s.clearCookie(w, verifierCookie)
	s.clearCookie(w, nonceCookieName)
	target := "/console.html"
	if cookie, cookieErr := r.Cookie(returnCookieName); cookieErr == nil {
		target = safeReturnTo(cookie.Value)
	}
	s.clearCookie(w, returnCookieName)
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *SessionManager) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.endSession(w, r); err != nil {
		deny(w, http.StatusServiceUnavailable, "could not revoke workspace sessions")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// endSession revokes the subject's workspace sessions (OnLogout) and clears
// the SSO cookie. The cookie is kept when revocation fails, so logout can be
// retried instead of leaving live workspace sessions behind.
func (s *SessionManager) endSession(w http.ResponseWriter, r *http.Request) error {
	if s.config.OnLogout != nil {
		if cookie, err := r.Cookie(SessionCookieName); err == nil {
			if principal, verifyErr := s.verifier.Verify(r.Context(), cookie.Value); verifyErr == nil {
				if err := s.config.OnLogout(r.Context(), principal.Subject); err != nil {
					return err
				}
			}
		}
	}
	s.clearCookie(w, SessionCookieName)
	return nil
}

func (s *SessionManager) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()), Expires: time.Now().Add(ttl),
		HttpOnly: true, Secure: s.config.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *SessionManager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: s.config.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeReturnTo(value string) string {
	if value == "" {
		return "/console.html"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") ||
		strings.HasPrefix(parsed.Path, "//") {
		return "/console.html"
	}
	return parsed.RequestURI()
}
