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
	// Trigger records how the run started: manual, schedule, api, event or retry.
	Trigger string `json:"trigger,omitempty"`
	// ScheduledFor is the schedule slot a scheduled run fills.
	ScheduledFor *time.Time `json:"scheduled_for,omitempty"`
	// Provenance pins exactly what executed (definition revision, YAML hash,
	// image digests, overrides, policy decision). See RunProvenance.
	Provenance   *RunProvenance `json:"provenance,omitempty"`
	OwnerSubject string         `json:"owner_subject,omitempty"`
}

// RunProvenance is the immutable record of what a run executed.
type RunProvenance struct {
	DefinitionRevision int               `json:"definition_revision,omitempty"`
	DefinitionSHA256   string            `json:"definition_sha256,omitempty"`
	ImageDigests       map[string]string `json:"image_digests,omitempty"`
	Overrides          *RunOverrides     `json:"overrides,omitempty"`
	PolicyDecision     string            `json:"policy_decision,omitempty"`
	Executor           string            `json:"executor,omitempty"`
}

// RunOverrides are one-run changes to a definition's defaults, applied only
// where the definition marks the field overridable and policy allows it.
type RunOverrides struct {
	Parameters     map[string]any          `json:"parameters,omitempty"`
	Nodes          map[string]NodeOverride `json:"nodes,omitempty"`
	SelectedNodes  []string                `json:"selected_nodes,omitempty"`
	RerunFromRunID string                  `json:"rerun_from_run_id,omitempty"`
	Priority       string                  `json:"priority,omitempty"`
	MaxParallelism int                     `json:"max_parallelism,omitempty"`
}

// NodeOverride changes one node for one run.
type NodeOverride struct {
	Image          string            `json:"image,omitempty"`
	Resources      *JobResources     `json:"resources,omitempty"`
	Retries        *int              `json:"retries,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
}

// UpdateRunStepRequest is sent by the executing pipeline itself (through the
// SDK step reporter) at every step transition.
type UpdateRunStepRequest struct {
	Step    string `json:"step"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Execution facts reported by the runner; all optional for older runners.
	Attempt      int        `json:"attempt,omitempty"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	WorkloadKind string     `json:"workload_kind,omitempty"`
	WorkloadID   string     `json:"workload_id,omitempty"`
	ImageDigest  string     `json:"image_digest,omitempty"`
	At           *time.Time `json:"at,omitempty"`
}

type PipelineStep struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Image     string     `json:"image"`
	DependsOn []string   `json:"depends_on,omitempty"`
	Progress  int        `json:"progress"`
	Kind      string     `json:"kind,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Attempt   int        `json:"attempt,omitempty"`
	Attempts  int        `json:"attempts,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Message   string     `json:"message,omitempty"`
	// WorkloadKind is docker-container, k8s-job, k8s-pod or openfaas-call;
	// the console never labels one kind as another.
	WorkloadKind string `json:"workload_kind,omitempty"`
	WorkloadID   string `json:"workload_id,omitempty"`
	ImageDigest  string `json:"image_digest,omitempty"`
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
	Overrides    *RunOverrides  `json:"overrides,omitempty"`
	Trigger      string         `json:"trigger,omitempty"`
	// ScheduledFor is set only by the scheduler, never from request JSON.
	ScheduledFor *time.Time `json:"-"`
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
	// TimeoutSeconds bounds one attempt; 0 uses the runner default (1h).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// RetryBackoffSeconds waits between attempts.
	RetryBackoffSeconds int      `json:"retry_backoff_seconds,omitempty"`
	Description         string   `json:"description,omitempty"`
	Inputs              []string `json:"inputs,omitempty"`
	Outputs             []string `json:"outputs,omitempty"`
	// ResourceProfile names an administrator preset; explicit resources win.
	ResourceProfile string `json:"resource_profile,omitempty"`
	// When skips the node unless a run parameter equals a value.
	When *NodeCondition `json:"when,omitempty"`
}

// NodeCondition is the only conditional form the runner evaluates: run the
// node when parameter Param equals Equals (compared as strings). Skipped
// nodes report status "skipped"; their dependents still run.
type NodeCondition struct {
	Param  string `json:"param"`
	Equals string `json:"equals"`
}

// PipelineTrigger declares how a definition starts. Schedule triggers use
// five-field cron in an IANA timezone; the scheduler maintains NextRunAt,
// LastRunAt and LastRunID.
type PipelineTrigger struct {
	Type      string     `json:"type"`
	Cron      string     `json:"cron,omitempty"`
	Timezone  string     `json:"timezone,omitempty"`
	Paused    bool       `json:"paused,omitempty"`
	Topic     string     `json:"topic,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	LastRunID string     `json:"last_run_id,omitempty"`
}

// Overridable lists what a manual run may change for one execution.
type Overridable struct {
	Parameters  []string `json:"parameters,omitempty"`
	Image       bool     `json:"image,omitempty"`
	Resources   bool     `json:"resources,omitempty"`
	Retries     bool     `json:"retries,omitempty"`
	Timeout     bool     `json:"timeout,omitempty"`
	Nodes       bool     `json:"nodes,omitempty"`
	Parallelism bool     `json:"parallelism,omitempty"`
}

type PipelineDefinition struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	ExecutionMode string            `json:"execution_mode"`
	Jobs          []PipelineJob     `json:"jobs"`
	RepositoryURL string            `json:"repository_url,omitempty"`
	CommitSHA     string            `json:"commit_sha,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Description   string            `json:"description,omitempty"`
	Triggers      []PipelineTrigger `json:"triggers,omitempty"`
	Overridable   *Overridable      `json:"overridable,omitempty"`
	Parameters    map[string]any    `json:"parameters,omitempty"`
	// Revision increments on every saved change; revisions are immutable.
	Revision     int    `json:"revision,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	OwnerSubject string `json:"owner_subject,omitempty"`
}

type UpsertPipelineDefinitionRequest struct {
	ProjectID     string            `json:"project_id"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	ExecutionMode string            `json:"execution_mode"`
	Jobs          []PipelineJob     `json:"jobs"`
	RepositoryURL string            `json:"repository_url,omitempty"`
	CommitSHA     string            `json:"commit_sha,omitempty"`
	Description   string            `json:"description,omitempty"`
	Triggers      []PipelineTrigger `json:"triggers,omitempty"`
	Overridable   *Overridable      `json:"overridable,omitempty"`
	Parameters    map[string]any    `json:"parameters,omitempty"`
	// Message describes the change for revision history.
	Message string `json:"message,omitempty"`
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
	Source           *ModelSource       `json:"source,omitempty"`
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
	Source       *ModelSource       `json:"source,omitempty"`
	ProjectID    string             `json:"project_id"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	ArtifactURI  string             `json:"artifact_uri"`
	ServingImage string             `json:"serving_image,omitempty"`
	Metrics      map[string]float64 `json:"metrics"`
}

type ModelSource struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Task       string `json:"task,omitempty"`
	Library    string `json:"library,omitempty"`
	License    string `json:"license,omitempty"`
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
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Endpoint    string     `json:"endpoint"`
	SecretRef   string     `json:"secret_ref"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	Message     string     `json:"message,omitempty"`
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
