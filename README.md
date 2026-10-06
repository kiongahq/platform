# Kionga: ml-ai-ops-platform

Kionga is a self-hosted control plane for classical ML, data-centric AI, and agentic AI
workloads. Go owns
infrastructure services and Python owns the ML-facing SDK and workload primitives.

## Backend implemented

- Durable control-plane API for Git-native projects, reusable pipelines, functions, models, agents, tools, connections, and
  immutable audit events
- Versioned project-template catalog for production ML, distributed deep learning,
  production agents, full-stack AI applications, and expert-owned Python systems
- Versioned Kubernetes CRDs, RBAC, network isolation, and reconciled per-user Jupyter/IDE workspaces
- Online feature gateway with a Feast-compatible request shape
- S3/MinIO proxy generating bounded AWS SigV4 URLs
- OpenAI-compatible LLM reverse proxy with asynchronous trace emission
- Prometheus component-health collector
- Standard API clients for KFP, MLflow, Langfuse, and Kafka REST Proxy
- PostgreSQL repositories with transactional Kafka outbox delivery
- OIDC/JWKS authentication with tenant and role enforcement
- Leader-elected Kubernetes controllers for resource-bounded, CPU-autoscaled agents
  with collision-safe, immutable-ID-derived Deployments and Services, plus pipeline,
  model, and KServe reconciliation
- Kafka lifecycle worker with manual offset commits, bounded retries, and
  at-least-once, idempotent translation of durable commands into Kionga CRDs
- Guided onboarding with active infrastructure checks and readiness scoring
- Bundled Prefect `pipeline-definition/mlaiops` execution for validated container
  DAGs: concurrent ready layers, OCI CPU/RAM/GPU limits, parameters and dependency
  outputs, retries, timeouts, bounded logs, cleanup, Dagre visualization, and Git lineage
- Resource-aware heavyweight training intent with framework/accelerator validation,
  GPU profiles, resumable checkpoints, and a Kubernetes multi-node scale path
- OpenFaaS-backed OCI functions with HTTP, async, Cron, and Kafka trigger configuration
- Admin resource profiles and deny-by-default service/project/storage/function quotas
- Aggregate agent admission at maximum replica capacity, including the trace sidecar,
  with strict Kubernetes extended-resource validation for GPUs
- Quality-gated model promotion, canary deployment and rollback, with optional
  model-specific exact-dependency serving images and a generic serving fallback
- Agent sessions, traces, tools, token usage and cost aggregation
- Dex, Vault, CloudNativePG backup and scoped NetworkPolicy assets
- Typed Python SDK, pipeline compiler, tool registry, and tracing primitive
- Single-binary CLI for common platform operations
- Container and Kubernetes deployment assets

The embedded console exposes these backend contracts directly: administrators provision
users and resources; engineers connect Git, deploy functions, compose flows, run workloads,
and inspect live metadata without bypassing the same authorization boundaries.

The Compose pipeline runner and serving manager control sibling containers through
the Docker socket. That socket is a deliberate root-equivalent trust boundary for a
trusted laptop or single-operator VM, not multi-tenant isolation. Distributed-team
deployments use Kubernetes-native workers/KFP and KServe without exposing a node's
container-runtime socket to platform workloads.

## Quick start

The full platform requires Docker Engine with Compose v2, 8 GB of Docker memory,
and roughly 25–35 GB of free Docker storage for the first build. Go and Python are
only required when building or testing outside containers.

```bash
make local-up
```

That command performs the whole bootstrap: it checks Docker, limits concurrent
downloads, retries transient registry/CDN failures, builds the local images, waits
for the API, and creates Kafka topics. Re-running it is safe and resumes cached image
layers. `make local` is a shorter alias.

Normal starts reuse local images and build any that are missing. After changing
Dockerized source or dependencies, run `make local-rebuild` once to rebuild the
affected images and start the stack.

Open <http://localhost:8080>, choose **Open console**, and sign in with the local
development account `admin` / `mlaiops-local`. If port 8080 is occupied, start with
`GATEWAY_PORT=18080 make local-up` and use that port instead.

The SDK and CLI use the same API:

Create an API key in **Settings → API keys** and export it as `MLAIOPS_TOKEN`
before using the SDK or CLI. Local API requests now require authentication too.
Open Jupyter or the IDE from the console to reuse your signed-in session. Select
a project first to keep its context across pipelines, models, agents, and tools.

```python
from mlaiops_sdk import MLAIOpsClient

with MLAIOpsClient(actor="engineer@example.com") as client:
    project = client.create_project(
        "churn",
        template="production-ml",
        framework="xgboost",
        accelerator="cpu",
    )
    run = client.submit_pipeline(project.id)
```

## Build and verify

```bash
make verify
make test-integration
```

Builds produced in `bin/`:

```text
mlaiops-gateway
mlaiops-operator
mlaiops-integration-worker
mlaiops-trace-proxy
mlaiops-feature-gateway
mlaiops-storage-proxy
mlaiops-metrics-collector
mlaiops-serving-manager
mlaiops-cli
```

## Architecture

```text
Python SDK / CLI / UI
          │ HTTP + JSON
          ▼
  Go control-plane gateway ───────────────► durable audit/state
          │
          ├── Prefect          (Compose pipelines)
          ├── MLflow           (experiments and registry)
          ├── KServe           (model and LLM serving)
          ├── Feast / Redis    (features)
          ├── MinIO / S3       (artifacts)
          ├── Kafka            (events)
          ├── OpenFaaS         (functions and event-driven microservices)
          └── Langfuse         (LLM traces and prompts)

  Access grant ──► KiongaWorkspace ──► bounded Jupyter + IDE + persistent disk
  Lifecycle CRDs ──► operator reconciliation ──► ID-addressed workloads and traffic
  Scale path: Kubernetes + KFP/Argo + KServe; external functions: OpenFaaS
```

## Documentation

**Pretrained models:** connect a personal account in Settings, then use the
Hugging Face Hub panel under Models to register a pinned revision and generate
workspace download code. See the [Hugging Face guide](docs/guides/huggingface.md)
for encrypted credential setup, SDK usage, gated models, and deployment boundaries.

Full documentation lives in [`docs/`](docs/index.md) and builds into a browsable site
with MkDocs Material — architecture, every service and module, installation,
configuration reference, the REST API, RBAC, and operations:

```bash
make docs-install     # pip install mkdocs-material
make docs-serve       # live preview at http://localhost:8000
make docs-build       # strict static build into site/
```

Start at [`docs/index.md`](docs/index.md). The
[implementation status](docs/reference/implementation-status.md) distinguishes
what is bundled locally, enabled by an optional profile, configured externally,
or delivered on the Kubernetes scale path. The
[project-template catalog](docs/guides/project-templates.md) covers heavyweight ML,
distributed training, agent projects, and full-stack AI starters; the
[identifier migration](docs/reference/kionga-migration.md) covers upgrades to the
Kionga runtime and CRDs.

## Important scope boundary

This repository implements the platform-owned integration and control services. It does not
fork or vendor Kafka, MinIO, KFP/Argo, MLflow, Feast, KServe, Redis, PostgreSQL, Langfuse,
or OpenFaaS. Compose bundles the upstream services listed in the implementation-status
matrix; Kubernetes engines and OpenFaaS are connected through standard APIs.

The architecture exclusion policy is enforced in CI.
