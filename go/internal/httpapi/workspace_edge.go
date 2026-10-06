package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
)

const edgeCookieName = "kionga_workspace_session"

func workspaceSlug(name string) string {
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x", digest[:16])
}

type edgeWorkspaceNameKey struct{}

type edgeStore interface {
	RedeemWorkspaceTicket(context.Context, string) (store.WorkspaceEdgeIdentity, string, error)
	WorkspaceEdgeSession(context.Context, string) (store.WorkspaceEdgeIdentity, error)
	RevokeWorkspaceEdgeSession(context.Context, string) error
}

// WorkspaceHostRouter isolates the control origin from every user workspace
// origin. The wildcard TLS ingress points to the same service, but requests on
// workspace hosts never enter the console/auth router.
func WorkspaceHostRouter(platform http.Handler, repository store.Repository) http.Handler {
	origin, _ := url.Parse(os.Getenv("MLAIOPS_ALLOWED_ORIGIN"))
	platformHost := strings.ToLower(origin.Hostname())
	base := strings.ToLower(strings.TrimSuffix(os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN"), "."))
	server := &Server{store: repository}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		if host == platformHost {
			platform.ServeHTTP(w, r)
			return
		}
		kind, workspace, ok := parseWorkspaceHost(host, base)
		if !ok {
			http.NotFound(w, r)
			return
		}
		started := time.Now()
		server.workspaceEdge(w, r, kind, workspace)
		requestCount.WithLabelValues(r.Method, "workspace").Inc()
		requestDuration.WithLabelValues("workspace").Observe(time.Since(started).Seconds())
	})
}

func parseWorkspaceHost(host, base string) (kind, slug string, ok bool) {
	if base == "" || !strings.HasSuffix(host, "."+base) {
		return "", "", false
	}
	label := strings.TrimSuffix(host, "."+base)
	for _, candidate := range []string{"workbench", "ide"} {
		prefix := candidate + "-"
		if strings.HasPrefix(label, prefix) {
			name := strings.TrimPrefix(label, prefix)
			if len(name) != 32 {
				return "", "", false
			}
			for _, char := range name {
				if !(char >= 'a' && char <= 'f' || char >= '0' && char <= '9') {
					return "", "", false
				}
			}
			return candidate, name, true
		}
	}
	return "", "", false
}

func (s *Server) workspaceEdge(w http.ResponseWriter, r *http.Request, kind, slug string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+os.Getenv("MLAIOPS_ALLOWED_ORIGIN"))
	w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	backend, ok := s.store.(edgeStore)
	if !ok {
		http.Error(w, "Workspace sessions require PostgreSQL", 503)
		return
	}
	if r.URL.Path == "/_kionga/redeem" {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.Header.Get("Origin") != os.Getenv("MLAIOPS_ALLOWED_ORIGIN") {
			http.Error(w, "Invalid handoff origin", 403)
			return
		}
		if r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			http.Error(w, "Invalid handoff transport", 400)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid handoff form", 400)
			return
		}
		next := r.FormValue("next")
		if !safeWorkspaceNext(next, kind) {
			http.Error(w, "Invalid workspace destination", 400)
			return
		}
		identity, session, err := backend.RedeemWorkspaceTicket(r.Context(), r.FormValue("ticket"))
		if err != nil || identity.Kind != kind || workspaceSlug(identity.WorkspaceName) != slug {
			http.Error(w, "Workspace handoff expired or invalid", 401)
			return
		}
		if !s.edgeAccessAllowed(identity) {
			_ = backend.RevokeWorkspaceEdgeSession(r.Context(), session)
			http.Error(w, "Workspace access revoked", 403)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: edgeCookieName, Value: session, Path: "/", MaxAge: 1800, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/_kionga/logout" {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != "https://"+r.Host {
			http.Error(w, "Forbidden", 403)
			return
		}
		if cookie, err := r.Cookie(edgeCookieName); err == nil {
			_ = backend.RevokeWorkspaceEdgeSession(r.Context(), cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: edgeCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(204)
		return
	}
	cookie, err := r.Cookie(edgeCookieName)
	if err != nil {
		http.Error(w, "Workspace sign-in required", 401)
		return
	}
	identity, err := backend.WorkspaceEdgeSession(r.Context(), cookie.Value)
	if err != nil || identity.Kind != kind || workspaceSlug(identity.WorkspaceName) != slug {
		http.Error(w, "Workspace session expired", 401)
		return
	}
	if !s.edgeAccessAllowed(identity) {
		_ = backend.RevokeWorkspaceEdgeSession(r.Context(), cookie.Value)
		http.Error(w, "Workspace access revoked", 403)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != "https://"+r.Host {
		http.Error(w, "Workspace origin mismatch", 403)
		return
	}
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/workspaces/"+kind+"/", http.StatusFound)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/workspaces/"+kind+"/") {
		http.NotFound(w, r)
		return
	}
	access, _ := s.store.AccessFor(identity.Subject)
	principal := auth.Principal{Subject: identity.Subject, Roles: []string{access.Role}, Services: access.Services, Disabled: access.Disabled, Provisioned: true}
	ctx := context.WithValue(r.Context(), edgeContextKey{}, true)
	ctx = context.WithValue(ctx, edgeWorkspaceNameKey{}, identity.WorkspaceName)
	r = r.WithContext(auth.WithPrincipal(ctx, principal))
	r.SetPathValue("kind", kind)
	s.workspaceProxy(w, r)
}

func (s *Server) edgeAccessAllowed(identity store.WorkspaceEdgeIdentity) bool {
	access, err := s.store.AccessFor(identity.Subject)
	if err != nil || access.Disabled {
		return false
	}
	p := auth.Principal{Subject: identity.Subject, Roles: []string{access.Role}, Services: access.Services, Disabled: access.Disabled, Provisioned: true}
	return auth.Allowed(p, http.MethodGet, "/workspaces/"+identity.Kind+"/")
}

func safeWorkspaceNext(next, kind string) bool {
	u, err := url.Parse(next)
	return err == nil && !u.IsAbs() && u.Host == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/workspaces/"+kind+"/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") && !strings.Contains(u.Path, "..")
}
