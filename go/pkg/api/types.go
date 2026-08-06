// Package api contains the stable control-plane wire contracts.
package api

import "time"

// UserAccess is the administrator-owned authorization and capacity profile for
// one human identity. Subject must match the OIDC `sub` claim exactly.
type UserAccess struct {
	Subject    string       `json:"subject"`
	Email      string       `json:"email"`
	Role       string       `json:"role"`
	Services   []string     `json:"services"`
	ProjectIDs []string     `json:"project_ids,omitempty"`
	Storage    StorageGrant `json:"storage"`
	Compute    ComputeGrant `json:"compute"`
	Disabled   bool         `json:"disabled"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

type StorageGrant struct {
	SizeGB  int      `json:"size_gb"`
	Buckets []string `json:"buckets,omitempty"`
}

type ComputeGrant struct {
	Profile      string `json:"profile,omitempty"`
	VCPUs        int    `json:"vcpus"`
	MemoryGB     int    `json:"memory_gb"`
	GPUs         int    `json:"gpus,omitempty"`
	GPUType      string `json:"gpu_type,omitempty"`
	MaxVMs       int    `json:"max_vms"`
	MaxProjects  int    `json:"max_projects"`
	MaxRuns      int    `json:"max_concurrent_runs"`
	MaxFunctions int    `json:"max_functions"`
}

// ResourceProfile is an administrator-facing allocation preset. Profiles
// keep common workspace sizing simple while the custom profile retains full
// control for unusual workloads.
type ResourceProfile struct {
	Name        string       `json:"name"`
	Label       string       `json:"label"`
	Description string       `json:"description"`
	Compute     ComputeGrant `json:"compute"`
	StorageGB   int          `json:"storage_gb"`
}

type UpsertUserAccessRequest struct {
	Email      string       `json:"email"`
	Role       string       `json:"role"`
	Services   []string     `json:"services"`
	ProjectIDs []string     `json:"project_ids,omitempty"`
	Storage    StorageGrant `json:"storage"`
	Compute    ComputeGrant `json:"compute"`
	Disabled   bool         `json:"disabled"`
}

type AccessRequest struct {
	ID                string    `json:"id"`
	Subject           string    `json:"subject"`
	Email             string    `json:"email"`
	Reason            string    `json:"reason"`
	RequestedServices []string  `json:"requested_services"`
	Status            string    `json:"status"`
	Reviewer          string    `json:"reviewer,omitempty"`
	ReviewNote        string    `json:"review_note,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CreateAccessRequest struct {
	Reason            string   `json:"reason"`
	RequestedServices []string `json:"requested_services"`
}

type ReviewAccessRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

type APIToken struct {
	ID         string     `json:"id"`
	Subject    string     `json:"subject"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	SecretHash string     `json:"secret_hash,omitempty"`
	Services   []string   `json:"services"`
	ProjectIDs []string   `json:"project_ids,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type CreateAPITokenRequest struct {
	Name          string   `json:"name"`
	Services      []string `json:"services"`
	ProjectIDs    []string `json:"project_ids,omitempty"`
	ExpiresInDays int      `json:"expires_in_days"`
}

type CreatedAPIToken struct {
	Token  APIToken `json:"token"`
	Secret string   `json:"secret"`
}

type BlogPost struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	Content     string     `json:"content"`
	Author      string     `json:"author"`
	Tags        []string   `json:"tags"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type UpsertBlogPostRequest struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Content string   `json:"content"`
	Author  string   `json:"author"`
	Tags    []string `json:"tags"`
	Status  string   `json:"status"`
}

type Project struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Template         string         `json:"template"`
	TemplateVersion  string         `json:"template_version"`
	Framework        string         `json:"framework"`
	Accelerator      string         `json:"accelerator"`
	RequestedProfile string         `json:"requested_profile"`
	Capabilities     []string       `json:"capabilities"`
	ScaffoldCommand  string         `json:"scaffold_command"`
	Namespace        string         `json:"namespace"`
	Status           string         `json:"status"`
	CreatedAt        time.Time      `json:"created_at"`
	OwnerSubject     string         `json:"owner_subject,omitempty"`
	Repository       *GitRepository `json:"repository,omitempty"`
}

type CreateProjectRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Template         string `json:"template"`
	TemplateVersion  string `json:"template_version,omitempty"`
	Framework        string `json:"framework,omitempty"`
	Accelerator      string `json:"accelerator,omitempty"`
	RequestedProfile string `json:"requested_profile,omitempty"`
	RepositoryURL    string `json:"repository_url,omitempty"`
	DefaultBranch    string `json:"default_branch,omitempty"`
	OwnerSubject     string `json:"-"`
}

// ProjectTemplate is a versioned, API-visible starter contract shared by the
// console, SDK and the Kionga workspace generator.
type ProjectTemplate struct {
	ID                 string   `json:"id"`
	Version            string   `json:"version"`
	Name               string   `json:"name"`
	Category           string   `json:"category"`
	Description        string   `json:"description"`
	Frameworks         []string `json:"frameworks"`
	Accelerators       []string `json:"accelerators"`
	Capabilities       []string `json:"capabilities"`
	RequiredServices   []string `json:"required_services"`
	RecommendedProfile string   `json:"recommended_profile"`
}

// GitRepository binds a platform project to its source of truth without
// storing source-control credentials in the control plane. Workspaces use
// their own Git credential helper when cloning private repositories.
type GitRepository struct {
	URL           string     `json:"url"`
	Provider      string     `json:"provider"`
	DefaultBranch string     `json:"default_branch"`
	LastCommit    string     `json:"last_commit,omitempty"`
	SyncedAt      *time.Time `json:"synced_at,omitempty"`
}

type SetProjectRepositoryRequest struct {
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
	LastCommit    string `json:"last_commit,omitempty"`
}

type PipelineRun struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Progress    int       `json:"progress"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ParentRunID string    `json:"parent_run_id,omitempty"`
	// EngineRunID links the control-plane run to the execution engine's run
	// (Prefect flow run id locally, KFP run id on Kubernetes).
	EngineRunID   string         `json:"engine_run_id,omitempty"`
	DefinitionID  string         `json:"definition_id,omitempty"`
	ExecutionMode string         `json:"execution_mode,omitempty"`
	Parameters    map[string]any `json:"parameters,omitempty"`
	Steps         []PipelineStep `json:"steps"`
	Logs          []RunLog       `json:"logs,omitempty"`
}

