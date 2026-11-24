package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Sign-in proxy for applications with visibility sso.
//
// The operator sends the Ingress for <app>.<KIONGA_APPLICATIONS_BASE_DOMAIN>
// to the gateway (an ExternalName Service <app>-sso), and records the real
// upstream in the custom resource. The gateway then:
//
//  1. On the application host without a valid application session, redirects
//     to the console: /applications/<app>/sso?next=<path>.
//  2. That console route is behind normal sign-in (signed-out visitors go to
//     the sign-in page and come back). It checks the caller may read the
//     application's project, then redirects to
//     https://<app host>/_kionga/sso?ticket=<signed, 60 s, one application>.
//  3. The application host turns the ticket into a host-only session cookie
//     (8 hours) and redirects to the original path. A ticket works once:
//     its id is recorded in the store every replica shares, and a second
//     redemption on any replica is refused.
//  4. Every proxied request re-checks the subject's access (suspension,
//     service grant and project) before it is forwarded, so revoking access
//     applies on the next request.
//
// Tickets and sessions are HMAC-signed with a key derived from
// KIONGA_CREDENTIAL_KEY (or KIONGA_INTERNAL_TOKEN), so every gateway replica
// accepts them. Without either key, a per-process random key is used, which
// suits a single development replica only.
//
// Single use needs state the replicas share. That is the gateway's own
// store: PostgreSQL in every multi-replica install, or the memory of the one
// process that serves a file store. If the store cannot record a redemption
// the ticket is refused, never accepted unrecorded.

const (
	appSessionCookie = "kionga_app_session"
	appTicketTTL     = 60 * time.Second
	// A redemption record outlives its ticket by this margin, so a replica
	// whose clock runs behind cannot accept a ticket whose record was
	// already purged.
	appTicketRecordMargin = 5 * time.Minute
	appTicketPurpose      = "application_ticket"
	appSessionTTL         = 8 * time.Hour
	appSSOPath            = "/_kionga/sso"
	upstreamAnnotation    = "kionga.dev/upstream"
)

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /applications/{name}/sso", s.applicationSignIn)
	})
}

type appToken struct {
	Kind string `json:"k"`
	// ID is a ticket's unique id, recorded when the ticket is redeemed.
	// Sessions carry none.
	ID      string `json:"i,omitempty"`
	App     string `json:"a"`
	Subject string `json:"s"`
	// Roles carries the console-time roles for subjects without an access
	// profile (identity-provider administrators); a profile always wins.
	Roles   []string `json:"r,omitempty"`
	Next    string   `json:"n,omitempty"`
	Expires int64    `json:"e"`
}

var (
	appKeyOnce sync.Once
	appKey     []byte
)

func appSigningKey() []byte {
	appKeyOnce.Do(func() {
		secret := os.Getenv("KIONGA_CREDENTIAL_KEY")
		if secret == "" {
			secret = os.Getenv("KIONGA_INTERNAL_TOKEN")
		}
		if secret == "" {
			random := make([]byte, 32)
			_, _ = rand.Read(random)
			secret = string(random)
			log.Printf("application sign-in: no KIONGA_CREDENTIAL_KEY; sessions are valid on this replica only")
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte("kionga application sign-in v1"))
		appKey = mac.Sum(nil)
	})
	return appKey
}

