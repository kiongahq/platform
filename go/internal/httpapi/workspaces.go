package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Workspace upstreams are configured by the operator, never by a browser URL.
// Hosted users require a separate upstream per subject; a shared Compose
// workspace is available only to the local development administrator.
type workspaceDestination struct {
	*url.URL
	token string
	name  string
}

type edgeContextKey struct{}

var ideSessions = struct {
	sync.Mutex
	values map[string]struct {
		cookie  string
		expires time.Time
	}
}{values: make(map[string]struct {
	cookie  string
	expires time.Time
})}

func ideSession(ctx context.Context, target *workspaceDestination) (string, error) {
	if target.token == "" {
		return "", nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(target.String()+target.token)))
	ideSessions.Lock()
	cached := ideSessions.values[key]
	ideSessions.Unlock()
	if time.Now().Before(cached.expires) {
		return cached.cookie, nil
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(target.String(), "/")+"/login", strings.NewReader(url.Values{"password": {target.token}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("IDE sign-in is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 302 && response.StatusCode != 303 {
		return "", fmt.Errorf("IDE rejected its provisioned credential")
	}
	cookie := ""
	for _, c := range response.Cookies() {
		if c.Value != "" {
			if cookie != "" {
				cookie += "; "
			}
			cookie += c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		return "", fmt.Errorf("IDE did not establish a session")
	}
	ideSessions.Lock()
	for old, c := range ideSessions.values {
		if time.Now().After(c.expires) {
			delete(ideSessions.values, old)
		}
	}
	ideSessions.values[key] = struct {
		cookie  string
		expires time.Time
	}{cookie, time.Now().Add(15 * time.Minute)}
	ideSessions.Unlock()
	return cookie, nil
}

func workspaceTarget(kind string, r *http.Request) (*workspaceDestination, error) {
	if os.Getenv("KIONGA_ENVIRONMENT") == "production" && os.Getenv("KIONGA_TRUSTED_WORKSPACE_PROXY") != "true" && os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN") == "" {
		return nil, fmt.Errorf("Public same-origin workspaces are disabled. Configure isolated workspace access with your administrator")
	}
	key := map[string]string{"workbench": "KIONGA_JUPYTER_UPSTREAM", "ide": "KIONGA_IDE_UPSTREAM"}[kind]
	if key == "" {
		return nil, fmt.Errorf("Unknown workspace")
	}
	if namespace := os.Getenv("KIONGA_WORKSPACE_NAMESPACE"); namespace != "" {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("Workspace discovery is unavailable")
		}
		config.Timeout = 3 * time.Second
		client, err := dynamic.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("Workspace discovery is unavailable")
		}
		return discoverWorkspace(r.Context(), client, namespace, principal(r).Subject, kind)
	}
	raw := os.Getenv(key)
	if raw == "" {
		return nil, fmt.Errorf("This workspace has not been configured")
	}
	p := principal(r)
	if strings.Contains(raw, "{subject}") {
		raw = strings.ReplaceAll(raw, "{subject}", fmt.Sprintf("%x", sha256.Sum256([]byte(p.Subject))))
	} else if os.Getenv("OIDC_ISSUER") != "" || p.Subject != os.Getenv("MLAIOPS_LOCAL_USERNAME") && p.Subject != "admin" {
		return nil, fmt.Errorf("A personal workspace must be provisioned for this account")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("The workspace address is invalid; contact your administrator")
	}
	token := os.Getenv("KIONGA_JUPYTER_TOKEN")
	if kind == "ide" {
		token = os.Getenv("KIONGA_IDE_PASSWORD")
	}
	return &workspaceDestination{URL: u, token: token}, nil
}

