# REST API reference

The gateway exposes a JSON REST API under `/api/v1`. A searchable human-facing
reference is served at `GET /api-docs.html`; the machine-readable OpenAPI
document remains available at `GET /api/openapi.json`. Every request is authorized by
[RBAC](rbac.md).

- **Base URL (local):** `http://localhost:${GATEWAY_PORT:-8080}`; shell examples
  can export `MLAIOPS_URL=http://localhost:8080`
- **Content type:** `application/json`
- **Auth:** local API requests use `MLAIOPS_LOCAL_ROLE`; the browser console requires
  a local session. Hosted clients send `Authorization: Bearer <token>` using OIDC
  or a scoped personal key. Internal reporters use the service token.
- **Errors:** non-2xx responses return `{"error": "<code>", "message": "<detail>"}`.
- **Lists:** most list endpoints return `{"items": [...], "total": N}`.

## Identity & health

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/health` | public | Liveness (`{"status":"ok",...}`) |
| `GET` | `/api/v1/me` | any | Caller identity, roles, and effective permissions |
| `GET` | `/api/v1/admin/users` | admin/operator | List provisioned users |
| `PUT` | `/api/v1/admin/users/{subject}` | admin/operator | Assign role, services, projects, storage, and compute quotas |
| `DELETE` | `/api/v1/admin/users/{subject}` | admin/operator | Revoke all access |
| `GET` | `/api/v1/admin/resource-profiles` | admin/operator | List canonical starter/team/power/GPU/custom allocations |
| `GET` | `/api/v1/access-requests` | authenticated user | List the caller's access requests |
| `POST` | `/api/v1/access-requests` | authenticated user | Request one or more platform services |
| `GET` | `/api/v1/admin/access-requests` | admin/operator | List the organization approval queue |
| `PATCH` | `/api/v1/admin/access-requests/{id}` | admin/operator | Approve or reject an access request |
| `GET` | `/api/v1/settings/tokens` | signed-in user | List personal API keys (secrets are never returned) |
| `POST` | `/api/v1/settings/tokens` | signed-in user | Create a scoped, expiring personal API key |
| `DELETE` | `/api/v1/settings/tokens/{id}` | key owner | Revoke a personal API key immediately |
| `GET` | `/api/v1/blogs` | public | List published engineering posts |
| `GET` | `/api/v1/blogs/{slug}` | public | Read a published post |
| `GET` / `POST` | `/api/v1/admin/blogs` | admin/operator | List drafts or create a post |
| `PUT` / `DELETE` | `/api/v1/admin/blogs/{id}` | admin/operator | Update, publish, or delete a post |
| `GET` | `/api/v1/dashboard` | viewer+ | Workspace summary (counts + recent runs) |
| `GET` | `/api/v1/onboarding/readiness` | viewer+ | Onboarding readiness score |
| `GET` | `/api/openapi.json` | public | OpenAPI document |

The human API explorer is `/api-docs.html`. It renders the OpenAPI document in the
browser; `/api/openapi.json` remains intentionally machine-readable JSON.

## Projects

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/project-templates` | projects read | List versioned ML, distributed-training, agent, full-stack, and foundation templates |
| `GET` | `/api/v1/project-templates/{id}` | projects read | Read frameworks, accelerators, capabilities, required services, and recommended profile |
| `GET` | `/api/v1/projects` | viewer+ | List projects |
| `POST` | `/api/v1/projects` | engineer+ | Create a versioned template project, optionally connected to Git |
| `GET` | `/api/v1/projects/{id}` | viewer+ | Read project and repository metadata |
| `PUT` | `/api/v1/projects/{id}/repository` | Git service | Connect a credential-free HTTPS/SSH repository reference |

Create a heavyweight project with explicit execution intent:

```bash
curl -s -X POST "$MLAIOPS_URL/api/v1/projects" \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"vision-foundation-eval",
    "description":"Multi-GPU evaluation and fine-tuning",
    "template":"distributed-training",
    "framework":"pytorch-ddp",
    "accelerator":"multi-gpu",
    "requested_profile":"gpu",
    "repository_url":"git@github.com:acme/vision-foundation-eval.git",
    "default_branch":"main"
  }'
```