func signAppToken(token appToken) string {
	payload, _ := json.Marshal(token)
	mac := hmac.New(sha256.New, appSigningKey())
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyAppToken checks the signature, kind, application and expiry.
func verifyAppToken(raw, kind, app string, now time.Time) (appToken, error) {
	var token appToken
	payloadPart, signaturePart, found := strings.Cut(raw, ".")
	if !found {
		return token, errors.New("malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return token, errors.New("malformed")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return token, errors.New("malformed")
	}
	mac := hmac.New(sha256.New, appSigningKey())
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return token, errors.New("bad signature")
	}
	if err := json.Unmarshal(payload, &token); err != nil {
		return token, errors.New("malformed")
	}
	if token.Kind != kind || token.App != app || token.Subject == "" {
		return token, errors.New("not for this application")
	}
	if now.Unix() > token.Expires {
		return token, errors.New("expired")
	}
	return token, nil
}

const appTicketRefused = "Sign-in link expired, already used or invalid. Open the application again."

var errAppTicketStore = errors.New("sign-in state unavailable")

func newAppTicketID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

// redeemAppTicket makes a verified ticket single-use. It records the ticket
// id in the shared store and fails when the id was recorded before (a
// replay, on this replica or another), when the ticket has no id, or when
// the store cannot answer (errAppTicketStore): an unrecorded ticket is never
// accepted. It runs only after the signature check, so a caller without a
// valid ticket cannot add records. Ticket fields and store errors are logged
// quoted (%q), so no value can start a log line of its own.
func (s *Server) redeemAppTicket(r *http.Request, ticket appToken) error {
	if ticket.ID == "" {
		return errors.New("ticket has no id")
	}
	expires := time.Unix(ticket.Expires, 0).Add(appTicketRecordMargin)
	err := s.store.RedeemOnce(r.Context(), appTicketPurpose, ticket.App+"/"+ticket.ID, expires)
	if errors.Is(err, store.ErrAlreadyRedeemed) {
		log.Printf("application %q sign-in: ticket for %q was presented again and refused", ticket.App, ticket.Subject)
		return err
	}
	if err != nil {
		log.Printf("application %q sign-in: could not record the ticket, refusing it: %q", ticket.App, err.Error())
		return errAppTicketStore
	}
	return nil
}

// safeAppNext returns next when it is a local path on the application host,
// otherwise "/". It accepts only a path that starts with exactly one "/",
// and rejects:
//   - "//" and "/\" starts (protocol-relative and browser-normalised hosts);
//   - any backslash, control character, space or non-ASCII byte, raw or
//     percent-encoded (tab and newline injection, %2f%2f and %5c tricks);
//   - anything url.Parse reads as having a scheme, host, user or opaque part;
//   - the sign-in endpoint itself.
//
// The result is rebuilt from the parsed path and query, never from the input.
func safeAppNext(next string) string {
	if len(next) > 2048 || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	decoded, err := url.PathUnescape(next)
	if err != nil {
		return "/"
	}
	for _, candidate := range []string{next, decoded} {
		if strings.HasPrefix(candidate, "//") || strings.HasPrefix(candidate, "/\\") || strings.ContainsAny(candidate, "\\") {
			return "/"
		}
		for _, char := range candidate {
			if char <= ' ' || char >= 0x7f {
				return "/"
			}
		}
	}
	parsed, err := url.Parse(next)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "/"
	}
	if !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || parsed.Path == appSSOPath || strings.HasPrefix(parsed.Path, appSSOPath+"/") {
		return "/"
	}
	local := &url.URL{Path: parsed.Path, RawPath: parsed.RawPath, RawQuery: parsed.RawQuery}
	result := local.String()
	if !strings.HasPrefix(result, "/") || strings.HasPrefix(result, "//") || strings.HasPrefix(result, "/\\") {
		return "/"
	}
	return result
}

func applicationConsoleOrigin() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("KIONGA_ALLOWED_ORIGIN")), "/")
}

// applicationHost returns the application name when host is
// <name>.<KIONGA_APPLICATIONS_BASE_DOMAIN>, a valid application name and not
// the console host.
func applicationHost(host string) (string, bool) {
	base := applicationsBaseDomain()
	if base == "" {
		return "", false
	}
	host = strings.ToLower(host)
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	if host == installHost() || !strings.HasSuffix(host, "."+base) {
		return "", false
	}
	name := strings.TrimSuffix(host, "."+base)
	if api.ValidateApplicationName(name) != nil {
		return "", false
	}
	return name, true
}

// ApplicationHostRouter serves sso application hosts and passes every other
// request to next.
func ApplicationHostRouter(next http.Handler, repository store.Repository) http.Handler {
	server := &Server{store: repository}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := applicationHost(r.Host)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		server.applicationEdge(w, r, name)
	})
}

// applicationAccess reports whether a subject may use an application now:
// an enabled profile with the applications service and the application's
// project. Administrators and operators pass.
func (s *Server) applicationAccess(principal auth.Principal, app api.Application) bool {
	if principal.Disabled || !auth.Allowed(principal, http.MethodGet, "/api/v1/applications") {
		return false
	}
	return projectAllowed(s.store, principal, app.ProjectID)
}

// subjectPrincipal rebuilds a principal from the access profile, as the
// API's role middleware does. Without a profile the given roles stand (an
// identity-provider administrator); a user role without a profile is not
// provisioned and is denied by auth.Allowed.
func (s *Server) subjectPrincipal(subject string, roles []string) auth.Principal {
	access, err := s.store.AccessFor(subject)
	if err != nil {
		return auth.Principal{Subject: subject, Roles: roles}
	}
	return auth.Principal{Subject: subject, Roles: []string{access.Role}, Services: access.Services, ProjectIDs: access.ProjectIDs, Disabled: access.Disabled, Provisioned: true}
}

