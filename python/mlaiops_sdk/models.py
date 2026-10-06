from datetime import datetime

from pydantic import BaseModel, Field


class GitRepository(BaseModel):
    url: str
    provider: str
    default_branch: str
    last_commit: str | None = None
    synced_at: datetime | None = None


class Project(BaseModel):
    id: str
    name: str
    description: str
    template: str
    template_version: str = ""
    framework: str = ""
    accelerator: str = ""
    requested_profile: str = ""
    capabilities: list[str] = Field(default_factory=list)
    scaffold_command: str = ""
    namespace: str
    status: str
    created_at: datetime
    owner_subject: str | None = None
    repository: GitRepository | None = None


class ProjectTemplate(BaseModel):
    id: str
    version: str
    name: str
    category: str
    description: str
    frameworks: list[str]
    accelerators: list[str]
    capabilities: list[str]
    required_services: list[str]
    recommended_profile: str


class PipelineRun(BaseModel):
    id: str
    project_id: str
    name: str
    status: str
    progress: int
    created_at: datetime
    updated_at: datetime
    parent_run_id: str | None = None
    engine_run_id: str | None = None
    definition_id: str | None = None
    execution_mode: str | None = None
    parameters: dict = Field(default_factory=dict)
    steps: list[dict] = Field(default_factory=list)
    logs: list[dict] = Field(default_factory=list)


class PipelineDefinition(BaseModel):
    id: str
    project_id: str
    name: str
    version: str
    execution_mode: str
    jobs: list[dict]
    repository_url: str | None = None
    commit_sha: str | None = None
    created_at: datetime
    updated_at: datetime


class Function(BaseModel):
    name: str
    project_id: str = ""
    image: str
    status: str
    replicas: int = 0
    env_vars: dict[str, str] = Field(default_factory=dict)
    annotations: dict[str, str] = Field(default_factory=dict)
    cpu: str | None = None
    memory: str | None = None
    created_at: datetime | None = None
    updated_at: datetime | None = None


class Model(BaseModel):
    source: dict[str, str] | None = None
    id: str
    project_id: str
    name: str
    version: str
    stage: str
    artifact_uri: str
    serving_image: str | None = None
    metrics: dict[str, float]
    created_at: datetime
    gate_status: str = ""
    deployment_status: str = ""
    canary_weight: int = 0
    endpoint_url: str | None = None
    previous_stage: str | None = None


class AgentAutoscaling(BaseModel):
    min_replicas: int = 1
    max_replicas: int = 1


class AgentResources(BaseModel):
    cpu: str = "500m"
    memory: str = "1Gi"
    gpu: int = 0
    gpu_type: str | None = None


class Agent(BaseModel):
    id: str
    project_id: str
    owner_subject: str | None = None
    name: str
    version: str
    image: str
    graph_module: str
    llm_backend: str
    status: str
    endpoint_url: str | None = None
    replicas: int
    autoscaling: AgentAutoscaling = Field(default_factory=AgentAutoscaling)
    resources: AgentResources = Field(default_factory=AgentResources)
    canary_weight: int
    tools: list[str]
    created_at: datetime


class Tool(BaseModel):
    id: str
    name: str
    version: str
    description: str
    tags: list[str]
    input_schema: dict
    status: str
    created_at: datetime


class Connection(BaseModel):
    id: str
    name: str
    type: str
    endpoint: str
    secret_ref: str
    status: str
    created_at: datetime
    checked_at: datetime | None = None
    message: str | None = None


class ReadinessItem(BaseModel):
    key: str
    label: str
    status: str
    description: str
    action: str | None = None


class Readiness(BaseModel):
    percent: int
    ready: bool
    items: list[ReadinessItem]


class AgentSession(BaseModel):
    id: str
    agent_id: str
    user_id: str
    status: str
    current_node: str
    turns: int
    input_tokens: int
    output_tokens: int
    cost_usd: float
    started_at: datetime
    updated_at: datetime


class AgentTrace(BaseModel):
    id: str
    agent_id: str
    session_id: str
    name: str
    status: str
    duration_ms: int
    tokens: int
    metadata: dict | None = None
    created_at: datetime


class AuditEvent(BaseModel):
    id: str
    action: str
    resource: str
    resource_id: str
    actor: str
    metadata: dict | None = None
    created_at: datetime
