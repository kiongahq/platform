package store

import (
	"context"
	"errors"
	"github.com/ml-ai-ops/platform/internal/pipelinespec"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ml-ai-ops/platform/pkg/api"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	resourceQuantity = regexp.MustCompile(`^[1-9][0-9]*(m|Mi|Gi)?$`)
	functionName     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	agentGraphModule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*:[A-Za-z_][A-Za-z0-9_]*$`)
	agentVersion     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
)

// ValidateFunctionRequest applies the portable subset of the OpenFaaS and
// Kubernetes function contracts before anything is sent to the runtime.
func ValidateFunctionRequest(req api.DeployFunctionRequest) error {
	if req.ProjectID == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Image) == "" {
		return errors.New("project_id, name and image are required")
	}
	if !functionName.MatchString(req.Name) {
		return errors.New("function name must be a lowercase DNS label")
	}
	if req.CPU != "" && !resourceQuantity.MatchString(req.CPU) {
		return errors.New("CPU must be a quantity such as 500m or 2")
	}
	if req.Memory != "" && !resourceQuantity.MatchString(req.Memory) {
		return errors.New("memory must be a quantity such as 512Mi or 2Gi")
	}
	mode := req.Annotations["io.kionga.invocation"]
	if mode != "" && mode != "sync" && mode != "async" {
		return errors.New("io.kionga.invocation must be sync or async")
	}
	if schedule := strings.TrimSpace(req.Annotations["schedule"]); schedule != "" {
		if !strings.Contains(req.Annotations["topic"], "cron-function") {
			return errors.New("scheduled functions must subscribe to the cron-function topic")
		}
		for _, expression := range strings.Split(schedule, ";") {
			if len(strings.Fields(expression)) != 5 {
				return errors.New("schedule must contain five-field cron expressions")
			}
		}
	}
	return nil
}

// NormalizeAgentRequest applies safe workload defaults shared by the file and
// PostgreSQL stores. Replicas remains a compatibility input; autoscaling is
// the canonical contract returned to clients and reconciled on Kubernetes.
func NormalizeAgentRequest(req *api.DeployAgentRequest) error {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)
	req.Image = strings.TrimSpace(req.Image)
	req.GraphModule = strings.TrimSpace(req.GraphModule)
	req.LLMBackend = strings.TrimSpace(req.LLMBackend)
	if req.ProjectID == "" || req.Name == "" || req.Version == "" || req.Image == "" || req.GraphModule == "" {
		return errors.New("project_id, name, version, image and graph_module are required")
	}
	if !functionName.MatchString(req.Name) {
		return errors.New("agent name must be a lowercase DNS label")
	}
	if !agentVersion.MatchString(req.Version) {
		return errors.New("agent version must contain only letters, numbers, dot, underscore, plus, or hyphen")
	}
	if len(req.Image) > 512 || strings.IndexFunc(req.Image, func(char rune) bool { return unicode.IsSpace(char) || unicode.IsControl(char) }) >= 0 {
		return errors.New("agent image must be a non-whitespace OCI image reference")
	}
	if !agentGraphModule.MatchString(req.GraphModule) {
		return errors.New("agent graph_module must use package.module:attribute")
	}
	if req.Replicas < 1 {
		req.Replicas = 1
	}
	if req.Autoscaling.MinReplicas < 1 {
		req.Autoscaling.MinReplicas = req.Replicas
	}
	if req.Autoscaling.MaxReplicas < 1 {
		req.Autoscaling.MaxReplicas = req.Autoscaling.MinReplicas
	}
	if req.Autoscaling.MaxReplicas < req.Autoscaling.MinReplicas || req.Autoscaling.MaxReplicas > 100 {
		return errors.New("agent max_replicas must be between min_replicas and 100")
	}
	req.Replicas = req.Autoscaling.MinReplicas
	req.Resources.CPU = strings.TrimSpace(req.Resources.CPU)
	req.Resources.Memory = strings.TrimSpace(req.Resources.Memory)
	if req.Resources.CPU == "" {
		req.Resources.CPU = "500m"
	}
	if req.Resources.Memory == "" {
		req.Resources.Memory = "1Gi"
	}
	if !resourceQuantity.MatchString(req.Resources.CPU) {
		return errors.New("agent CPU must be a quantity such as 500m or 2")
	}
	if !resourceQuantity.MatchString(req.Resources.Memory) {
		return errors.New("agent memory must be a quantity such as 512Mi or 2Gi")
	}
	if req.Resources.GPU < 0 {
		return errors.New("agent GPU count cannot be negative")
	}
	if req.Resources.GPU > 0 && strings.TrimSpace(req.Resources.GPUType) == "" {
		req.Resources.GPUType = "nvidia.com/gpu"
	}
	req.Resources.GPUType = strings.TrimSpace(req.Resources.GPUType)
	if req.Resources.GPU > 0 {
		if err := validateAgentGPUType(req.Resources.GPUType); err != nil {
			return err
		}
	}
	if req.Resources.GPU == 0 {
		req.Resources.GPUType = ""
	}
	if req.LLMBackend == "" {
		req.LLMBackend = "mock"
	}
	switch req.LLMBackend {
	case "mock", "openai", "anthropic", "openai-compatible", "self-hosted":
	default:
		return errors.New("agent llm_backend must be mock, openai, anthropic, openai-compatible, or self-hosted")
	}
	seenTools := make(map[string]bool, len(req.Tools))
	tools := make([]string, 0, len(req.Tools))
	for _, value := range req.Tools {
		name := strings.TrimSpace(value)
		if name == "" {
			return errors.New("agent tool names must not be empty")
		}
		if seenTools[name] {
			return errors.New("agent tool names must be unique")
		}
		seenTools[name] = true
		tools = append(tools, name)
	}
	req.Tools = tools
	return nil
}

func validateAgentGPUType(value string) error {
	switch value {
	case "cpu", "memory", "ephemeral-storage", "storage":
		return errors.New("agent gpu_type must not collide with a native Kubernetes resource")
	}
	prefix, _, qualified := strings.Cut(value, "/")
	if !qualified || prefix == "" || len(kvalidation.IsQualifiedName(value)) > 0 {
		return errors.New("agent gpu_type must be a domain-qualified Kubernetes extended resource name")
	}
	if prefix == "kubernetes.io" || strings.HasSuffix(prefix, ".kubernetes.io") ||
		prefix == "k8s.io" || strings.HasSuffix(prefix, ".k8s.io") {
		return errors.New("agent gpu_type must not use a Kubernetes-reserved domain")
	}
	return nil
}

// NormalizeAgentRecord upgrades records written before autoscaling and
// per-replica resources became part of the public agent contract. Corrupt
// legacy resource values are replaced with conservative, deployable defaults
// so a subsequent update cannot turn cpu or memory into a GPU resource key.
func NormalizeAgentRecord(agent *api.Agent) {
	if agent.Replicas < 1 {
		agent.Replicas = 1
	}
	if agent.Replicas > 100 {
		agent.Replicas = 100
	}
	if agent.Autoscaling.MinReplicas < 1 {
		agent.Autoscaling.MinReplicas = agent.Replicas
	}
	if agent.Autoscaling.MinReplicas > 100 {
		agent.Autoscaling.MinReplicas = 100
	}
	if agent.Autoscaling.MaxReplicas < agent.Autoscaling.MinReplicas {
		agent.Autoscaling.MaxReplicas = agent.Autoscaling.MinReplicas
	}
	if agent.Autoscaling.MaxReplicas > 100 {
		agent.Autoscaling.MaxReplicas = 100
	}
	agent.Replicas = agent.Autoscaling.MinReplicas

	agent.Resources.CPU = strings.TrimSpace(agent.Resources.CPU)
	if !resourceQuantity.MatchString(agent.Resources.CPU) {
		agent.Resources.CPU = "500m"
	}
	agent.Resources.Memory = strings.TrimSpace(agent.Resources.Memory)
	if !resourceQuantity.MatchString(agent.Resources.Memory) {
		agent.Resources.Memory = "1Gi"
	}
	if agent.Resources.GPU < 0 {
		agent.Resources.GPU = 0
	}
	if agent.Resources.GPU == 0 {
		agent.Resources.GPUType = ""
	} else {
		agent.Resources.GPUType = strings.TrimSpace(agent.Resources.GPUType)
		if validateAgentGPUType(agent.Resources.GPUType) != nil {
			agent.Resources.GPUType = "nvidia.com/gpu"
		}
	}
	if strings.TrimSpace(agent.LLMBackend) == "" {
		agent.LLMBackend = "mock"
	}
	if agent.Tools == nil {
		agent.Tools = []string{}
	}
}

func validateGitRepository(req api.SetProjectRepositoryRequest) (api.GitRepository, error) {
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		return api.GitRepository{}, errors.New("repository URL is required")
	}
	provider := "git"
	if strings.HasPrefix(raw, "git@") {
		if !strings.Contains(raw, ":") || strings.ContainsAny(raw, "\n\r\t ") {
			return api.GitRepository{}, errors.New("invalid SSH repository URL")
		}
	} else {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return api.GitRepository{}, errors.New("repository URL must use HTTPS or the git@host:path SSH form")
		}
		if parsed.User != nil {
			return api.GitRepository{}, errors.New("repository credentials must not be embedded in the URL")
		}
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "github.com"):
		provider = "github"
	case strings.Contains(lower, "gitlab.com"):
		provider = "gitlab"
	case strings.Contains(lower, "bitbucket.org"):
		provider = "bitbucket"
	}
	branch := strings.TrimSpace(req.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	if strings.ContainsAny(branch, " ~^:?*[\\") || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return api.GitRepository{}, errors.New("invalid default branch")
	}
	return api.GitRepository{URL: raw, Provider: provider, DefaultBranch: branch, LastCommit: strings.TrimSpace(req.LastCommit)}, nil
}

func (s *Store) Project(projectID string) (api.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, project := range s.data.Projects {
		if project.ID == projectID {
			return project, nil
		}
	}
	return api.Project{}, ErrNotFound
}

func (s *Store) SetProjectRepository(projectID string, req api.SetProjectRepositoryRequest, actor string) (api.Project, error) {
	repository, err := validateGitRepository(req)
	if err != nil {
		return api.Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Projects {
		if s.data.Projects[i].ID == projectID {
			now := time.Now().UTC()
			repository.SyncedAt = &now
			s.data.Projects[i].Repository = &repository
			s.record("project.repository_updated", "project", projectID, actor, map[string]any{"provider": repository.Provider})
			return s.data.Projects[i], s.persist()
		}
	}
	return api.Project{}, ErrNotFound
}

func (s *Store) PipelineDefinition(definitionID string) (api.PipelineDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pipelineDefinitionFrom(s.data.Definitions, definitionID)
}

func pipelineDefinitionFrom(values []api.PipelineDefinition, definitionID string) (api.PipelineDefinition, error) {
	for _, definition := range values {
		if definition.ID == definitionID {
			return cloneDefinition(definition), nil
		}
	}
	return api.PipelineDefinition{}, ErrNotFound
}

// ValidatePipelineDefinition normalizes and validates the portable pipeline
// contract before persistence or resource-authorization checks. The error is
// a pipelinespec.Issues listing every problem.
func ValidatePipelineDefinition(req api.UpsertPipelineDefinitionRequest) (api.UpsertPipelineDefinitionRequest, error) {
	normalized, issues := pipelinespec.Validate(req)
	if len(issues) > 0 {
		return normalized, issues
	}
	return normalized, nil
}

func (s *Store) UpsertPipelineDefinition(definitionID string, req api.UpsertPipelineDefinitionRequest, actor string) (api.PipelineDefinition, error) {
	req, err := ValidatePipelineDefinition(req)
	if err != nil {
		return api.PipelineDefinition{}, err
	}
	definition, yamlText, changed, err := s.upsertPipelineDefinition(definitionID, req, actor)
	if err != nil || !changed {
		return definition, err
	}
	return definition, recordRevision(s, definition, yamlText, req, actor)
}

func (s *Store) upsertPipelineDefinition(definitionID string, req api.UpsertPipelineDefinitionRequest, actor string) (api.PipelineDefinition, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasProject(s.data.Projects, req.ProjectID) {
		return api.PipelineDefinition{}, "", false, ErrNotFound
	}
	now := time.Now().UTC()
	for i := range s.data.Definitions {
		if s.data.Definitions[i].ID == definitionID || (definitionID == "" && s.data.Definitions[i].ProjectID == req.ProjectID && s.data.Definitions[i].Name == req.Name && s.data.Definitions[i].Version == req.Version) {
			definition := &s.data.Definitions[i]
			yamlText, changed, err := applyDefinitionRequest(definition, req, now)
			if err != nil {
				return api.PipelineDefinition{}, "", false, err
			}
			s.record("pipeline_definition.updated", "pipeline_definition", definition.ID, actor, nil)
			return cloneDefinition(*definition), yamlText, changed, s.persist()
		}
	}
	if definitionID != "" {
		return api.PipelineDefinition{}, "", false, ErrNotFound
	}
	definition := api.PipelineDefinition{ID: id("pipe"), OwnerSubject: actor, CreatedAt: now}
	yamlText, _, err := applyDefinitionRequest(&definition, req, now)
	if err != nil {
		return api.PipelineDefinition{}, "", false, err
	}
	s.data.Definitions = append([]api.PipelineDefinition{definition}, s.data.Definitions...)
	s.record("pipeline_definition.created", "pipeline_definition", definition.ID, actor, nil)
	return definition, yamlText, true, s.persist()
}

func stepsFromDefinition(definition api.PipelineDefinition) []api.PipelineStep {
	steps := make([]api.PipelineStep, 0, len(definition.Jobs))
	for _, job := range definition.Jobs {
		image := job.Image
		if job.Kind == "function" {
			image = "function://" + job.Function
		}
		steps = append(steps, api.PipelineStep{Name: job.Name, Status: "pending", Image: image, DependsOn: job.DependsOn})
	}
	return steps
}

func resetSteps(previous []api.PipelineStep) []api.PipelineStep {
	steps := make([]api.PipelineStep, len(previous))
	copy(steps, previous)
	for i := range steps {
		steps[i].Status, steps[i].Progress = "pending", 0
	}
	return steps
}

func (s *Store) UpsertFunction(req api.DeployFunctionRequest, owner, actor string) (api.Function, error) {
	if err := ValidateFunctionRequest(req); err != nil {
		return api.Function{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasProject(s.data.Projects, req.ProjectID) {
		return api.Function{}, ErrNotFound
	}
	now := time.Now().UTC()
	for i := range s.data.Functions {
		if s.data.Functions[i].Name == req.Name {
			fn := &s.data.Functions[i]
			if fn.ProjectID != req.ProjectID {
				return api.Function{}, ErrConflict
			}
			fn.Image, fn.EnvVars, fn.Labels, fn.Annotations, fn.CPU, fn.Memory, fn.Status, fn.UpdatedAt = req.Image, req.EnvVars, req.Labels, req.Annotations, req.CPU, req.Memory, "deployed", now
			s.record("function.updated", "function", fn.Name, actor, nil)
			return *fn, s.persist()
		}
	}
	fn := api.Function{Name: req.Name, ProjectID: req.ProjectID, Image: req.Image, EnvVars: req.EnvVars, Labels: req.Labels, Annotations: req.Annotations, CPU: req.CPU, Memory: req.Memory, OwnerSubject: owner, Status: "deployed", CreatedAt: now, UpdatedAt: now}
	s.data.Functions = append([]api.Function{fn}, s.data.Functions...)
	s.record("function.deployed", "function", fn.Name, actor, nil)
	return fn, s.persist()
}

func (s *Store) DeleteFunction(name, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Functions {
		if s.data.Functions[i].Name == name {
			s.data.Functions = append(s.data.Functions[:i], s.data.Functions[i+1:]...)
			s.record("function.deleted", "function", name, actor, nil)
			return s.persist()
		}
	}
	return ErrNotFound
}

func (p *Postgres) Project(projectID string) (api.Project, error) {
	return get[api.Project](p, "project", projectID)
}
func (p *Postgres) SetProjectRepository(projectID string, req api.SetProjectRepositoryRequest, actor string) (api.Project, error) {
	project, err := p.Project(projectID)
	if err != nil {
		return project, err
	}
	repository, err := validateGitRepository(req)
	if err != nil {
		return project, err
	}
	now := time.Now().UTC()
	repository.SyncedAt = &now
	project.Repository = &repository
	return project, p.write("project", project.ID, project, "project.repository_updated", actor, map[string]any{"provider": repository.Provider})
}
func (p *Postgres) PipelineDefinition(definitionID string) (api.PipelineDefinition, error) {
	return get[api.PipelineDefinition](p, "pipeline_definition", definitionID)
}
func (p *Postgres) UpsertPipelineDefinition(definitionID string, req api.UpsertPipelineDefinitionRequest, actor string) (api.PipelineDefinition, error) {
	req, err := ValidatePipelineDefinition(req)
	if err != nil {
		return api.PipelineDefinition{}, err
	}
	if !p.exists("project", req.ProjectID) {
		return api.PipelineDefinition{}, ErrNotFound
	}
	if definitionID == "" {
		for _, existing := range p.PipelineDefinitions() {
			if existing.ProjectID == req.ProjectID && existing.Name == req.Name && existing.Version == req.Version {
				definitionID = existing.ID
				break
			}
		}
	}
	now := time.Now().UTC()
	var yamlText string
	var changed bool
	action := "pipeline_definition.updated"
	if definitionID == "" {
		definitionID, action = id("pipe"), "pipeline_definition.created"
	}
	// UpdateDoc locks the row so concurrent saves cannot both claim the same
	// next revision number.
	definition, err := UpdateDoc(p, "pipeline_definition", definitionID, func(current api.PipelineDefinition, exists bool) (api.PipelineDefinition, error) {
		if !exists {
			if action == "pipeline_definition.updated" {
				return current, ErrNotFound
			}
			current = api.PipelineDefinition{ID: definitionID, OwnerSubject: actor, CreatedAt: now}
		}
		var applyErr error
		yamlText, changed, applyErr = applyDefinitionRequest(&current, req, now)
		return current, applyErr
	}, action, actor)
	if err != nil || !changed {
		return definition, err
	}
	return definition, recordRevision(p, definition, yamlText, req, actor)
}

func (p *Postgres) UpsertFunction(req api.DeployFunctionRequest, owner, actor string) (api.Function, error) {
	if err := ValidateFunctionRequest(req); err != nil {
		return api.Function{}, err
	}
	if !p.exists("project", req.ProjectID) {
		return api.Function{}, ErrNotFound
	}
	now := time.Now().UTC()
	for _, fn := range p.Functions() {
		if fn.Name == req.Name {
			if fn.ProjectID != req.ProjectID {
				return api.Function{}, ErrConflict
			}
			fn.Image, fn.EnvVars, fn.Labels, fn.Annotations, fn.CPU, fn.Memory, fn.Status, fn.UpdatedAt = req.Image, req.EnvVars, req.Labels, req.Annotations, req.CPU, req.Memory, "deployed", now
			return fn, p.write("function", fn.Name, fn, "function.updated", actor, nil)
		}
	}
	fn := api.Function{Name: req.Name, ProjectID: req.ProjectID, Image: req.Image, EnvVars: req.EnvVars, Labels: req.Labels, Annotations: req.Annotations, CPU: req.CPU, Memory: req.Memory, OwnerSubject: owner, Status: "deployed", CreatedAt: now, UpdatedAt: now}
	return fn, p.write("function", fn.Name, fn, "function.deployed", actor, nil)
}
func (p *Postgres) DeleteFunction(name, actor string) error {
	tag, err := p.pool.Exec(context.Background(), `DELETE FROM platform_resources WHERE tenant_id=$1 AND kind='function' AND id=$2`, p.tenant, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	event := api.AuditEvent{ID: id("evt"), Action: "function.deleted", Resource: "function", ResourceID: name, Actor: actorOrAnonymous(actor), CreatedAt: time.Now().UTC()}
	_, err = p.pool.Exec(context.Background(), `INSERT INTO audit_events (tenant_id,id,action,resource,resource_id,actor,metadata,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.tenant, event.ID, event.Action, event.Resource, event.ResourceID, event.Actor, event.Metadata, event.CreatedAt)
	return err
}
