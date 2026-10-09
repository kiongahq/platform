package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ml-ai-ops/platform/internal/pipelinespec"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/pipelines/validate", s.validatePipelineDefinition)
		mux.HandleFunc("POST /api/v1/pipelines/definitions/yaml", s.saveDefinitionYAML)
		mux.HandleFunc("PUT /api/v1/pipelines/definitions/{id}/yaml", s.saveDefinitionYAML)
		mux.HandleFunc("GET /api/v1/pipelines/definitions/{id}/yaml", s.definitionYAML)
		mux.HandleFunc("GET /api/v1/pipelines/definitions/{id}/revisions", s.definitionRevisions)
		mux.HandleFunc("GET /api/v1/pipelines/definitions/{id}/revisions/{revision}", s.definitionRevision)
		mux.HandleFunc("POST /api/v1/pipelines/definitions/{id}/revisions/{revision}/rollback", s.rollbackDefinition)
		mux.HandleFunc("POST /api/v1/pipelines/definitions/{id}/triggers/{index}/{action}", s.setTriggerPaused)
	})
}

// validationResponse is returned by the validator and by failed saves, so
// the form and YAML editors show the same issues tied to the same fields.
type validationResponse struct {
	Valid   bool                                `json:"valid"`
	Issues  pipelinespec.Issues                 `json:"issues"`
	YAML    string                              `json:"yaml,omitempty"`
	SHA256  string                              `json:"sha256,omitempty"`
	Request api.UpsertPipelineDefinitionRequest `json:"request"`
	Layers  [][]string                          `json:"layers,omitempty"`
}

func writeIssues(w http.ResponseWriter, issues pipelinespec.Issues) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_error", "message": issues.Error(), "details": issues})
}

// validatePipelineDefinition accepts {"yaml": "..."} or a definition request
// and returns the canonical YAML, its hash, layers, and every issue.
func (s *Server) validatePipelineDefinition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML    string                               `json:"yaml"`
		Request *api.UpsertPipelineDefinitionRequest `json:"request"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req api.UpsertPipelineDefinitionRequest
	var issues pipelinespec.Issues
	switch {
	case body.YAML != "":
		req, issues = pipelinespec.ParseAndValidate([]byte(body.YAML))
	case body.Request != nil:
		req, issues = pipelinespec.Validate(*body.Request)
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "send yaml or request")
		return
	}
	response := validationResponse{Valid: len(issues) == 0, Issues: issues, Request: req}
	if response.Issues == nil {
		response.Issues = pipelinespec.Issues{}
	}
	if sha, text, err := pipelinespec.SpecHash(req); err == nil {
		response.YAML, response.SHA256 = string(text), sha
	}
	if response.Valid {
		response.Layers = pipelinespec.Layers(req.Jobs)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) saveDefinitionYAML(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML    string `json:"yaml"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req, issues := pipelinespec.ParseAndValidate([]byte(body.YAML))
	if len(issues) > 0 {
		writeIssues(w, issues)
		return
	}
	req.Message = body.Message
	s.saveDefinition(w, r, req)
}

// saveDefinition is the single persistence path for form, JSON and YAML
// editors: authorization, capacity and function checks, then the store.
func (s *Server) saveDefinition(w http.ResponseWriter, r *http.Request, req api.UpsertPipelineDefinitionRequest) {
	definitionID := r.PathValue("id")
	if definitionID != "" {
		existing, err := s.store.PipelineDefinition(definitionID)
		if err != nil || !projectAllowed(s.store, principal(r), existing.ProjectID) {
			writeError(w, http.StatusNotFound, "not_found", "pipeline definition not found")
			return
		}
		if existing.ProjectID != req.ProjectID {
			writeIssues(w, pipelinespec.Issues{{Path: "metadata.project", Message: "a definition cannot move to another project; create a new one instead"}})
			return
		}
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	if err := enforcePipelineResources(s.store, principal(r), req.Jobs); err != nil {
		writeError(w, http.StatusForbidden, "resource_not_provisioned", err.Error())
		return
	}
	registered := map[string]bool{}
	for _, function := range s.store.Functions() {
		if function.ProjectID == req.ProjectID {
			registered[function.Name] = true
		}
	}
	var issues pipelinespec.Issues
	for i, job := range req.Jobs {
		if job.Kind == "function" && !registered[job.Function] {
			issues = append(issues, pipelinespec.Issue{Path: "spec.nodes[" + strconv.Itoa(i) + "].function", Node: job.Name, Message: "function " + job.Function + " is not deployed in this project"})
		}
	}
	if len(issues) > 0 {
		writeIssues(w, issues)
		return
	}
	item, err := s.store.UpsertPipelineDefinition(definitionID, req, actor(r))
	var validation pipelinespec.Issues
	if errors.As(err, &validation) {
		writeIssues(w, validation)
		return
	}
	status := http.StatusCreated
	if definitionID != "" {
		status = http.StatusOK
	}
	writeMutation(w, item, err, status)
}

func (s *Server) allowedDefinition(w http.ResponseWriter, r *http.Request) (api.PipelineDefinition, bool) {
	definition, err := s.store.PipelineDefinition(r.PathValue("id"))
	if err != nil || !projectAllowed(s.store, principal(r), definition.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "pipeline definition not found")
		return definition, false
	}
	return definition, true
}

