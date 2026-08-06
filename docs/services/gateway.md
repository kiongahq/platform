# Gateway & control-plane services

## gateway

The heart of Kionga. A single Go binary that serves the **REST API** and the
**embedded web console**, and orchestrates every downstream engine.

| | |
| --- | --- |
| **Image** | Built from root `Dockerfile` with `SERVICE=gateway` |
| **Host port** | `8080` (`GATEWAY_PORT`) |
| **Source** | `go/cmd/gateway`, `go/internal/httpapi` |
| **Health** | `GET /api/v1/health` |
| **Store** | PostgreSQL (`DATABASE_URL`) or local JSON file (`MLAIOPS_DATA_PATH`) |

### Responsibilities

- **Owns state** for the versioned project-template catalog, Git-native projects,
  pipeline runs/definitions, functions,
  models, agents, tools, connections, feature views, user access, access requests,
  personal API keys, engineering posts, and immutable audit events.
- **Transactional writes** — every mutation persists its resource, an audit record,
  and Kafka outbox entries in one transaction (Postgres mode).
- **Orchestration & proxying** — drives Prefect (pipelines), the serving manager
  (endpoints), the agent runtime (agent turns), the storage proxy (browse), the
  feature gateway (lookups), and Langfuse (prompts).
- **Fail-closed** — if a downstream engine rejects a request, the control plane
  reflects it honestly (e.g. a run is marked failed, a deploy returns 502).
- **Validates project intent** — canonicalizes template aliases, pins the template
  version, rejects incompatible framework/accelerator selections, and records a
  resource-aware scaffold command without granting capacity.
- **Validates execution graphs** — rejects duplicate/missing dependencies, cycles,
  invalid job resources, and unresolved function references before persistence;
  browser layout never changes execution semantics.
- **Admits agent workloads** — validates replica bounds and strict Kubernetes GPU
  extended-resource names; for normal users, every owned agent's maximum-replica
  CPU/RAM/GPU plus per-pod trace-sidecar overhead must fit the aggregate
  compute/workload grant.
- **Routes live agents** — derives the selected Kubernetes Service from the
  immutable agent ID and `MLAIOPS_AGENT_NAMESPACE` (or uses the explicit Compose
  `AGENT_RUNTIME_URL` override), enriches list results with bounded health probes,
  and rejects invocation with `503 agent_not_ready` before forwarding to an
  unhealthy runtime.
- **Authorization** — the RBAC middleware runs on every request in every mode.
- **Browser sessions** — local credential login for development and OIDC
  authorization-code login with secure HttpOnly cookies when hosted.
- **Live updates** — `GET /api/v1/events` streams a state digest over SSE.
- **Serves the console** — the vanilla-JS UI is embedded via `go:embed`.
- **Publishes engineering writing** — persisted Markdown posts, public
  index/article pages, landing previews, and an admin authoring workflow.

### The embedded console

`go/cmd/gateway/web/` holds the landing page (`index.html`, `landing.css`,
`landing.js`) and console (`console.html`, `app.js`, `styles.css`), embedded into
the binary. It is dependency-free (no framework build step). It renders role-aware
views for workload lifecycle, functions, workspaces, access administration,
settings/tokens, storage, catalog, and platform connections; includes DAG, chat,
prediction, and object-browser interactions; and drives them from the same API
documented under [Reference → REST API](../reference/api.md).

The console's pipeline view uses pinned open-source Dagre for node placement. The
validated API graph remains authoritative and a compact fallback keeps step state
and logs readable if the layout asset is unavailable. `pipeline-graph.js` exposes a
small independently tested analyzer/renderer, surfaces authoring errors, and emits
keyboard-focusable SVG nodes with accessible job/target/status labels.

### Key environment

See the [gateway section of the configuration reference](../getting-started/configuration.md#gateway-control-plane).
The essentials: `DATABASE_URL`, `PREFECT_API_URL`, `SERVING_MANAGER_URL`,
`AGENT_RUNTIME_URL` (Compose) or `MLAIOPS_AGENT_NAMESPACE` (Kubernetes),
`STORAGE_PROXY_URL`, `LANGFUSE_*`, `MLAIOPS_LOCAL_ROLE`, `MLAIOPS_INTERNAL_TOKEN`,
and (public) `OIDC_*`, `MLAIOPS_ALLOWED_ORIGIN`.

## operator

Kubernetes controller for the **scale path** — leader-elected reconcilers that turn
Kionga CRDs (`KiongaAgent`, `KiongaPipelineRun`, `KiongaTool`, `KiongaConnection`,
`KiongaModelPromotion`, `KiongaWorkspace`) into workloads. Agent reconciliation
uses the immutable-ID-derived name for its Deployment and Service, applies
CPU/RAM/GPU requirements and health probes, and reports Ready only after the minimum
replicas are available. It creates an owned 70%-CPU HPA when maximum exceeds minimum
and does not reset HPA-owned replicas; clusters must provide resource metrics, such
as metrics-server. Ships as
`mlaiops-operator`. Not part of the
default Compose stack; used only with the optional Kubernetes deployment.

| | |
| --- | --- |
| **Source** | `go/cmd/operator`, `go/internal/operator` |
| **CRDs** | `config/crd/` |
| **RBAC** | `config/rbac/operator.yaml` |

## metrics-collector

Prometheus exposition of platform metrics: pipeline duration, inference
latency/requests, feature-lookup latency, LLM tokens/cost, agent quality, and active
sessions. Ships as `mlaiops-metrics-collector`, default port `9090`.

| | |
| --- | --- |
| **Source** | `go/cmd/metrics-collector`, `go/internal/metrics` |
| **Config** | `MLAIOPS_METRICS_TARGETS` (gateway/services to scrape) |

## integration-worker

Kafka lifecycle worker that translates durable outbox commands into Kionga CRDs on
the Kubernetes path, including access-grant-driven workspace create/update,
suspension, and revocation. Its Kafka REST consumer disables auto-commit, retries a
failed record with bounded exponential backoff, and manually commits offsets only
after the full poll batch succeeds. Exhausted dispatch or commit retries stop the
worker before another poll, so restart replays from the last durable commit. This is
at-least-once delivery; collision-safe create-or-update reconciliation makes replay
idempotent. Ships as `mlaiops-integration-worker`. Part of the scale-path deployment
(`config/deploy/integration-worker.yaml`).

## cli

Single-binary operator/engineer CLI (`mlaiops`). See [CLI reference](../reference/cli.md).