// UpdateRunStepRequest is sent by the executing pipeline itself (through the
// SDK step reporter) at every step transition.
type UpdateRunStepRequest struct {
	Step    string `json:"step"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type PipelineStep struct {
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Image     string   `json:"image"`
	DependsOn []string `json:"depends_on,omitempty"`
	Progress  int      `json:"progress"`
}

type RunLog struct {
	Timestamp time.Time `json:"timestamp"`
	Step      string    `json:"step"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

type SubmitPipelineRequest struct {
	ProjectID    string         `json:"project_id"`
	Name         string         `json:"name"`
	DefinitionID string         `json:"definition_id,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
}

type JobResources struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	GPU    int    `json:"gpu,omitempty"`
}

// PipelineJob is a reusable unit of work. Function jobs invoke an OpenFaaS
// function; container jobs are handed to Prefect locally and KFP on
// Kubernetes. The same dependency graph is exposed to every interface.
type PipelineJob struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Function    string            `json:"function,omitempty"`
	Image       string            `json:"image,omitempty"`
	Command     []string          `json:"command,omitempty"`
	DependsOn   []string          `json:"depends_on,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Resources   JobResources      `json:"resources"`
	Retries     int               `json:"retries"`
}

type PipelineDefinition struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	ExecutionMode string        `json:"execution_mode"`
	Jobs          []PipelineJob `json:"jobs"`
	RepositoryURL string        `json:"repository_url,omitempty"`
	CommitSHA     string        `json:"commit_sha,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

type UpsertPipelineDefinitionRequest struct {
	ProjectID     string        `json:"project_id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	ExecutionMode string        `json:"execution_mode"`
	Jobs          []PipelineJob `json:"jobs"`
	RepositoryURL string        `json:"repository_url,omitempty"`
	CommitSHA     string        `json:"commit_sha,omitempty"`
}

type Function struct {
	Name         string            `json:"name"`
	ProjectID    string            `json:"project_id"`
	Image        string            `json:"image"`
	Status       string            `json:"status"`
	Replicas     int               `json:"replicas"`
	EnvVars      map[string]string `json:"env_vars,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	CPU          string            `json:"cpu,omitempty"`
	Memory       string            `json:"memory,omitempty"`
	OwnerSubject string            `json:"owner_subject,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type DeployFunctionRequest struct {
	ProjectID   string            `json:"project_id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	EnvVars     map[string]string `json:"env_vars,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	CPU         string            `json:"cpu,omitempty"`
	Memory      string            `json:"memory,omitempty"`
}

type Component struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	Description string `json:"description"`
	URL         string `json:"url,omitempty"`
}

type Model struct {
	ID               string             `json:"id"`
	ProjectID        string             `json:"project_id"`
	Name             string             `json:"name"`
	Version          string             `json:"version"`
	Stage            string             `json:"stage"`
	ArtifactURI      string             `json:"artifact_uri"`
	ServingImage     string             `json:"serving_image,omitempty"`
	Metrics          map[string]float64 `json:"metrics"`
	CreatedAt        time.Time          `json:"created_at"`
	GateStatus       string             `json:"gate_status"`
	DeploymentStatus string             `json:"deployment_status"`
	CanaryWeight     int                `json:"canary_weight"`
	EndpointURL      string             `json:"endpoint_url,omitempty"`
	PreviousStage    string             `json:"previous_stage,omitempty"`
}

