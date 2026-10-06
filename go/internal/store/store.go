package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ml-ai-ops/platform/pkg/api"
)

var ErrNotFound = errors.New("resource not found")
var ErrConflict = errors.New("resource already exists")

type state struct {
	HubAccounts    map[string]HubAccount    `json:"hub_accounts,omitempty"`
	UserAccess     []api.UserAccess         `json:"user_access"`
	AccessRequests []api.AccessRequest      `json:"access_requests"`
	APITokens      []api.APIToken           `json:"api_tokens"`
	BlogPosts      []api.BlogPost           `json:"blog_posts"`
	Projects       []api.Project            `json:"projects"`
	Runs           []api.PipelineRun        `json:"runs"`
	Definitions    []api.PipelineDefinition `json:"pipeline_definitions"`
	Functions      []api.Function           `json:"functions"`
	Models         []api.Model              `json:"models"`
	Agents         []api.Agent              `json:"agents"`
	Tools          []api.Tool               `json:"tools"`
	Connections    []api.Connection         `json:"connections"`
	Audit          []api.AuditEvent         `json:"audit"`
	Sessions       []api.AgentSession       `json:"sessions"`
	Traces         []api.AgentTrace         `json:"traces"`
	Features       []api.FeatureView        `json:"features"`
}

func (s *Store) UserAccess() []api.UserAccess {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.UserAccess)
}

func (s *Store) AccessFor(subject string) (api.UserAccess, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, access := range s.data.UserAccess {
		if access.Subject == subject {
			return access, nil
		}
	}
	return api.UserAccess{}, ErrNotFound
}

func (s *Store) UpsertUserAccess(subject string, req api.UpsertUserAccessRequest, actor string) (api.UserAccess, error) {
	access, err := validateAccess(subject, req)
	if err != nil {
		return api.UserAccess{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	access.UpdatedAt = now
	for i := range s.data.UserAccess {
		if s.data.UserAccess[i].Subject == subject {
			access.CreatedAt = s.data.UserAccess[i].CreatedAt
			s.data.UserAccess[i] = access
			s.record("access.updated", "user_access", subject, actor, nil)
			return access, s.persist()
		}
	}
	access.CreatedAt = now
	s.data.UserAccess = append([]api.UserAccess{access}, s.data.UserAccess...)
	s.record("access.created", "user_access", subject, actor, nil)
	return access, s.persist()
}

func (s *Store) DeleteUserAccess(subject, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.UserAccess {
		if s.data.UserAccess[i].Subject == subject {
			s.data.UserAccess = append(s.data.UserAccess[:i], s.data.UserAccess[i+1:]...)
			s.record("access.deleted", "user_access", subject, actor, nil)
			return s.persist()
		}
	}
	return ErrNotFound
}

type Store struct {
	mu   sync.RWMutex
	path string
	data state
}

func New(path ...string) *Store {
	s := &Store{}
	if len(path) > 0 {
		s.path = path[0]
	}
	if s.path != "" {
		_ = s.load()
	}
	if len(s.data.Projects) == 0 {
		now := time.Now().UTC()
		s.data.Projects = []api.Project{{ID: "prj-demo", Name: "Fraud detection starter", Description: "A guided tabular ML project", Template: "tabular-classification", Namespace: "team-demo", Status: "ready", CreatedAt: now}}
		s.data.Runs = []api.PipelineRun{{ID: "run-demo", ProjectID: "prj-demo", Name: "training-pipeline", Status: "succeeded", Progress: 100, CreatedAt: now, UpdatedAt: now, Steps: defaultSteps("succeeded")}}
		_ = s.persist()
	}
	return s
}

func (s *Store) Projects() []api.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.Projects)
}
func (s *Store) PipelineDefinitions() []api.PipelineDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.Definitions)
}
func (s *Store) Functions() []api.Function {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.Functions)
}
func (s *Store) Runs() []api.PipelineRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePipelineRuns(s.data.Runs)
}
func (s *Store) Models() []api.Model { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.data.Models) }
func (s *Store) Agents() []api.Agent { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.data.Agents) }
func (s *Store) Tools() []api.Tool   { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.data.Tools) }
func (s *Store) Connections() []api.Connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.Connections)
}
func (s *Store) Audit() []api.AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.Audit)
}

