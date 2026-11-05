package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
)

// Agent definitions and teams are project-scoped documents with a revision
// history. Runs copy them into a snapshot, so edits here never reach a run
// already in progress.
const (
	agentDefinitionKind = "agent_definition"
	agentTeamKind       = "agent_team"
	maxResourceRevision = 20
)

var (
	agentDefinitionID = regexp.MustCompile(`^adf-[a-f0-9]{24}$`)
	agentTeamID       = regexp.MustCompile(`^atm-[a-f0-9]{24}$`)
)

// versioned is a stored resource: server fields plus flattened content.
type versioned[C any] struct {
	ID           string
	ProjectID    string
	Revision     int
	OwnerSubject string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Content      C
	History      []revisionEntry[C]
}

type revisionEntry[C any] struct {
	Revision int       `json:"revision"`
	SavedAt  time.Time `json:"saved_at"`
	Content  C         `json:"content"`
}

type versionedMeta struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	Revision     int             `json:"revision"`
	OwnerSubject string          `json:"owner_subject"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	History      json.RawMessage `json:"history,omitempty"`
}

func (v versioned[C]) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(v.Content)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["id"], fields["project_id"], fields["revision"] = v.ID, v.ProjectID, v.Revision
	fields["owner_subject"], fields["created_at"], fields["updated_at"] = v.OwnerSubject, v.CreatedAt, v.UpdatedAt
	if len(v.History) > 0 {
		fields["history"] = v.History
	}
	return json.Marshal(fields)
}

func (v *versioned[C]) UnmarshalJSON(raw []byte) error {
	var meta versionedMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &v.Content); err != nil {
		return err
	}
	v.ID, v.ProjectID, v.Revision, v.OwnerSubject, v.CreatedAt, v.UpdatedAt = meta.ID, meta.ProjectID, meta.Revision, meta.OwnerSubject, meta.CreatedAt, meta.UpdatedAt
	v.History = nil
	if len(meta.History) > 0 {
		return json.Unmarshal(meta.History, &v.History)
	}
	return nil
}

// view is what the API returns: never the history, which has its own route.
func (v versioned[C]) view() versioned[C] {
	v.History = nil
	return v
}

type (
	agentDefinitionDoc = versioned[agentteam.DefinitionContent]
	agentTeamDoc       = versioned[agentteam.TeamContent]
)

// resourceSpec adapts the shared CRUD handlers to one resource.
type resourceSpec[C any] struct {
	kind, prefix, audit, label string
	id                         *regexp.Regexp
	defaults                   func(C) C
	validate                   func(s *Server, caller auth.Principal, projectID, selfID string, content C) []string
	name                       func(C) string
	inUse                      func(s *Server, doc versioned[C]) bool
}

// decodeResource reads a closed body: content fields plus project_id and revision.
func decodeResource[C any](w http.ResponseWriter, r *http.Request) (content C, projectID string, revision int, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := requireJSON(r); err != nil {
		writeDecodeError(w, err)
		return content, "", 0, false
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeDecodeError(w, err)
		return content, "", 0, false
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		writeError(w, 400, "invalid_request", "Send a JSON object")
		return content, "", 0, false
	}
	if value, present := fields["project_id"]; present {
		if err := json.Unmarshal(value, &projectID); err != nil {
			writeError(w, 400, "invalid_request", "project_id must be a string")
			return content, "", 0, false
		}
	}
	if value, present := fields["revision"]; present {
		if err := json.Unmarshal(value, &revision); err != nil {
			writeError(w, 400, "invalid_request", "revision must be an integer")
			return content, "", 0, false
		}
	}
	delete(fields, "project_id")
	delete(fields, "revision")
	stripped, err := json.Marshal(fields)
	if err != nil {
		writeError(w, 400, "invalid_request", "Send a JSON object")
		return content, "", 0, false
	}
	decoder := json.NewDecoder(bytesReader(stripped))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&content); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return content, "", 0, false
	}
	return content, projectID, revision, true
}

func writeTeamIssues(w http.ResponseWriter, issues []string) {
	writeJSON(w, 422, map[string]any{"error": "validation_error", "message": issues[0], "issues": issues})
}

func (spec resourceSpec[C]) load(s *Server, r *http.Request) (versioned[C], bool) {
	id := r.PathValue("id")
	if !spec.id.MatchString(id) {
		return versioned[C]{}, false
	}
	doc, err := store.GetDoc[versioned[C]](s.store, spec.kind, id)
	if err != nil || !projectAllowed(s.store, principal(r), doc.ProjectID) {
		return versioned[C]{}, false
	}
	return doc, true
}

func (spec resourceSpec[C]) nameTaken(s *Server, projectID, name, selfID string) bool {
	for _, doc := range store.ListDocs[versioned[C]](s.store, spec.kind) {
		if doc.ProjectID == projectID && doc.ID != selfID && spec.name(doc.Content) == name {
			return true
		}
	}
	return false
}

func (spec resourceSpec[C]) list(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wanted := r.URL.Query().Get("project_id")
		items := []versioned[C]{}
		for _, doc := range store.ListDocs[versioned[C]](s.store, spec.kind) {
			if projectAllowed(s.store, principal(r), doc.ProjectID) && (wanted == "" || doc.ProjectID == wanted) {
				items = append(items, doc.view())
			}
		}
		writeJSON(w, 200, map[string]any{"items": items})
	}
}

func (spec resourceSpec[C]) get(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		doc, ok := spec.load(s, r)
		if !ok {
			writeError(w, 404, "not_found", spec.label+" not found")
			return
		}
		writeJSON(w, 200, doc.view())
	}
}

func (spec resourceSpec[C]) create(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := principal(r)
		if machineCredential(caller) {
			writeError(w, 403, "access_denied", "Agent identities cannot change team resources.")
			return
		}
		content, projectID, _, ok := decodeResource[C](w, r)
		if !ok {
			return
		}
		if projectID == "" || !projectAllowed(s.store, caller, projectID) {
			writeError(w, 403, "access_denied", "project is not assigned to this user")
			return
		}
		if _, err := s.store.Project(projectID); err != nil {
			writeError(w, 404, "not_found", "project not found")
			return
		}
		content = spec.defaults(content)
		if issues := spec.validate(s, caller, projectID, "", content); len(issues) > 0 {
			writeTeamIssues(w, issues)
			return
		}
		if spec.nameTaken(s, projectID, spec.name(content), "") {
			writeError(w, 409, "already_exists", "This name already exists in the project")
			return
		}
		id, err := newOpaqueID(spec.prefix)
		if err != nil {
			writeError(w, 500, "internal_error", "could not create an ID")
			return
		}
		now := time.Now().UTC()
		doc := versioned[C]{ID: id, ProjectID: projectID, Revision: 1, OwnerSubject: caller.Subject, CreatedAt: now, UpdatedAt: now, Content: content}
		saved, err := store.UpdateDoc(s.store, spec.kind, id, func(_ versioned[C], exists bool) (versioned[C], error) {
			if exists {
				return versioned[C]{}, store.ErrConflict
			}
			return doc, nil
		}, spec.audit+".created", actor(r))
		if err != nil {
			writeError(w, 500, "save_failed", "could not save")
			return
		}
		writeJSON(w, 201, saved.view())
	}
}

func (spec resourceSpec[C]) update(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := principal(r)
		if machineCredential(caller) {
			writeError(w, 403, "access_denied", "Agent identities cannot change team resources.")
			return
		}
		existing, ok := spec.load(s, r)
		if !ok {
			writeError(w, 404, "not_found", spec.label+" not found")
			return
		}
		content, projectID, revision, ok := decodeResource[C](w, r)
		if !ok {
			return
		}
		if projectID != "" && projectID != existing.ProjectID {
			writeError(w, 422, "validation_error", "project cannot be changed")
			return
		}
		content = spec.defaults(content)
		if issues := spec.validate(s, caller, existing.ProjectID, existing.ID, content); len(issues) > 0 {
			writeTeamIssues(w, issues)
			return
		}
		if spec.nameTaken(s, existing.ProjectID, spec.name(content), existing.ID) {
			writeError(w, 409, "already_exists", "This name already exists in the project")
			return
		}
		saved, err := store.UpdateDoc(s.store, spec.kind, existing.ID, func(current versioned[C], exists bool) (versioned[C], error) {
			if !exists {
				return current, store.ErrNotFound
			}
			if current.Revision != revision {
				return current, store.ErrConflict
			}
			current.History = append(current.History, revisionEntry[C]{Revision: current.Revision, SavedAt: current.UpdatedAt, Content: current.Content})
			if len(current.History) > maxResourceRevision-1 {
				current.History = current.History[len(current.History)-(maxResourceRevision-1):]
			}
			current.Content = content
			current.Revision++
			current.UpdatedAt = time.Now().UTC()
			return current, nil
		}, spec.audit+".updated", actor(r))
		switch {
		case errors.Is(err, store.ErrConflict):
			latest, _ := store.GetDoc[versioned[C]](s.store, spec.kind, existing.ID)
			writeJSON(w, 409, map[string]any{"error": "revision_conflict", "message": spec.label + " changed elsewhere; reload before saving", "latest": latest.view()})
		case errors.Is(err, store.ErrNotFound):
			writeError(w, 404, "not_found", spec.label+" not found")
		case err != nil:
			writeError(w, 500, "save_failed", "could not save")
		default:
			writeJSON(w, 200, saved.view())
		}
	}
}

func (spec resourceSpec[C]) remove(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if machineCredential(principal(r)) {
			writeError(w, 403, "access_denied", "Agent identities cannot change team resources.")
			return
		}
		doc, ok := spec.load(s, r)
		if !ok {
			writeError(w, 404, "not_found", spec.label+" not found")
			return
		}
		if spec.inUse != nil && spec.inUse(s, doc) {
			writeError(w, 409, "in_use", "A team still uses this agent; remove it from the team first.")
			return
		}
		if err := s.store.DeleteDocument(spec.kind, doc.ID, spec.audit+".deleted", actor(r)); err != nil {
			writeError(w, 404, "not_found", spec.label+" not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// revisions returns every kept revision, newest first, current included.
func (spec resourceSpec[C]) revisions(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		doc, ok := spec.load(s, r)
		if !ok {
			writeError(w, 404, "not_found", spec.label+" not found")
			return
		}
		items := []revisionEntry[C]{{Revision: doc.Revision, SavedAt: doc.UpdatedAt, Content: doc.Content}}
		items = append(items, doc.History...)
		sort.Slice(items, func(i, j int) bool { return items[i].Revision > items[j].Revision })
		writeJSON(w, 200, map[string]any{"items": items})
	}
}

func (spec resourceSpec[C]) register(s *Server, mux *http.ServeMux, route string) {
	mux.HandleFunc("GET "+route, spec.list(s))
	mux.HandleFunc("POST "+route, spec.create(s))
	mux.HandleFunc("GET "+route+"/{id}", spec.get(s))
	mux.HandleFunc("PUT "+route+"/{id}", spec.update(s))
	mux.HandleFunc("DELETE "+route+"/{id}", spec.remove(s))
	mux.HandleFunc("GET "+route+"/{id}/revisions", spec.revisions(s))
}

// grantResourceKind maps a grant to the resource checker's vocabulary.
func grantResourceKind(kind string) string {
	if kind == "knowledge" {
		return "knowledge_base"
	}
	return kind
}

var definitionSpec = resourceSpec[agentteam.DefinitionContent]{
	kind: agentDefinitionKind, prefix: "adf-", audit: "agent_definition", label: "agent definition", id: agentDefinitionID,
	defaults: agentteam.DefaultDefinition,
	name:     func(c agentteam.DefinitionContent) string { return c.Name },
	validate: func(s *Server, caller auth.Principal, projectID, _ string, content agentteam.DefinitionContent) []string {
		issues := agentteam.ValidateDefinition(content)
		check := s.resourceCheckerFor(caller, projectID)
		for i, grant := range content.Tools {
			switch grant.Kind {
			case "tool", "connection", "knowledge", "dataset":
				if !check(grantResourceKind(grant.Kind), grant.Ref) {
					issues = append(issues, fmt.Sprintf("tools[%d]: %s %s is not available in this project", i, grant.Kind, grant.Ref))
				}
			case "git":
				if grant.Connection != "" && !check("connection", grant.Connection) {
					issues = append(issues, fmt.Sprintf("tools[%d]: connection %s is not available in this project", i, grant.Connection))
				}
			}
		}
		return issues
	},
	inUse: func(s *Server, doc agentDefinitionDoc) bool {
		for _, team := range store.ListDocs[agentTeamDoc](s.store, agentTeamKind) {
			for _, member := range team.Content.Members {
				if member == doc.ID {
					return true
				}
			}
		}
		return false
	},
}

var teamSpec = resourceSpec[agentteam.TeamContent]{
	kind: agentTeamKind, prefix: "atm-", audit: "agent_team", label: "team", id: agentTeamID,
	defaults: agentteam.DefaultTeam,
	name:     func(c agentteam.TeamContent) string { return c.Name },
	validate: func(s *Server, _ auth.Principal, projectID, _ string, content agentteam.TeamContent) []string {
		issues := agentteam.ValidateTeam(content)
		for i, id := range content.Members {
			member, err := store.GetDoc[agentDefinitionDoc](s.store, agentDefinitionKind, id)
			if err != nil || member.ProjectID != projectID {
				issues = append(issues, "members["+strconv.Itoa(i)+"]: agent definition not found in this project")
			}
		}
		return issues
	},
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		definitionSpec.register(s, mux, "/api/v1/agent-definitions")
		teamSpec.register(s, mux, "/api/v1/agent-teams")
	})
}
