# Go packages

The Go module is `github.com/ml-ai-ops/platform` (Go 1.25). Its direct application
dependencies include `github.com/jackc/pgx/v5` for Postgres; Kubernetes controller
libraries and their transitive dependencies support the scale path. Commands live under `go/cmd/`,
reusable logic under `go/internal/`, and public API types under `go/pkg/`.

## Commands (`go/cmd`)

Each builds to a `mlaiops-<name>` binary (`make build`) and to a container image via
the root `Dockerfile` (`--build-arg SERVICE=<name>`).

| Command | Binary | Role |
| --- | --- | --- |
| `gateway` | `mlaiops-gateway` | REST API + embedded console |
| `operator` | `mlaiops-operator` | Kubernetes reconcilers (scale path) |
| `integration-worker` | `mlaiops-integration-worker` | Kafka → CRD lifecycle worker (scale path) |
| `trace-proxy` | `mlaiops-trace-proxy` | LLM egress + trace capture |
| `feature-gateway` | `mlaiops-feature-gateway` | Online feature retrieval |
| `storage-proxy` | `mlaiops-storage-proxy` | SigV4 S3 URLs + browse |
| `metrics-collector` | `mlaiops-metrics-collector` | Prometheus platform metrics |
| `serving-manager` | `mlaiops-serving-manager` | Model-serving container control |
| `cli` | `mlaiops` | Operator/engineer CLI |

## Internal packages (`go/internal`)

### `auth`

Authentication and authorization. OIDC/JWKS verification (`Verifier`), the RBAC
model (`Allowed`, `Permissions`, the role constants), local/OIDC browser sessions,
personal-key verification, and the `RBAC` middleware that runs on every request.
Roles: `admin`, `operator`, `user`, `engineer`, `viewer`, `service`. See
[RBAC & security](../reference/rbac.md).

### `httpapi`

The gateway's HTTP layer. `server.go` defines the router (stdlib `net/http`
ServeMux), all handlers, and the JSON helpers (`writeJSON`, `writeMutation`,
`decode`). `openapi.go` serves the OpenAPI document. This is where every
`/api/v1/*` endpoint is wired.

`GET /api/v1/project-templates` exposes the same versioned project contract used by
the Python SDK and workspace generator. Project creation returns the resolved
framework, accelerator, profile, capabilities, template version, and scaffold
command.

### `store`

Persistence. `repository.go` is the interface contract; `store.go` is the local
file implementation; `postgres.go` is the PostgreSQL implementation with the
transactional **outbox** (`outbox.go`) — every mutation writes resource + audit +
outbox atomically. Handles user grants/access requests, personal-key digests, Git
metadata, functions and DAG definitions, run steps, agent sessions, model endpoints,
blog posts, and feature views.

### `integrations`

HTTP clients for external systems: MLflow, KFP, Langfuse, Prefect (`client.go`),
the Kafka REST producer/consumer (`kafka_consumer.go`), and OpenFaaS. `dispatcher.go`
routes durable commands and derives collision-safe agent resource names from the
immutable control-plane ID. `lifecycle_worker.go` is the at-least-once boundary: it
retries record dispatch, commits Kafka offsets manually only after the complete
batch succeeds, retries commit without redispatch, and returns a terminal error so
the process restarts from the last commit when retries are exhausted. `NewPrefect`
normalizes the API base URL so paths don't double-prefix `/api`.

### `serving`

Model serving over the Docker Engine API (`manager.go`). `Manager.Deploy` does a
replace → create → start with labels and restart policy, chooses the model's
framework-compatible image with a configured fallback, and applies capability,
privilege-escalation, and PID hardening. It supports an `APIVersion` override for
older daemons. Backs the serving-manager command.

### `storage`

Object-store access. `presign.go` builds AWS SigV4 URLs (with a verified,
signature-correct path); `browse.go` lists buckets/objects and previews objects.
Backs the storage-proxy command.

### `feature`

Online feature retrieval. `redis.go` (online store), `feast.go` (Feast-compatible
request shape), `service.go` (HTTP handlers). Backs the feature-gateway command.

### `traceproxy`

The OpenAI-compatible reverse proxy (`proxy.go`) that forwards LLM calls upstream
and asynchronously emits each call to the Kafka traces topic. Backs the trace-proxy
command.

### `metrics`

`collector.go` (component health scraping) and `platform.go` (`PlatformCollector`,
the platform metric set: pipeline outcomes, agent usage, real-time stats).
Prometheus exposition. Backs the metrics-collector command.

### `operator`

Kubernetes controllers for the scale path: `agent_controller.go`,
`lifecycle_controllers.go`, workspace reconciliation, and deterministic plans
(`reconciler.go`) for the Kionga CRDs. Agent reconciliation applies Kubernetes
resource requirements and probes to the immutable-ID-derived Deployment/Service,
owns a 70%-CPU HorizontalPodAutoscaler when maximum replicas exceed minimum
replicas without fighting its replica writes, and derives CR readiness from
available pods. Operator RBAC includes the autoscaling resource; a resource-metrics
API is required for elastic scaling.

### `platform`

Live derivation of the component-health grid (`Components`) and the shared
**catalog** (`Catalog`) from real connections, models, features, agents, and tools —
no hardcoded data.

## Public API types (`go/pkg`)

| Package | Contents |
| --- | --- |
| `pkg/api` | Request/response types shared by the gateway, CLI, and clients (`ProjectTemplate`, `Project`, `PipelineRun`, `Model`, `Agent`, `Connection`, request bodies, `Page[T]`, `APIError`); `project_templates.go` owns versioned template resolution and framework/accelerator/profile validation |
| `pkg/kube/v1alpha1` | Typed Kubernetes CRD definitions (scale path) |

## Testing

Every package has table-driven `_test.go` files. The CI `go` job runs `gofmt -l`
(must be empty), `go vet`, `go test -race`, the tagged integration suite against a
real Postgres, `go build ./cmd/...`, and the banned-tech scan. Run locally with
`make test-go` and `make test-integration`.