// applicationSignIn runs on the console origin behind normal sign-in and
// hands a short ticket to the application host.
func (s *Server) applicationSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	value, signedIn := auth.PrincipalFrom(r.Context())
	if !signedIn || value.Subject == "" {
		http.Redirect(w, r, "/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	app, err := store.GetApplication(s.store, r.PathValue("name"))
	if err != nil || app.Visibility != "sso" {
		http.NotFound(w, r)
		return
	}
	// Non-API routes skip the role middleware, so resolve the profile here.
	value = s.subjectPrincipal(value.Subject, value.Roles)
	if !s.applicationAccess(value, app) {
		http.Error(w, "Your account has no access to this application's project.", http.StatusForbidden)
		return
	}
	base := applicationsBaseDomain()
	if base == "" {
		http.NotFound(w, r)
		return
	}
	id, err := newAppTicketID()
	if err != nil {
		http.Error(w, "Sign-in is temporarily unavailable. Try again.", http.StatusServiceUnavailable)
		return
	}
	ticket := signAppToken(appToken{Kind: "ticket", ID: id, App: app.Name, Subject: value.Subject, Roles: value.Roles, Next: safeAppNext(r.URL.Query().Get("next")), Expires: time.Now().Add(appTicketTTL).Unix()})
	target := "https://" + app.Name + "." + base + appSSOPath + "?ticket=" + url.QueryEscape(ticket)
	http.Redirect(w, r, target, http.StatusFound) // #nosec G710 -- host is <validated application name>.<configured base domain>
}

// applicationEdge serves one sso application host.
func (s *Server) applicationEdge(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	app, err := store.GetApplication(s.store, name)
	if err != nil || app.Visibility != "sso" {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	if r.URL.Path == appSSOPath {
		w.Header().Set("Cache-Control", "no-store")
		ticket, err := verifyAppToken(r.URL.Query().Get("ticket"), "ticket", name, now)
		if err == nil {
			err = s.redeemAppTicket(r, ticket)
		}
		if errors.Is(err, errAppTicketStore) {
			http.Error(w, "Sign-in is temporarily unavailable. Open the application again.", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			// One answer for every refusal, a replay included. Opening the
			// application again starts a new sign-in.
			http.Error(w, appTicketRefused, http.StatusUnauthorized)
			return
		}
		if !s.applicationAccess(s.subjectPrincipal(ticket.Subject, ticket.Roles), app) {
			http.Error(w, "Your account has no access to this application's project.", http.StatusForbidden)
			return
		}
		session := signAppToken(appToken{Kind: "session", App: name, Subject: ticket.Subject, Roles: ticket.Roles, Expires: now.Add(appSessionTTL).Unix()})
		http.SetCookie(w, &http.Cookie{Name: appSessionCookie, Value: session, Path: "/", MaxAge: int(appSessionTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, safeAppNext(ticket.Next), http.StatusSeeOther) // #nosec G710 -- local path validated by safeAppNext
		return
	}
	cookie, err := r.Cookie(appSessionCookie)
	var session appToken
	if err == nil {
		session, err = verifyAppToken(cookie.Value, "session", name, now)
	}
	if err != nil {
		origin := applicationConsoleOrigin()
		if origin == "" {
			http.Error(w, "Sign-in is not configured for applications (KIONGA_ALLOWED_ORIGIN).", http.StatusServiceUnavailable)
			return
		}
		target := origin + "/applications/" + url.PathEscape(name) + "/sso?next=" + url.QueryEscape(r.URL.RequestURI())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Sign in to continue.", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, target, http.StatusFound) // #nosec G710 -- console origin is operator configuration
		return
	}
	if !s.applicationAccess(s.subjectPrincipal(session.Subject, session.Roles), app) {
		http.Error(w, "Your account has no access to this application's project.", http.StatusForbidden)
		return
	}
	upstream, err := s.applicationUpstream(r, name)
	if err != nil {
		http.Error(w, "The application is not reachable yet: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(upstream)
			out.SetXForwarded()
			out.Out.Header.Set("X-Forwarded-Host", r.Host)
			for key := range out.Out.Header {
				if strings.HasPrefix(strings.ToLower(key), "x-kionga-") {
					out.Out.Header.Del(key)
				}
			}
			out.Out.Header.Set("X-Kionga-Subject", session.Subject)
			stripCookie(out.Out, appSessionCookie)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("application %s proxy: %v", name, err)
			http.Error(w, "The application did not answer.", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// applicationUpstream reads where the operator says the application runs:
// the kionga.dev/upstream annotation, else status.internalURL.
func (s *Server) applicationUpstream(r *http.Request, name string) (*url.URL, error) {
	access, err := s.kube()
	if err != nil {
		return nil, errKubernetesUnavailable
	}
	object, err := access.dynamic.Resource(applicationsGVR).Namespace(access.namespace).Get(r.Context(), applicationObjectName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, errors.New("the application resource does not exist")
	}
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(object.GetAnnotations()[upstreamAnnotation])
	if raw == "" {
		raw, _, _ = unstructured.NestedString(object.Object, "status", "internalURL")
	}
	if raw == "" {
		return nil, errors.New("the operator has not published its address")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw // host:port form
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("invalid upstream %q", raw)
	}
	return parsed, nil
}

func stripCookie(request *http.Request, name string) {
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	kept := slices.DeleteFunc(cookies, func(cookie *http.Cookie) bool { return cookie.Name == name })
	for _, cookie := range kept {
		request.AddCookie(cookie)
	}
}