func discoverWorkspace(ctx context.Context, client dynamic.Interface, namespace, subject, kind string) (*workspaceDestination, error) {
	gvr := schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaworkspaces"}
	list, err := client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("Workspace discovery is unavailable; contact your administrator")
	}
	var match *unstructured.Unstructured
	for i := range list.Items {
		item := &list.Items[i]
		owner, _, _ := unstructured.NestedString(item.Object, "spec", "subject")
		if owner != subject {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("Multiple workspaces are assigned to this identity; contact your administrator")
		}
		match = item
	}
	if match == nil {
		return nil, fmt.Errorf("Your workspace is still being provisioned")
	}
	disabled, _, _ := unstructured.NestedBool(match.Object, "spec", "disabled")
	if disabled {
		return nil, fmt.Errorf("Your workspace has been suspended")
	}
	services, _, _ := unstructured.NestedStringSlice(match.Object, "spec", "services")
	assigned := false
	for _, service := range services {
		if service == kind {
			assigned = true
		}
	}
	if !assigned {
		return nil, fmt.Errorf("This tool has not been provisioned in your workspace")
	}
	secret, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(namespace).Get(ctx, match.GetName()+"-auth", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Your workspace credentials are not ready")
	}
	encoded, _, _ := unstructured.NestedString(secret.Object, "data", "token")
	token, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(token) == 0 {
		return nil, fmt.Errorf("Your workspace credentials are invalid")
	}
	port := "8888"
	if kind == "ide" {
		port = "8080"
	}
	target, _ := url.Parse("http://" + match.GetName() + "." + namespace + ".svc:" + port)
	return &workspaceDestination{URL: target, token: string(token), name: match.GetName()}, nil
}

func (s *Server) projectOptions(w http.ResponseWriter, r *http.Request) {
	// Only minimal metadata for projects already assigned to this identity.
	items := []map[string]string{}
	allowed := allowedProjectIDs(s.store, principal(r))
	for _, p := range s.store.Projects() {
		if allowed != nil && !allowed[p.ID] {
			continue
		}
		items = append(items, map[string]string{"id": p.ID, "name": p.Name, "namespace": p.Namespace})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Available bool   `json:"available"`
		URL       string `json:"url,omitempty"`
		Message   string `json:"message"`
	}
	items := make([]entry, 2)
	var wg sync.WaitGroup
	for i, kind := range []string{"workbench", "ide"} {
		items[i] = entry{ID: kind, Name: map[string]string{"workbench": "JupyterLab", "ide": "Browser IDE"}[kind]}
		wg.Add(1)
		go func(i int, kind string) {
			defer wg.Done()
			e := &items[i]
			if !auth.Allowed(principal(r), http.MethodGet, "/workspaces/"+kind+"/") {
				e.Message = "Request access to use this workspace"
				return
			}
			target, err := workspaceTarget(kind, r)
			if err != nil {
				e.Message = err.Error()
				return
			}
			endpoint := strings.TrimRight(target.String(), "/") + "/healthz"
			if kind == "workbench" {
				endpoint = strings.TrimRight(target.String(), "/") + "/workspaces/workbench/api"
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if kind == "workbench" {
				request.Header.Set("Authorization", "token "+target.token)
			} else {
				cookie, err := ideSession(ctx, target)
				if err != nil {
					e.Message = err.Error()
					return
				}
				request.Header.Set("Cookie", cookie)
			}
			client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				e.Message = "Workspace is offline. Ask your administrator to start it."
				return
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				e.Message = "Workspace is not ready or its credentials need updating"
				return
			}
			e.Available = true
			if os.Getenv("KIONGA_ENVIRONMENT") == "production" && os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN") != "" {
				e.URL = "/workspace.html?tool=" + kind
				e.Message = "Ready · isolated workspace"
			} else {
				e.URL = "/workspaces/" + kind + "/"
				e.Message = "Ready · uses your Kionga session"
			}
		}(i, kind)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) workspaceProxy(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if os.Getenv("KIONGA_ENVIRONMENT") == "production" && os.Getenv("KIONGA_TRUSTED_WORKSPACE_PROXY") != "true" && r.Context().Value(edgeContextKey{}) != true {
		writeError(w, 404, "not_found", "Workspace access uses an isolated host")
		return
	}
	if _, ok := auth.PrincipalFrom(r.Context()); !ok {
		writeError(w, 401, "unauthenticated", "Sign in to open your workspace")
		return
	}
	if !auth.Allowed(principal(r), r.Method, "/workspaces/"+kind+"/") {
		writeError(w, 403, "forbidden", "This workspace has not been assigned to you")
		return
	}
	// Never send platform cookies or bearer tokens to a workspace. Prevent
	// cross-origin writes and WebSocket upgrades before injecting its credential.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			writeError(w, 403, "forbidden", "Cross-origin workspace requests are not allowed")
			return
		}
	}
	target, err := workspaceTarget(kind, r)
	if err != nil {
		writeError(w, 503, "workspace_unavailable", err.Error())
		return
	}
	if expected, ok := r.Context().Value(edgeWorkspaceNameKey{}).(string); ok && target.name != expected {
		writeError(w, 403, "forbidden", "Workspace host does not match the assigned workspace")
		return
	}
	ideCookie := ""
	if kind == "ide" {
		ideCookie, err = ideSession(r.Context(), target)
		if err != nil {
			writeError(w, 503, "workspace_unavailable", err.Error())
			return
		}
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(target.URL)
		if kind == "ide" {
			p.Out.URL.Path = strings.TrimPrefix(r.URL.Path, "/workspaces/ide")
			p.Out.URL.RawPath = ""
		}
		p.Out.Header.Del("Cookie")
		p.Out.Header.Del("Authorization")
		if ideCookie != "" {
			p.Out.Header.Set("Cookie", ideCookie)
		}
		p.SetXForwarded()
		if kind == "workbench" {
			p.Out.Header.Set("Authorization", "token "+target.token)
		}
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
		writeError(w, 503, "workspace_unavailable", "Workspace is offline. Return to Kionga and retry when it is ready.")
	},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")
			if r.Context().Value(edgeContextKey{}) == true {
				rewriteWorkspaceFramePolicy(response.Header, os.Getenv("MLAIOPS_ALLOWED_ORIGIN"))
			}
			if kind == "ide" {
				if path := response.Header.Get("Location"); strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") {
					response.Header.Set("Location", "/workspaces/ide"+path)
				}
			}
			return nil
		}}
	// Notebook kernels and IDE terminals are long-lived connections. The
	// gateway's ordinary API deadlines must not cut these streams short.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	proxy.ServeHTTP(w, r)
}