func (s *Store) CreateProject(req api.CreateProjectRequest, actor ...string) (api.Project, error) {
	name := strings.TrimSpace(req.Name)
	if len(name) < 3 {
		return api.Project{}, errors.New("name must contain at least 3 characters")
	}
	template, err := api.NormalizeProjectRequest(&req)
	if err != nil {
		return api.Project{}, err
	}
	namespace := slug(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.data.Projects {
		if strings.EqualFold(v.Name, name) || v.Namespace == namespace {
			return api.Project{}, ErrConflict
		}
	}
	p := api.Project{
		ID: id("prj"), Name: name, Description: strings.TrimSpace(req.Description),
		Template: template.ID, TemplateVersion: template.Version, Framework: req.Framework,
		Accelerator: req.Accelerator, RequestedProfile: req.RequestedProfile,
		Capabilities:    append([]string(nil), template.Capabilities...),
		ScaffoldCommand: scaffoldCommand(namespace, template, req),
		Namespace:       namespace, Status: "ready", CreatedAt: time.Now().UTC(), OwnerSubject: req.OwnerSubject,
	}
	if strings.TrimSpace(req.RepositoryURL) != "" {
		repository, err := validateGitRepository(api.SetProjectRepositoryRequest{URL: req.RepositoryURL, DefaultBranch: req.DefaultBranch})
		if err != nil {
			return api.Project{}, err
		}
		p.Repository = &repository
	}
	s.data.Projects = append([]api.Project{p}, s.data.Projects...)
	s.record("project.created", "project", p.ID, first(actor), nil)
	return p, s.persist()
}

func scaffoldCommand(namespace string, template api.ProjectTemplate, req api.CreateProjectRequest) string {
	return fmt.Sprintf(
		"kionga scaffold %s --template %s --template-version %s --framework %s --accelerator %s --profile %s",
		namespace, template.ID, template.Version, req.Framework, req.Accelerator, req.RequestedProfile,
	)
}

func (s *Store) SubmitPipeline(req api.SubmitPipelineRequest, actor ...string) (api.PipelineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasProject(s.data.Projects, req.ProjectID) {
		return api.PipelineRun{}, ErrNotFound
	}
	steps, mode := defaultSteps("pending"), "prefect"
	if req.DefinitionID != "" {
		definition, err := pipelineDefinitionFrom(s.data.Definitions, req.DefinitionID)
		if err != nil || definition.ProjectID != req.ProjectID {
			return api.PipelineRun{}, ErrNotFound
		}
		steps, mode = stepsFromDefinition(definition), definition.ExecutionMode
		if strings.TrimSpace(req.Name) == "" {
			req.Name = definition.Name
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "training-pipeline"
	}
	now := time.Now().UTC()
	run := api.PipelineRun{ID: id("run"), ProjectID: req.ProjectID, Name: strings.TrimSpace(req.Name), Status: "queued", Progress: 0, CreatedAt: now, UpdatedAt: now, DefinitionID: req.DefinitionID, ExecutionMode: mode, Parameters: req.Parameters, Steps: steps, Logs: []api.RunLog{{Timestamp: now, Level: "info", Message: "Run accepted by control plane"}}}
	s.data.Runs = append([]api.PipelineRun{run}, s.data.Runs...)
	s.record("pipeline.submitted", "pipeline_run", run.ID, first(actor), nil)
	err := s.persist()
	return clonePipelineRun(run), err
}

func (s *Store) Run(runID string) (api.PipelineRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.data.Runs {
		if run.ID == runID {
			return clonePipelineRun(run), nil
		}
	}
	return api.PipelineRun{}, ErrNotFound
}

func (s *Store) CancelRun(runID, actor string) (api.PipelineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Runs {
		if s.data.Runs[i].ID == runID {
			if s.data.Runs[i].Status == "succeeded" || s.data.Runs[i].Status == "failed" {
				return api.PipelineRun{}, errors.New("completed runs cannot be cancelled")
			}
			s.data.Runs[i].Status, s.data.Runs[i].UpdatedAt = "cancelled", time.Now().UTC()
			s.data.Runs[i].Logs = append(s.data.Runs[i].Logs, api.RunLog{Timestamp: time.Now().UTC(), Level: "warning", Message: "Run cancelled by " + actor})
			s.record("pipeline.cancelled", "pipeline_run", runID, actor, nil)
			err := s.persist()
			return clonePipelineRun(s.data.Runs[i]), err
		}
	}
	return api.PipelineRun{}, ErrNotFound
}

func (s *Store) RetryRun(runID, actor string) (api.PipelineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, previous := range s.data.Runs {
		if previous.ID == runID {
			now := time.Now().UTC()
			steps := resetSteps(previous.Steps)
			run := api.PipelineRun{ID: id("run"), ProjectID: previous.ProjectID, Name: previous.Name, ParentRunID: previous.ID, Status: "queued", CreatedAt: now, UpdatedAt: now, DefinitionID: previous.DefinitionID, ExecutionMode: previous.ExecutionMode, Parameters: previous.Parameters, Steps: steps, Logs: []api.RunLog{{Timestamp: now, Level: "info", Message: "Retry created from " + previous.ID}}}
			s.data.Runs = append([]api.PipelineRun{run}, s.data.Runs...)
			s.record("pipeline.retried", "pipeline_run", run.ID, actor, map[string]any{"parent_run_id": previous.ID})
			err := s.persist()
			return clonePipelineRun(run), err
		}
	}
	return api.PipelineRun{}, ErrNotFound
}

// SetRunEngine links a control-plane run to its execution engine run id.
func (s *Store) SetRunEngine(runID, engineRunID string) (api.PipelineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Runs {
		if s.data.Runs[i].ID == runID {
			s.data.Runs[i].EngineRunID = engineRunID
			s.data.Runs[i].UpdatedAt = time.Now().UTC()
			err := s.persist()
			return clonePipelineRun(s.data.Runs[i]), err
		}
	}
	return api.PipelineRun{}, ErrNotFound
}

// UpdateRunStep applies a step transition reported by the executing pipeline
// and recomputes run status and progress deterministically.
func (s *Store) UpdateRunStep(runID string, req api.UpdateRunStepRequest, actor string) (api.PipelineRun, error) {
	if req.Step == "" || req.Status == "" {
		return api.PipelineRun{}, errors.New("step and status are required")
	}
	if !validStepStatus(req.Status) {
		return api.PipelineRun{}, fmt.Errorf("invalid step status %q", req.Status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Runs {
		if s.data.Runs[i].ID != runID {
			continue
		}
		run := &s.data.Runs[i]
		now := time.Now().UTC()
		level := "info"
		if req.Status == "failed" {
			level = "error"
		}
		run.Logs = append(run.Logs, api.RunLog{Timestamp: now, Step: req.Step, Level: level, Message: stepMessage(req)})
		if run.Status == "cancelled" || run.Status == "failed" || run.Status == "succeeded" {
			// Terminal runs keep their state; late reports are only logged.
			err := s.persist()
			return clonePipelineRun(*run), err
		}
		applyStepTransition(run, req)
		run.UpdatedAt = now
		err := s.persist()
		return clonePipelineRun(*run), err
	}
	return api.PipelineRun{}, ErrNotFound
}

func validStepStatus(status string) bool {
	switch status {
	case "pending", "running", "succeeded", "failed", "skipped":
		return true
	}
	return false
}

func stepMessage(req api.UpdateRunStepRequest) string {
	if req.Message != "" {
		return req.Message
	}
	return "step " + req.Step + " " + req.Status
}

// applyStepTransition upserts the step and recomputes run status/progress:
// any failed step fails the run, any running step keeps it running, and the
// run succeeds only when every step has finished.
func applyStepTransition(run *api.PipelineRun, req api.UpdateRunStepRequest) {
	found := false
	for i := range run.Steps {
		if run.Steps[i].Name == req.Step {
			run.Steps[i].Status = req.Status
			if req.Status == "succeeded" || req.Status == "skipped" {
				run.Steps[i].Progress = 100
			}
			found = true
			break
		}
	}
	if !found {
		step := api.PipelineStep{Name: req.Step, Status: req.Status}
		if req.Status == "succeeded" || req.Status == "skipped" {
			step.Progress = 100
		}
		run.Steps = append(run.Steps, step)
	}
	completed, failed, running := 0, 0, 0
	for _, step := range run.Steps {
		switch step.Status {
		case "succeeded", "skipped":
			completed++
		case "failed":
			failed++
		case "running":
			running++
		}
	}
	if len(run.Steps) > 0 {
		run.Progress = completed * 100 / len(run.Steps)
	}
	switch {
	case failed > 0:
		run.Status = "failed"
	case completed == len(run.Steps):
		run.Status, run.Progress = "succeeded", 100
	case running > 0 || completed > 0:
		run.Status = "running"
	}
}

func (s *Store) RegisterModel(req api.RegisterModelRequest, actor string) (api.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)
	req.ArtifactURI = strings.TrimSpace(req.ArtifactURI)
	servingImage, err := normalizeServingImage(req.ServingImage)
	if err != nil {
		return api.Model{}, err
	}
	if !hasProject(s.data.Projects, req.ProjectID) {
		return api.Model{}, ErrNotFound
	}
	if req.Name == "" || req.Version == "" || req.ArtifactURI == "" {
		return api.Model{}, errors.New("name, version and artifact_uri are required")
	}
	for _, v := range s.data.Models {
		if v.ProjectID == req.ProjectID && v.Name == req.Name && v.Version == req.Version {
			return api.Model{}, ErrConflict
		}
	}
	gate := "passed"
	if strings.HasPrefix(req.ArtifactURI, "hf://") {
		gate = "needs_evaluation"
	}
	if accuracy, ok := req.Metrics["accuracy"]; ok && accuracy < .8 {
		gate = "failed"
	}
	m := api.Model{ID: id("mdl"), ProjectID: req.ProjectID, Name: req.Name, Version: req.Version, Stage: "candidate", ArtifactURI: req.ArtifactURI, ServingImage: servingImage, Metrics: req.Metrics, GateStatus: gate, DeploymentStatus: "not_deployed", CreatedAt: time.Now().UTC()}
	m.Source = req.Source
	s.data.Models = append([]api.Model{m}, s.data.Models...)
	s.record("model.registered", "model", m.ID, actor, nil)
	return m, s.persist()
}

func normalizeServingImage(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("serving_image must not contain control characters")
	}
	image := strings.TrimSpace(value)
	if image == "" {
		return "", errors.New("serving_image must not be whitespace-only")
	}
	if strings.IndexFunc(image, unicode.IsSpace) >= 0 {
		return "", errors.New("serving_image must not contain whitespace")
	}
	return image, nil
}

func (s *Store) PromoteModel(modelID, stage, actor string) (api.Model, error) {
	allowed := map[string]bool{"staging": true, "production": true, "archived": true}
	if !allowed[stage] {
		return api.Model{}, errors.New("stage must be staging, production or archived")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Models {
		if s.data.Models[i].ID == modelID {
			if stage == "production" && s.data.Models[i].GateStatus != "passed" {
				return api.Model{}, errors.New("model evaluation gates have not passed")
			}
			s.data.Models[i].PreviousStage = s.data.Models[i].Stage
			s.data.Models[i].Stage = stage
			s.record("model.promoted", "model", modelID, actor, map[string]any{"stage": stage})
			return s.data.Models[i], s.persist()
		}
	}
	return api.Model{}, ErrNotFound
}

func (s *Store) DeployModel(modelID string, weight int, actor string) (api.Model, error) {
	if weight < 0 || weight > 100 {
		return api.Model{}, errors.New("canary_weight must be between 0 and 100")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Models {
		if s.data.Models[i].ID == modelID {
			if s.data.Models[i].GateStatus != "passed" {
				return api.Model{}, errors.New("model evaluation gates have not passed")
			}
			s.data.Models[i].DeploymentStatus, s.data.Models[i].CanaryWeight = "deploying", weight
			s.data.Models[i].EndpointURL = "/v1/models/" + s.data.Models[i].Name + ":predict"
			s.record("model.deployed", "model", modelID, actor, map[string]any{"canary_weight": weight})
			return s.data.Models[i], s.persist()
		}
	}
	return api.Model{}, ErrNotFound
}

// SetModelEndpoint records the live serving endpoint after the serving
// manager (or KServe on Kubernetes) has actually started the model server.
func (s *Store) SetModelEndpoint(modelID, endpoint, status string) (api.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Models {
		if s.data.Models[i].ID == modelID {
			s.data.Models[i].EndpointURL = endpoint
			s.data.Models[i].DeploymentStatus = status
			return s.data.Models[i], s.persist()
		}
	}
	return api.Model{}, ErrNotFound
}

func (s *Store) RollbackModel(modelID, actor string) (api.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Models {
		if s.data.Models[i].ID == modelID {
			if s.data.Models[i].PreviousStage == "" {
				return api.Model{}, errors.New("no previous stage is available")
			}
			s.data.Models[i].Stage, s.data.Models[i].PreviousStage = s.data.Models[i].PreviousStage, s.data.Models[i].Stage
			s.data.Models[i].DeploymentStatus, s.data.Models[i].CanaryWeight = "rolled_back", 0
			s.record("model.rolled_back", "model", modelID, actor, nil)
			return s.data.Models[i], s.persist()
		}
	}
	return api.Model{}, ErrNotFound
}

func (s *Store) DeployAgent(req api.DeployAgentRequest, actor string) (api.Agent, error) {
	if err := NormalizeAgentRequest(&req); err != nil {
		return api.Agent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasProject(s.data.Projects, req.ProjectID) {
		return api.Agent{}, ErrNotFound
	}
	a := api.Agent{ID: id("agt"), ProjectID: req.ProjectID, OwnerSubject: req.OwnerSubject, Name: req.Name, Version: req.Version, Image: req.Image, GraphModule: req.GraphModule, LLMBackend: req.LLMBackend, Replicas: req.Replicas, Autoscaling: req.Autoscaling, Resources: req.Resources, Tools: req.Tools, Status: "pending", CreatedAt: time.Now().UTC()}
	s.data.Agents = append([]api.Agent{a}, s.data.Agents...)
	s.record("agent.deployed", "agent", a.ID, actor, nil)
	return a, s.persist()
}

func (s *Store) UpdateConnectionStatus(connectionID, status, message, actor string) (api.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Connections {
		if s.data.Connections[i].ID == connectionID {
			now := time.Now().UTC()
			s.data.Connections[i].Status, s.data.Connections[i].Message, s.data.Connections[i].CheckedAt = status, message, &now
			s.record("connection.checked", "connection", connectionID, actor, map[string]any{"status": status})
			return s.data.Connections[i], s.persist()
		}
	}
	return api.Connection{}, ErrNotFound
}

func (s *Store) AgentSessions(agentID string) []api.AgentSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []api.AgentSession{}
	for _, session := range s.data.Sessions {
		if session.AgentID == agentID || agentID == "" {
			result = append(result, session)
		}
	}
	return result
}
func (s *Store) AgentTraces(agentID string) []api.AgentTrace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []api.AgentTrace{}
	for _, trace := range s.data.Traces {
		if trace.AgentID == agentID || agentID == "" {
			result = append(result, trace)
		}
	}
	return result
}
func (s *Store) RecordTrace(req api.RecordTraceRequest) (api.AgentTrace, error) {
	if req.AgentID == "" || req.SessionID == "" {
		return api.AgentTrace{}, errors.New("agent_id and session_id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	trace := api.AgentTrace{ID: id("trace"), AgentID: req.AgentID, SessionID: req.SessionID, Name: req.Name, Status: req.Status, DurationMS: req.DurationMS, Tokens: req.InputTokens + req.OutputTokens, Metadata: req.Metadata, CreatedAt: now}
	s.data.Traces = append([]api.AgentTrace{trace}, s.data.Traces...)
	found := false
	// Sessions are scoped per agent: the same session id under two agents is
	// two sessions.
	for i := range s.data.Sessions {
		if s.data.Sessions[i].ID == req.SessionID && s.data.Sessions[i].AgentID == req.AgentID {
			s.data.Sessions[i].CurrentNode, s.data.Sessions[i].Status, s.data.Sessions[i].UpdatedAt = req.CurrentNode, req.Status, now
			s.data.Sessions[i].Turns++
			s.data.Sessions[i].InputTokens += req.InputTokens
			s.data.Sessions[i].OutputTokens += req.OutputTokens
			s.data.Sessions[i].CostUSD += req.CostUSD
			found = true
		}
	}
	if !found {
		s.data.Sessions = append([]api.AgentSession{{ID: req.SessionID, AgentID: req.AgentID, UserID: req.UserID, Status: req.Status, CurrentNode: req.CurrentNode, Turns: 1, InputTokens: req.InputTokens, OutputTokens: req.OutputTokens, CostUSD: req.CostUSD, StartedAt: now, UpdatedAt: now}}, s.data.Sessions...)
	}
	return trace, s.persist()
}

func (s *Store) FeatureViews() []api.FeatureView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.Features)
}

// ApplyFeatureView upserts by name, mirroring `feast apply` semantics: the
// definition is declarative and re-applying replaces fields while keeping
// identity and materialization history.
func (s *Store) ApplyFeatureView(req api.ApplyFeatureViewRequest, actor string) (api.FeatureView, error) {
	if req.Name == "" || req.Entity == "" || len(req.Fields) == 0 {
		return api.FeatureView{}, errors.New("name, entity and at least one field are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Features {
		if s.data.Features[i].Name == req.Name {
			s.data.Features[i].Entity = req.Entity
			s.data.Features[i].Fields = req.Fields
			s.data.Features[i].Tags = req.Tags
			s.data.Features[i].Source = req.Source
			s.data.Features[i].TTLSeconds = req.TTLSeconds
			s.record("feature_view.applied", "feature_view", s.data.Features[i].ID, actor, nil)
			return s.data.Features[i], s.persist()
		}
	}
	view := api.FeatureView{ID: id("fv"), Name: req.Name, Entity: req.Entity, Fields: req.Fields, Tags: req.Tags, Source: req.Source, TTLSeconds: req.TTLSeconds, Status: "registered", CreatedAt: time.Now().UTC()}
	s.data.Features = append([]api.FeatureView{view}, s.data.Features...)
	s.record("feature_view.applied", "feature_view", view.ID, actor, nil)
	return view, s.persist()
}

func (s *Store) ReportMaterialization(name string, entityCount int, actor string) (api.FeatureView, error) {
	if entityCount < 0 {
		return api.FeatureView{}, errors.New("entity_count cannot be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Features {
		if s.data.Features[i].Name == name {
			now := time.Now().UTC()
			s.data.Features[i].Status = "materialized"
			s.data.Features[i].OnlineEntityCount = entityCount
			s.data.Features[i].MaterializedAt = &now
			s.record("feature_view.materialized", "feature_view", s.data.Features[i].ID, actor, map[string]any{"entity_count": entityCount})
			return s.data.Features[i], s.persist()
		}
	}
	return api.FeatureView{}, ErrNotFound
}

func (s *Store) SetAgentTraffic(agentID string, weight int, actor string) (api.Agent, error) {
	if weight < 0 || weight > 100 {
		return api.Agent{}, errors.New("canary_weight must be between 0 and 100")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Agents {
		if s.data.Agents[i].ID == agentID {
			s.data.Agents[i].CanaryWeight = weight
			s.record("agent.traffic_updated", "agent", agentID, actor, map[string]any{"canary_weight": weight})
			return s.data.Agents[i], s.persist()
		}
	}
	return api.Agent{}, ErrNotFound
}

func (s *Store) RegisterTool(req api.RegisterToolRequest, actor string) (api.Tool, error) {
	if req.Name == "" || req.Version == "" || len(req.InputSchema) == 0 {
		return api.Tool{}, errors.New("name, version and input_schema are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.data.Tools {
		if v.Name == req.Name && v.Version == req.Version {
			return api.Tool{}, ErrConflict
		}
	}
	t := api.Tool{ID: id("tool"), Name: req.Name, Version: req.Version, Description: req.Description, Tags: req.Tags, InputSchema: req.InputSchema, Status: "ready", CreatedAt: time.Now().UTC()}
	s.data.Tools = append([]api.Tool{t}, s.data.Tools...)
	s.record("tool.registered", "tool", t.ID, actor, nil)
	return t, s.persist()
}

func (s *Store) CreateConnection(req api.CreateConnectionRequest, actor string) (api.Connection, error) {
	if req.Name == "" || req.Type == "" || req.SecretRef == "" {
		return api.Connection{}, errors.New("name, type and secret_ref are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.data.Connections {
		if strings.EqualFold(v.Name, req.Name) {
			return api.Connection{}, ErrConflict
		}
	}
	c := api.Connection{ID: id("conn"), Name: req.Name, Type: req.Type, Endpoint: req.Endpoint, SecretRef: req.SecretRef, Status: "pending", CreatedAt: time.Now().UTC()}
	s.data.Connections = append([]api.Connection{c}, s.data.Connections...)
	s.record("connection.created", "connection", c.ID, actor, nil)
	return c, s.persist()
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return err
	}
	for i := range s.data.Agents {
		NormalizeAgentRecord(&s.data.Agents[i])
	}
	return nil
}

func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) record(action, resource, resourceID, actor string, metadata map[string]any) {
	if actor == "" {
		actor = "anonymous"
	}
	event := api.AuditEvent{ID: id("evt"), Action: action, Resource: resource, ResourceID: resourceID, Actor: actor, Metadata: metadata, CreatedAt: time.Now().UTC()}
	s.data.Audit = append([]api.AuditEvent{event}, s.data.Audit...)
}

// clone always returns a non-nil slice so list endpoints serialize as [] —
// never null — regardless of state.
func clone[T any](values []T) []T { return append([]T{}, values...) }

// Pipeline runs are updated asynchronously by execution engines. A shallow
// struct copy still aliases Steps, DependsOn, Logs, and Parameters, which lets a
// background step transition race with an HTTP encoder (or lets a caller mutate
// persisted state after the store lock is released). Always detach those fields
// at the repository boundary.
func clonePipelineRun(value api.PipelineRun) api.PipelineRun {
	value.Parameters = cloneJSONMap(value.Parameters)
	value.Steps = append([]api.PipelineStep{}, value.Steps...)
	for i := range value.Steps {
		value.Steps[i].DependsOn = append([]string{}, value.Steps[i].DependsOn...)
	}
	value.Logs = append([]api.RunLog{}, value.Logs...)
	return value
}

func clonePipelineRuns(values []api.PipelineRun) []api.PipelineRun {
	result := make([]api.PipelineRun, len(values))
	for i := range values {
		result[i] = clonePipelineRun(values[i])
	}
	return result
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneJSONValue(item)
	}
	return result
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i := range typed {
			result[i] = cloneJSONValue(typed[i])
		}
		return result
	case []string:
		return append([]string{}, typed...)
	default:
		return value
	}
}
func hasProject(values []api.Project, id string) bool {
	for _, v := range values {
		if v.ID == id {
			return true
		}
	}
	return false
}
func first(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
func id(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }
func slug(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultSteps(status string) []api.PipelineStep {
	// Must mirror the steps python/pipelines/training.py reports: the run only
	// completes when every templated step has reported, so a partial template
	// would mark the run succeeded while later steps are still executing.
	steps := []api.PipelineStep{{Name: "validate-data", Status: status, Image: "mlaiops/pipeline-runner:latest", Progress: 0}, {Name: "train-model", Status: status, Image: "mlaiops/pipeline-runner:latest", DependsOn: []string{"validate-data"}, Progress: 0}, {Name: "evaluate", Status: status, Image: "mlaiops/pipeline-runner:latest", DependsOn: []string{"train-model"}, Progress: 0}, {Name: "register-model", Status: status, Image: "mlaiops/pipeline-runner:latest", DependsOn: []string{"evaluate"}, Progress: 0}}
	if status == "succeeded" {
		for i := range steps {
			steps[i].Progress = 100
		}
	}
	return steps
}
