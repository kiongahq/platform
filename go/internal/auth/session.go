package auth

import (
	"context"
	"crypto/rand"
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
	state, err := randomToken(32)
	if err != nil {
		deny(w, http.StatusInternalServerError, "could not start login")
		return
	}
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	s.setCookie(w, stateCookieName, state, 10*time.Minute)
	s.setCookie(w, returnCookieName, returnTo, 10*time.Minute)
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline), http.StatusFound)
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
	token, err := s.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		deny(w, http.StatusUnauthorized, "login token exchange failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		deny(w, http.StatusUnauthorized, "identity provider returned no ID token")
		return
	}
	if _, err := s.verifier.Verify(r.Context(), rawIDToken); err != nil {
		deny(w, http.StatusUnauthorized, "identity token validation failed")
		return
	}
	s.setCookie(w, SessionCookieName, rawIDToken, 8*time.Hour)
	s.clearCookie(w, stateCookieName)
	target := "/console.html"
	if cookie, cookieErr := r.Cookie(returnCookieName); cookieErr == nil {
		target = safeReturnTo(cookie.Value)
	}
	s.clearCookie(w, returnCookieName)
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *SessionManager) logout(w http.ResponseWriter, r *http.Request) {
	if s.config.OnLogout != nil {
		if cookie, err := r.Cookie(SessionCookieName); err == nil {
			if principal, verifyErr := s.verifier.Verify(r.Context(), cookie.Value); verifyErr == nil {
				if err := s.config.OnLogout(r.Context(), principal.Subject); err != nil {
					deny(w, http.StatusServiceUnavailable, "could not revoke workspace sessions")
					return
				}
			}
		}
	}
	s.clearCookie(w, SessionCookieName)
	http.Redirect(w, r, "/", http.StatusFound)
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