type RegisterModelRequest struct {
	ProjectID    string             `json:"project_id"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	ArtifactURI  string             `json:"artifact_uri"`
	ServingImage string             `json:"serving_image,omitempty"`
	Metrics      map[string]float64 `json:"metrics"`
}

type PromoteModelRequest struct {
	Stage string `json:"stage"`
}

type DeployModelRequest struct {
	CanaryWeight int `json:"canary_weight"`
}

type Agent struct {
	ID           string           `json:"id"`
	ProjectID    string           `json:"project_id"`
	OwnerSubject string           `json:"owner_subject,omitempty"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Image        string           `json:"image"`
	GraphModule  string           `json:"graph_module"`
	LLMBackend   string           `json:"llm_backend"`
	Status       string           `json:"status"`
	EndpointURL  string           `json:"endpoint_url,omitempty"`
	Replicas     int              `json:"replicas"`
	Autoscaling  AgentAutoscaling `json:"autoscaling"`
	Resources    AgentResources   `json:"resources"`
	CanaryWeight int              `json:"canary_weight"`
	Tools        []string         `json:"tools"`
	CreatedAt    time.Time        `json:"created_at"`
}

type DeployAgentRequest struct {
	ProjectID    string           `json:"project_id"`
	OwnerSubject string           `json:"-"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Image        string           `json:"image"`
	GraphModule  string           `json:"graph_module"`
	LLMBackend   string           `json:"llm_backend"`
	Replicas     int              `json:"replicas,omitempty"`
	Autoscaling  AgentAutoscaling `json:"autoscaling,omitempty"`
	Resources    AgentResources   `json:"resources,omitempty"`
	Tools        []string         `json:"tools"`
}

type AgentAutoscaling struct {
	MinReplicas int `json:"min_replicas"`
	MaxReplicas int `json:"max_replicas"`
}

type AgentResources struct {
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
	GPU     int    `json:"gpu,omitempty"`
	GPUType string `json:"gpu_type,omitempty"`
}

type TrafficRequest struct {
	CanaryWeight int `json:"canary_weight"`
}

type Tool struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags"`
	InputSchema map[string]any `json:"input_schema"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
}

type RegisterToolRequest struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags"`
	InputSchema map[string]any `json:"input_schema"`
}

type Connection struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Endpoint  string     `json:"endpoint"`
	SecretRef string     `json:"secret_ref"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Message   string     `json:"message,omitempty"`
}

type CreateConnectionRequest struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Endpoint  string `json:"endpoint"`
	SecretRef string `json:"secret_ref"`
}

type ReadinessItem struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Action      string `json:"action,omitempty"`
}

type Readiness struct {
	Percent int             `json:"percent"`
	Ready   bool            `json:"ready"`
	Items   []ReadinessItem `json:"items"`
}

type AgentSession struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	UserID       string    `json:"user_id"`
	Status       string    `json:"status"`
	CurrentNode  string    `json:"current_node"`
	Turns        int       `json:"turns"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AgentTrace struct {
	ID         string         `json:"id"`
	AgentID    string         `json:"agent_id"`
	SessionID  string         `json:"session_id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	DurationMS int64          `json:"duration_ms"`
	Tokens     int            `json:"tokens"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type RecordTraceRequest struct {
	AgentID      string         `json:"agent_id"`
	SessionID    string         `json:"session_id"`
	UserID       string         `json:"user_id"`
	Name         string         `json:"name"`
	Status       string         `json:"status"`
	CurrentNode  string         `json:"current_node"`
	DurationMS   int64          `json:"duration_ms"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	CostUSD      float64        `json:"cost_usd"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// FeatureView is the control-plane record of a feature store view: what the
// catalog shows and what materialization jobs report against. The data itself
// lives in the online (Redis/Feast) and offline (Parquet on S3) stores.
type FeatureView struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Entity            string         `json:"entity"`
	Fields            []FeatureField `json:"fields"`
	Tags              []string       `json:"tags"`
	Source            string         `json:"source"`
	TTLSeconds        int            `json:"ttl_seconds"`
	Status            string         `json:"status"`
	OnlineEntityCount int            `json:"online_entity_count"`
	MaterializedAt    *time.Time     `json:"materialized_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
}

type FeatureField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ApplyFeatureViewRequest struct {
	Name       string         `json:"name"`
	Entity     string         `json:"entity"`
	Fields     []FeatureField `json:"fields"`
	Tags       []string       `json:"tags"`
	Source     string         `json:"source"`
	TTLSeconds int            `json:"ttl_seconds"`
}

type MaterializationReport struct {
	EntityCount int `json:"entity_count"`
}

type InvokeAgentRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

type AgentUsage struct {
	Sessions     int     `json:"sessions"`
	Active       int     `json:"active"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type AuditEvent struct {
	ID         string         `json:"id"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	ResourceID string         `json:"resource_id"`
	Actor      string         `json:"actor"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type CatalogItem struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Status   string   `json:"status"`
	Kind     string   `json:"kind"`
	Metadata []string `json:"metadata"`
}

type Dashboard struct {
	Projects      int           `json:"projects"`
	ActiveRuns    int           `json:"active_runs"`
	Healthy       int           `json:"healthy_components"`
	Total         int           `json:"total_components"`
	RecentRuns    []PipelineRun `json:"recent_runs"`
	OnboardingPct int           `json:"onboarding_percent"`
}

type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}