func rewriteWorkspaceFramePolicy(header http.Header, origin string) {
	// Preserve all upstream CSP directives except frame-ancestors. The
	// internal service's SAMEORIGIN policy would block the isolated iframe.
	header.Del("X-Frame-Options")
	policies := header.Values("Content-Security-Policy")
	header.Del("Content-Security-Policy")
	for _, policy := range policies {
		parts := strings.Split(policy, ";")
		kept := make([]string, 0, len(parts)+1)
		for _, part := range parts {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(part)), "frame-ancestors") && strings.TrimSpace(part) != "" {
				kept = append(kept, strings.TrimSpace(part))
			}
		}
		kept = append(kept, "frame-ancestors "+origin)
		header.Add("Content-Security-Policy", strings.Join(kept, "; "))
	}
	if len(policies) == 0 {
		header.Set("Content-Security-Policy", "frame-ancestors "+origin)
	}
	header.Set("Referrer-Policy", "no-referrer")
}

func (s *Server) launchWorkspace(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !auth.Allowed(principal(r), r.Method, "/api/v1/workspaces/"+kind+"/launch") {
		writeError(w, 403, "forbidden", "This workspace has not been assigned to you")
		return
	}
	target, err := workspaceTarget(kind, r)
	if err != nil {
		writeError(w, 503, "workspace_unavailable", err.Error())
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	destination := "/workspaces/" + kind + "/"
	if kind == "workbench" {
		destination += "lab"
	}
	if req.ProjectID != "" {
		if !projectAllowed(s.store, principal(r), req.ProjectID) {
			writeError(w, 403, "forbidden", "This project has not been assigned to you")
			return
		}
		project, err := s.store.Project(req.ProjectID)
		if err != nil {
			writeError(w, 404, "not_found", "Project not found")
			return
		}
		if kind == "ide" {
			cookie, err := ideSession(r.Context(), target)
			if err != nil {
				writeError(w, 503, "workspace_unavailable", err.Error())
				return
			}
			body, _ := json.Marshal(map[string]string{"namespace": project.Namespace})
			request, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(target.String(), "/")+"/proxy/8890/prepare-project", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Cookie", cookie)
			client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				writeError(w, 503, "workspace_unavailable", "Could not prepare the IDE project folder. Please retry.")
				return
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				writeError(w, 502, "workspace_unavailable", "The IDE could not prepare this project folder. Rebuild or update its workspace image.")
				return
			}
			destination += "?folder=" + url.QueryEscape("/workspace/projects/"+project.Namespace)
			s.writeWorkspaceLaunch(w, r, target, kind, destination)
			return
		}
		// Jupyter and the IDE share /workspace. The contents API creates the
		// selected project folder without invoking a shell or accepting a path.
		upstream, err := workspaceTarget("workbench", r)
		if err != nil {
			writeError(w, 503, "workspace_unavailable", "Start the project's Jupyter workspace to prepare its shared folder")
			return
		}
		for _, path := range []string{"projects", "projects/" + project.Namespace} {
			endpoint := strings.TrimRight(upstream.String(), "/") + "/workspaces/workbench/api/contents/" + path
			client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			get, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
			get.Header.Set("Authorization", "token "+upstream.token)
			response, err := client.Do(get)
			if err != nil {
				writeError(w, 503, "workspace_unavailable", "Could not reach the shared workspace. Please retry.")
				return
			}
			response.Body.Close()
			if response.StatusCode == 200 {
				continue
			}
			if response.StatusCode != 404 {
				writeError(w, 502, "workspace_unavailable", "Could not read the project folder. Check workspace credentials.")
				return
			}
			put, _ := http.NewRequestWithContext(r.Context(), http.MethodPut, endpoint, bytes.NewBufferString(`{"type":"directory"}`))
			put.Header.Set("Authorization", "token "+upstream.token)
			put.Header.Set("Content-Type", "application/json")
			response, err = client.Do(put)
			if err != nil {
				writeError(w, 502, "workspace_unavailable", "Could not prepare the project folder. Please retry.")
				return
			}
			response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				writeError(w, 502, "workspace_unavailable", "Could not prepare the project folder. Check workspace storage permissions.")
				return
			}
		}
		destination += "/tree/projects/" + url.PathEscape(project.Namespace)
	}
	s.writeWorkspaceLaunch(w, r, target, kind, destination)
}

