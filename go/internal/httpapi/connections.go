package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/integrations"
	"github.com/kiongahq/platform/internal/policy"
	"github.com/kiongahq/platform/internal/runtimeconfig"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func (s *Server) activeConnection(kind string) *api.Connection {
	var selected *api.Connection
	for _, c := range s.store.Connections() {
		if c.Type == kind && c.ActivatedAt != nil && (selected == nil || c.ActivatedAt.After(*selected.ActivatedAt)) {
			copy := c
			selected = &copy
		}
	}
	return selected
}

func connectionToken(c api.Connection) (string, error) {
	if c.SecretRef == "none" {
		return "", nil
	}
	if !strings.HasPrefix(c.SecretRef, "env:") {
		return "", fmt.Errorf("Use none for an unauthenticated service, or env:VARIABLE for a gateway environment credential")
	}
	token, permitted := runtimeconfig.PlatformSecrets(c.Type).Resolve(c.SecretRef)
	if !permitted {
		return "", fmt.Errorf("This credential reference is not permitted; use env:KIONGA_PLATFORM_SECRET_<NAME> or list the variable in KIONGA_PLATFORM_SECRET_REFS")
	}
	if token == "" {
		return "", fmt.Errorf("The referenced credential is not available on the gateway")
	}
	return token, nil
}

var connectionHTTPClient = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

func (s *Server) prefectClient() integrations.Prefect {
	base, token := os.Getenv("PREFECT_API_URL"), ""
	if c := s.activeConnection("prefect"); c != nil {
		base = c.Endpoint
		var err error
		token, err = connectionToken(*c)
		if err != nil {
			base = ""
		}
	}
	return integrations.NewPrefect(base, token)
}

func (s *Server) prefectConfigured() bool {
	return s.activeConnection("prefect") != nil || os.Getenv("PREFECT_API_URL") != ""
}

// connectionSecretIssue is the credential reference rule create and update
// share: an env: reference must be one the platform scope permits, so the
// gateway's own secrets can never be attached to a connection.
func connectionSecretIssue(req api.CreateConnectionRequest) string {
	if strings.HasPrefix(req.SecretRef, "env:") && !runtimeconfig.PlatformSecrets(req.Type).Permits(req.SecretRef) {
		return "Use env:KIONGA_PLATFORM_SECRET_<NAME>, or list the variable in KIONGA_PLATFORM_SECRET_REFS. The gateway's own secrets cannot be referenced."
	}
	return ""
}

func (s *Server) findConnection(id string) *api.Connection {
	for _, item := range s.store.Connections() {
		if item.ID == id {
			copy := item
			return &copy
		}
	}
	return nil
}

// verifyRuntime runs the checks a connection must pass to drive new
// operations: a runtime service, a resolvable credential, no pipeline in
// flight and a reachable service. It writes the refusal and reports false.
func (s *Server) verifyRuntime(w http.ResponseWriter, r *http.Request, c api.Connection) bool {
	if c.Type != "prefect" && c.Type != "openfaas" {
		writeError(w, 422, "monitor_only", "This connection monitors availability. Configure its deployment settings to change its runtime.")
		return false
	}
	if _, err := connectionToken(c); err != nil {
		writeError(w, 422, "credential_unavailable", err.Error())
		return false
	}
	for _, run := range s.store.Runs() {
		if run.Status == "queued" || run.Status == "running" {
			writeError(w, 409, "runs_active", "Wait for running and queued pipelines to finish before changing runtimes.")
			return false
		}
	}
	if err := checkConnection(r, c); err != nil {
		writeError(w, 422, "connection_failed", err.Error())
		return false
	}
	return true
}

func (s *Server) activateConnection(w http.ResponseWriter, r *http.Request) {
	c := s.findConnection(r.PathValue("id"))
	if c == nil {
		writeError(w, 404, "not_found", "Connection not found")
		return
	}
	if decision := s.authorize(r, policy.InfraProvision, policy.ConnectionResource(c.ID)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if !s.verifyRuntime(w, r, *c) {
		return
	}
	// The store activates only the target verified above.
	item, err := s.store.ActivateConnection(*c, actor(r))
	if errors.Is(err, store.ErrConnectionChanged) {
		writeError(w, http.StatusConflict, "connection_changed", err.Error())
		return
	}
	writeMutation(w, item, err, http.StatusOK)
}

// updateConnection replaces a connection's name, service, address and
// credential reference under the rules of create. A connection that has been
// activated as a runtime keeps its activation: it cannot change service, and
// a new address or credential is saved only after it passes the same checks
// as activation, so new operations never reach a target nobody verified. A
// connection that was never activated returns to pending when its target
// changes, until it is tested again.
func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	var req api.CreateConnectionRequest
	if err := decode(r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	current := s.findConnection(r.PathValue("id"))
	if current == nil {
		writeError(w, http.StatusNotFound, "not_found", "Connection not found")
		return
	}
	if decision := s.authorize(r, policy.InfraProvision, policy.ConnectionResource(current.ID)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if err := store.ValidateConnectionRequest(req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	if message := connectionSecretIssue(req); message != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", message)
		return
	}
	verified := false
	if current.ActivatedAt != nil && store.ConnectionTargetChanged(*current, req) {
		if req.Type != current.Type {
			writeError(w, http.StatusConflict, "active_runtime", store.ErrConnectionTypeLocked.Error())
			return
		}
		candidate := *current
		candidate.Endpoint, candidate.SecretRef = req.Endpoint, req.SecretRef
		if !s.verifyRuntime(w, r, candidate) {
			return
		}
		verified = true
	}
	item, err := s.store.UpdateConnection(current.ID, req, verified, actor(r))
	// Reached only when the connection was activated between the read above
	// and the write: nothing was saved.
	if errors.Is(err, store.ErrConnectionTypeLocked) || errors.Is(err, store.ErrConnectionUnverified) {
		writeError(w, http.StatusConflict, "active_runtime", err.Error())
		return
	}
	writeMutation(w, item, err, http.StatusOK)
}

func checkConnection(r *http.Request, c api.Connection) error {
	target, err := url.Parse(c.Endpoint)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.Fragment != "" {
		return fmt.Errorf("Use an HTTP or HTTPS service address without embedded credentials")
	}
	token, err := connectionToken(c)
	if err != nil {
		return err
	}
	switch c.Type {
	case "prefect":
		target.Path = strings.TrimSuffix(strings.TrimRight(target.Path, "/"), "/api") + "/api/health"
	case "openfaas":
		target.Path = strings.TrimRight(target.Path, "/") + "/system/functions"
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if token != "" {
		if c.Type == "openfaas" {
			user := os.Getenv("OPENFAAS_USER")
			if user == "" {
				user = "admin"
			}
			request.SetBasicAuth(user, token)
		} else {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response, err := connectionHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("Service could not be reached: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Service returned %s; verify its address and credentials", response.Status)
	}
	return nil
}
