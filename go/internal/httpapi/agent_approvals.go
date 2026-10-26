package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/redact"
	"github.com/kiongahq/platform/internal/store"
)

// Human approval requests created by approval nodes. Only machine identities
// request them: a designer run credential (playground and evaluation runs)
// or a deployed agent's project identity through its kionga-api helper.
// Only signed-in people decide them: the requesting run or agent identity can
// never approve itself.
const agentApprovalKind = "agent_approval"

var approvalID = regexp.MustCompile(`^apr-[a-f0-9]{24}$`)

type agentApproval struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	RunID       string     `json:"run_id,omitempty"`
	NodeID      string     `json:"node_id"`
	Message     string     `json:"message"`
	Source      string     `json:"source"`
	RequestedBy string     `json:"requested_by"`
	Status      string     `json:"status"`
	DecidedBy   string     `json:"decided_by,omitempty"`
	Comment     string     `json:"comment,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
}

// view reports a pending request past its deadline as expired.
func (a agentApproval) view() agentApproval {
	if a.Status == "pending" && time.Now().After(a.ExpiresAt) {
		a.Status = "expired"
	}
	return a
}

func machineCredential(value auth.Principal) bool {
	return value.Credential == designRunCredential || value.Credential == "workspace"
}

func (s *Server) createApproval(w http.ResponseWriter, r *http.Request) {
	caller := principal(r)
	if !machineCredential(caller) || caller.WorkspaceProjectID == "" {
		writeError(w, 403, "access_denied", "Approval requests come from agent runs, not people.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		RunID          string `json:"run_id"`
		NodeID         string `json:"node_id"`
		Message        string `json:"message"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	source := "deployed_agent"
	if caller.Credential == designRunCredential {
		source = "designer_run"
		if req.RunID != verifiedRunID(r) {
			writeError(w, 403, "access_denied", "A run can request approvals only for itself.")
			return
		}
	} else {
		req.RunID = ""
	}
	if !designIDPattern.MatchString(req.NodeID) || len([]rune(req.Message)) > 2000 || req.TimeoutSeconds < 1 || req.TimeoutSeconds > 290 {
		writeError(w, 422, "validation_error", "Send a node ID, a message of at most 2000 characters and a timeout of 1-290 seconds.")
		return
	}
	id, err := newOpaqueID("apr-")
	if err != nil {
		writeError(w, 500, "internal_error", err.Error())
		return
	}
	now := time.Now().UTC()
	item := agentApproval{ID: id, ProjectID: caller.WorkspaceProjectID, RunID: req.RunID, NodeID: req.NodeID,
		Message: redact.String(req.Message), Source: source, RequestedBy: caller.Subject, Status: "pending",
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(req.TimeoutSeconds) * time.Second)}
	saved, err := store.UpdateDoc(s.store, agentApprovalKind, id, func(_ agentApproval, exists bool) (agentApproval, error) {
		if exists {
			return agentApproval{}, store.ErrConflict
		}
		return item, nil
	}, "agent_approval.requested", actor(r))
	writeMutation(w, saved, err, 201)
}

// approvalFor loads a request the caller may see: its own run, a deployed
// agent's project identity, or a person with access to the project.
func (s *Server) approvalFor(r *http.Request) (agentApproval, bool) {
	id := r.PathValue("id")
	if !approvalID.MatchString(id) {
		return agentApproval{}, false
	}
	item, err := store.GetDoc[agentApproval](s.store, agentApprovalKind, id)
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		return agentApproval{}, false
	}
	if principal(r).Credential == designRunCredential && item.RunID != verifiedRunID(r) {
		return agentApproval{}, false
	}
	return item, true
}

func (s *Server) approval(w http.ResponseWriter, r *http.Request) {
	item, ok := s.approvalFor(r)
	if !ok {
		writeError(w, 404, "not_found", "approval not found")
		return
	}
	writeJSON(w, 200, item.view())
}

func (s *Server) approvals(w http.ResponseWriter, r *http.Request) {
	if machineCredential(principal(r)) {
		writeError(w, 403, "access_denied", "Agent identities cannot list approvals.")
		return
	}
	status, projectID, runID := r.URL.Query().Get("status"), r.URL.Query().Get("project_id"), r.URL.Query().Get("run_id")
	items := []agentApproval{}
	for _, item := range store.ListDocs[agentApproval](s.store, agentApprovalKind) {
		item = item.view()
		if !projectAllowed(s.store, principal(r), item.ProjectID) || (projectID != "" && item.ProjectID != projectID) ||
			(status != "" && item.Status != status) || (runID != "" && item.RunID != runID) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > 200 {
		items = items[:200]
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

var errApprovalClosed = errors.New("approval closed")

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	caller := principal(r)
	if machineCredential(caller) || caller.Credential == "api_token" {
		writeError(w, 403, "access_denied", "Only a signed-in person can decide an approval.")
		return
	}
	item, ok := s.approvalFor(r)
	if !ok {
		writeError(w, 404, "not_found", "approval not found")
		return
	}
	if !auth.Allowed(caller, http.MethodPost, "/api/v1/agents") {
		writeError(w, 403, "access_denied", "Deciding approvals needs agents write access in the project.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	status := map[string]string{"approve": "approved", "reject": "rejected"}[req.Decision]
	if status == "" || len([]rune(req.Comment)) > 1000 {
		writeError(w, 422, "validation_error", "decision must be approve or reject, with a comment of at most 1000 characters")
		return
	}
	updated, err := store.UpdateDoc(s.store, agentApprovalKind, item.ID, func(current agentApproval, exists bool) (agentApproval, error) {
		if !exists {
			return agentApproval{}, store.ErrNotFound
		}
		if current.view().Status != "pending" {
			return agentApproval{}, errApprovalClosed
		}
		now := time.Now().UTC()
		current.Status, current.DecidedBy, current.Comment, current.DecidedAt = status, caller.Subject, strings.TrimSpace(req.Comment), &now
		return current, nil
	}, "agent_approval."+status, actor(r))
	if errors.Is(err, errApprovalClosed) {
		writeError(w, 409, "approval_closed", "This request was already decided or has expired.")
		return
	}
	writeMutation(w, updated, err, 200)
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/agent-approvals", s.approvals)
		mux.HandleFunc("POST /api/v1/agent-approvals", s.createApproval)
		mux.HandleFunc("GET /api/v1/agent-approvals/{id}", s.approval)
		mux.HandleFunc("POST /api/v1/agent-approvals/{id}/decision", s.decideApproval)
	})
}
