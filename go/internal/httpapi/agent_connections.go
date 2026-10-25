package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/store"
)

// Agent API connections are administrator-defined operations, not arbitrary
// HTTP URLs supplied by a flow. The gateway resolves credentials at call time.
const agentConnectionKind = "agent_api_connection"

var agentConnectionHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type agentAPIConnection struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Endpoint    string    `json:"endpoint"`
	SecretRef   string    `json:"secret_ref"`
	CreatedAt   time.Time `json:"created_at"`
}
type agentConnectionSummary struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (c agentAPIConnection) summary() agentConnectionSummary {
	return agentConnectionSummary{ID: c.ID, ProjectID: c.ProjectID, Name: c.Name, Description: c.Description}
}
func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/admin/agent-api-connections", s.createAgentAPIConnection)
		mux.HandleFunc("GET /api/v1/agent-api-connections", s.agentAPIConnections)
		mux.HandleFunc("POST /api/v1/agent-api-connections/{id}/invoke", s.invokeAgentAPIConnection)
	})
}
func (s *Server) createAgentAPIConnection(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var connection agentAPIConnection
	if err := decode(r, &connection); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	target, err := url.Parse(connection.Endpoint)
	if connection.ProjectID == "" || !projectAllowed(s.store, principal(r), connection.ProjectID) {
		writeError(w, 403, "forbidden", "Project is not assigned")
		return
	}
	if !connectionName.MatchString(connection.Name) || len(connection.Description) > 500 ||
		err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" ||
		target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		(connection.SecretRef != "none" && !knowledgeRef.MatchString(connection.SecretRef)) {
		writeError(w, 422, "invalid_request", "Use a named HTTP(S) endpoint without URL credentials or query, and none or an env: credential reference")
		return
	}
	for _, current := range store.ListDocs[agentAPIConnection](s.store, agentConnectionKind) {
		if current.ProjectID == connection.ProjectID && current.Name == connection.Name {
			writeError(w, 409, "already_exists", "An API connection with this name already exists in the project")
			return
		}
	}
	connection.ID = "api-" + knowledgeID()
	connection.CreatedAt = time.Now().UTC()
	saved, err := store.UpdateDoc(s.store, agentConnectionKind, connection.ID, func(_ agentAPIConnection, exists bool) (agentAPIConnection, error) {
		if exists {
			return agentAPIConnection{}, store.ErrConflict
		}
		return connection, nil
	}, "agent_api_connection.created", actor(r))
	writeMutation(w, saved.summary(), err, http.StatusCreated)
}
func (s *Server) agentAPIConnections(w http.ResponseWriter, r *http.Request) {
	items := []agentConnectionSummary{}
	for _, connection := range store.ListDocs[agentAPIConnection](s.store, agentConnectionKind) {
		if projectAllowed(s.store, principal(r), connection.ProjectID) {
			items = append(items, connection.summary())
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}
func (s *Server) invokeAgentAPIConnection(w http.ResponseWriter, r *http.Request) {
	connection, err := store.GetDoc[agentAPIConnection](s.store, agentConnectionKind, r.PathValue("id"))
	if err != nil || !projectAllowed(s.store, principal(r), connection.ProjectID) {
		writeError(w, 404, "not_found", "API connection not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var payload json.RawMessage
	if err := decode(r, &payload); err != nil || !json.Valid(payload) {
		writeError(w, 400, "invalid_request", "Provide a JSON request body")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, connection.Endpoint, bytes.NewReader(payload))
	if err != nil {
		writeError(w, 503, "connection_unavailable", "API connection is unavailable")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if connection.SecretRef != "none" {
		token := os.Getenv(strings.TrimPrefix(connection.SecretRef, "env:"))
		if token == "" {
			writeError(w, 503, "credential_unavailable", "API connection credential is unavailable")
			return
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := agentConnectionHTTP.Do(request)
	if err != nil {
		writeError(w, 503, "connection_unavailable", "API connection is unavailable")
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		writeError(w, 502, "invalid_response", "API connection returned an oversized response")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, 502, "upstream_error", "API connection returned an error")
		return
	}
	if json.Valid(body) {
		writeJSON(w, 200, map[string]any{"result": json.RawMessage(body)})
	} else {
		writeJSON(w, 200, map[string]any{"text": string(body)})
	}
}