The response pins `template_version`, resolved framework/accelerator/profile,
capabilities, and `scaffold_command`. Unsupported combinations return `422
validation_error` rather than creating unusable desired state. For normal users,
missing required services or profile/GPU capacity returns `403
resource_not_provisioned`; `single-gpu` requires one provisioned GPU and
`multi-gpu` requires at least two. Admin/operator principals bypass these
provisioning checks.

## Pipelines

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/pipelines/runs` | viewer+ | List runs |
| `GET` / `POST` | `/api/v1/pipelines/definitions` | pipelines service | List or create versioned job DAGs |
| `GET` / `PUT` | `/api/v1/pipelines/definitions/{id}` | pipelines service | Read or update a reusable flow |
| `POST` | `/api/v1/pipelines/submit` | engineer+ | Submit a built-in or definition-backed run with parameters |
| `GET` | `/api/v1/pipelines/runs/{id}` | viewer+ | Run detail: steps (DAG) + logs |
| `POST` | `/api/v1/pipelines/runs/{id}/cancel` | engineer+ | Cancel (propagates to the engine) |
| `POST` | `/api/v1/pipelines/runs/{id}/retry` | engineer+ | Retry |
| `POST` | `/api/v1/pipelines/runs/{id}/steps` | service | Step transition (from the executing flow) |

`jobs[].depends_on` is the graph contract. The gateway rejects duplicate names,
missing dependencies, cycles, invalid resource quantities, and unresolved function
references before persistence. Run detail returns the validated steps and logs used
by the console's Dagre renderer; visual coordinates are not part of the API and have
no scheduling meaning.

For a normal user, the gateway calculates dependency depth and sums CPU, memory,
and GPU requests for every concurrently runnable layer. A definition whose widest
layer exceeds the user's grant returns `403 resource_not_provisioned`. Sequential
layers are not incorrectly added together. Admin/operator principals bypass the
resource-admission check.

## Models

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/models` | viewer+ | List models |
| `POST` | `/api/v1/models` | engineer+ | Register (`project_id`, `name`, `version`, `artifact_uri`, optional `serving_image`, `metrics`) |
| `POST` | `/api/v1/models/{id}/promote` | engineer+ | Promote (`stage`) |
| `POST` | `/api/v1/models/{id}/deploy` | engineer+ | Deploy → live endpoint (`canary_weight`) |
| `POST` | `/api/v1/models/{id}/rollback` | engineer+ | Undeploy / roll back |
| `POST` | `/api/v1/models/{id}/predict` | engineer+ | Proxy a prediction to the live endpoint |

`serving_image` pins the model version to a framework-compatible OCI image. The
image must provide the `mlflow` CLI, artifact-store support, and the exact runtime
dependencies needed to load the model. When omitted, the serving manager uses its
configured `SERVE_IMAGE` fallback.