func (s *Server) writeWorkspaceLaunch(w http.ResponseWriter, r *http.Request, target *workspaceDestination, kind, destination string) {
	if os.Getenv("KIONGA_ENVIRONMENT") == "production" && os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN") != "" {
		issuer, ok := s.store.(interface {
			IssueWorkspaceTicket(context.Context, store.WorkspaceEdgeIdentity) (string, error)
		})
		if !ok || target.name == "" {
			writeError(w, 503, "workspace_unavailable", "Isolated workspace access is not configured")
			return
		}
		ticket, err := issuer.IssueWorkspaceTicket(r.Context(), store.WorkspaceEdgeIdentity{Subject: principal(r).Subject, WorkspaceName: target.name, Kind: kind})
		if err != nil {
			writeError(w, 503, "workspace_unavailable", "Could not create workspace handoff")
			return
		}
		host := kind + "-" + workspaceSlug(target.name) + "." + os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN")
		if _, _, valid := parseWorkspaceHost(host, os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN")); !valid {
			writeError(w, 503, "workspace_unavailable", "Workspace name cannot be routed on the isolated domain")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]string{"url": "https://" + host + "/_kionga/redeem", "ticket": ticket, "next": destination})
		return
	}
	writeJSON(w, 200, map[string]string{"url": destination})
}
