package httpapi

import (
	"fmt"
	"github.com/ml-ai-ops/platform/internal/policy"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/integrations"
	"github.com/ml-ai-ops/platform/pkg/api"
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
	token := os.Getenv(strings.TrimPrefix(c.SecretRef, "env:"))
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

func (s *Server) activateConnection(w http.ResponseWriter, r *http.Request) {
	var c *api.Connection
	for _, item := range s.store.Connections() {
		if item.ID == r.PathValue("id") {
			copy := item
			c = &copy
			break
		}
	}
	if c == nil {
		writeError(w, 404, "not_found", "Connection not found")
		return
	}
	if decision := s.authorize(r, policy.InfraProvision, policy.ConnectionResource(c.ID)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if c.Type != "prefect" && c.Type != "openfaas" {
		writeError(w, 422, "monitor_only", "This connection monitors availability. Configure its deployment settings to change its runtime.")
		return
	}
	if _, err := connectionToken(*c); err != nil {
		writeError(w, 422, "credential_unavailable", err.Error())
		return
	}
	for _, run := range s.store.Runs() {
		if run.Status == "queued" || run.Status == "running" {
			writeError(w, 409, "runs_active", "Wait for running and queued pipelines to finish before changing runtimes.")
			return
		}
	}
	if err := checkConnection(r, *c); err != nil {
		writeError(w, 422, "connection_failed", err.Error())
		return
	}
	item, err := s.store.ActivateConnection(c.ID, actor(r))
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