## Agents

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/agents` | viewer+ | List agents with live `status` and `endpoint_url` readiness enrichment |
| `POST` | `/api/v1/agents` | engineer+ | Deploy an agent |
| `PUT` | `/api/v1/agents/{id}/traffic` | engineer+ | Set canary weight |
| `POST` | `/api/v1/agents/{id}/invoke` | engineer+ | Health-gate and run one turn through the selected runtime |
| `GET` | `/api/v1/agents/{id}/sessions` | viewer+ | List sessions |
| `GET` | `/api/v1/agents/{id}/traces` | viewer+ | List traces |
| `GET` | `/api/v1/agents/{id}/usage` | viewer+ | Aggregated tokens/cost/active sessions |
| `POST` | `/api/v1/traces` | service | Record a session/trace (from the runtime) |

`POST /api/v1/agents` accepts the project, name, version, immutable image, graph
entry point (`module:function`), LLM backend, registered tools, an `autoscaling`
object (`min_replicas`, `max_replicas`), and a `resources` object (`cpu`, `memory`,
optional `gpu`, optional `gpu_type`). The defaults are one fixed replica, `500m`
CPU, `1Gi` memory, and no GPU. A positive GPU count defaults `gpu_type` to
`nvidia.com/gpu`. The top-level `replicas` field remains a compatibility input;
`autoscaling` is canonical. The maximum must be at least the minimum and no more
than 100. A non-default GPU type must be a domain-qualified Kubernetes extended
resource name. Native resource keys (`cpu`, `memory`, `storage`, and
`ephemeral-storage`), invalid qualified names, and Kubernetes-reserved domains are
rejected rather than copied into a workload resource map.

For a normal user, admission reserves every agent they own at its maximum replica
count, then evaluates the new request together with that existing capacity. Each
possible pod includes the agent request plus the trace sidecar's `100m` CPU and
`128Mi` memory request; GPUs and maximum replicas are aggregated as well. The total
must fit the provisioned compute/workload grant, and a typed GPU grant must match.
Violations return `403 resource_not_provisioned`; invalid quantities, resource
names, or replica bounds return `422 validation_error`. Admin/operator principals
bypass capacity admission. Use the `production-agent` project template for a
runnable graph/runtime, tests, evaluation fixtures, and container boundary before
deployment.

`GET /api/v1/agents` does not repeat a stale persisted `pending`/`ready` value. It
sets each `endpoint_url`, probes `/healthz` through a bounded worker pool and request
deadline, and returns `ready` only when the selected runtime responds successfully.
On Kubernetes the endpoint is the immutable-ID-derived Service in
`MLAIOPS_AGENT_NAMESPACE`; a non-empty `AGENT_RUNTIME_URL` is the Compose shared
runtime override. `POST /api/v1/agents/{id}/invoke` performs its own bounded health
gate and returns `503` with `error: "agent_not_ready"` without forwarding the turn
when the runtime is unavailable.

## Tools

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/tools` | viewer+ | List tools |
| `POST` | `/api/v1/tools` | engineer+ | Register a tool |

## Features

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/features` | viewer+ | List feature views |
| `POST` | `/api/v1/features` | engineer+ / service | Apply a feature view |
| `POST` | `/api/v1/features/{name}/materialized` | service | Report a materialization (entity count) |

## Storage

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/storage/buckets` | viewer+ | List buckets |
| `GET` | `/api/v1/storage/objects` | viewer+ | List objects (`bucket`, `prefix`) |
| `GET` | `/api/v1/storage/object` | viewer+ | Bounded object preview (`bucket`, `key`) |

## Connections

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/connections` | viewer+ | List connections |
| `POST` | `/api/v1/connections` | admin/operator | Create a secret-backed connection |
| `POST` | `/api/v1/connections/{id}/test` | admin/operator | Actively health-check a connection |

## Components, catalog, prompts

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/components` | viewer+ | Live component-health grid |
| `GET` | `/api/v1/catalog` | viewer+ | Shared catalog (`kind` filter: model/feature/agent/tool) |
| `GET` | `/api/v1/prompts` | viewer+ | Langfuse prompt library (proxy) |

## Real-time & serverless

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/realtime` | viewer+ | Live stream-demo statistics |
| `POST` | `/api/v1/realtime/{demo}` | service | Report stream stats (from the processor) |
| `GET` | `/api/v1/functions` | viewer+ | List serverless functions |
| `POST` | `/api/v1/functions` | functions service | Deploy an OCI function with resource limits and event annotations |
| `DELETE` | `/api/v1/functions/{name}` | functions service | Remove an owned function |
| `POST` | `/api/v1/functions/{name}/invoke` | engineer+ | Invoke a function |
| `POST` | `/api/v1/functions/{name}/invoke-async` | functions service | Queue an invocation and return its call ID |

Normal-user function deployment rejects CPU or memory above the assigned compute
grant as `403 resource_not_provisioned`, independently of the maximum function-count
quota. Admin/operator principals may provision beyond a user profile.

## Events & audit

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/events` | viewer+ | SSE stream of the state digest (live updates) |
| `GET` | `/api/v1/audit` | viewer+ | Immutable audit log |

!!! note "Role column"
    "viewer+" means viewer and above. "engineer+" means engineer, operator, admin.
    "projects read" additionally includes a provisioned normal user assigned the
    `projects` service.
    "service" means the internal token identity (used by in-platform reporters).
    Connections are admin/operator only. The exact matrix is in [RBAC](rbac.md).

## Attribution

Mutations are attributed to an actor. In OIDC mode the actor is the token's
email/subject. Otherwise the `X-MLAIOps-Actor` header is honored (used by internal
services like `pipeline-engine`, `materializer`, `realtime-processor`).