func (s *Server) definitionYAML(w http.ResponseWriter, r *http.Request) {
	definition, ok := s.allowedDefinition(w, r)
	if !ok {
		return
	}
	if revision, err := store.PipelineRevision(s.store, definition.ID, definition.Revision); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"yaml": revision.YAML, "sha256": revision.SHA256, "revision": revision.Revision})
		return
	}
	// Legacy definitions saved before revisions existed: render on demand.
	text, err := pipelinespec.Render(definitionRequest(definition))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"yaml": string(text), "sha256": pipelinespec.Hash(text), "revision": definition.Revision})
}

func (s *Server) definitionRevisions(w http.ResponseWriter, r *http.Request) {
	definition, ok := s.allowedDefinition(w, r)
	if !ok {
		return
	}
	revisions := store.PipelineRevisions(s.store, definition.ID)
	writeJSON(w, http.StatusOK, map[string]any{"items": revisions, "total": len(revisions), "current": definition.Revision})
}

func (s *Server) definitionRevision(w http.ResponseWriter, r *http.Request) {
	definition, ok := s.allowedDefinition(w, r)
	if !ok {
		return
	}
	number, err := store.ParseRevision(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	revision, err := store.PipelineRevision(s.store, definition.ID, number)
	writeMutation(w, revision, err, http.StatusOK)
}

// rollbackDefinition saves an old revision's spec as a new revision. History
// is never rewritten, so runs keep pointing at what they actually executed.
func (s *Server) rollbackDefinition(w http.ResponseWriter, r *http.Request) {
	definition, ok := s.allowedDefinition(w, r)
	if !ok {
		return
	}
	number, err := store.ParseRevision(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	revision, err := store.PipelineRevision(s.store, definition.ID, number)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "revision not found")
		return
	}
	req := revision.Spec
	req.Message = "Roll back to revision " + strconv.Itoa(number)
	s.saveDefinition(w, r, req)
}

func (s *Server) setTriggerPaused(w http.ResponseWriter, r *http.Request) {
	definition, ok := s.allowedDefinition(w, r)
	if !ok {
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	action := r.PathValue("action")
	if err != nil || index < 0 || index >= len(definition.Triggers) || (action != "pause" && action != "resume") {
		writeError(w, http.StatusNotFound, "not_found", "trigger not found")
		return
	}
	if definition.Triggers[index].Type != "schedule" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "only schedule triggers can be paused")
		return
	}
	req := definitionRequest(definition)
	req.Triggers[index].Paused = action == "pause"
	req.Message = "Schedule " + map[string]string{"pause": "paused", "resume": "resumed"}[action] + " at " + time.Now().UTC().Format(time.RFC3339)
	s.saveDefinition(w, r, req)
}

// definitionRequest reconstructs the request that would produce definition.
func definitionRequest(definition api.PipelineDefinition) api.UpsertPipelineDefinitionRequest {
	triggers := make([]api.PipelineTrigger, len(definition.Triggers))
	for i, trigger := range definition.Triggers {
		triggers[i] = api.PipelineTrigger{Type: trigger.Type, Cron: trigger.Cron, Timezone: trigger.Timezone, Paused: trigger.Paused, Topic: trigger.Topic}
	}
	return api.UpsertPipelineDefinitionRequest{
		ProjectID: definition.ProjectID, Name: definition.Name, Version: definition.Version, Description: definition.Description,
		ExecutionMode: definition.ExecutionMode, Jobs: definition.Jobs, RepositoryURL: definition.RepositoryURL, CommitSHA: definition.CommitSHA,
		Triggers: triggers, Overridable: definition.Overridable, Parameters: definition.Parameters,
	}
}
