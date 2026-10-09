# Kionga

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
- S3-compatible storage proxy generating bounded AWS SigV4 URLs
- OpenAI-compatible LLM reverse proxy with asynchronous trace emission ([trace-proxy](https://github.com/kiongahq/trace-proxy))
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
- Typed Python SDK, pipeline compiler, tool registry, and tracing primitive ([sdk-python](https://github.com/kiongahq/sdk-python))
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

## Repositories

This repository is Kionga's control plane: the Go gateway and its embedded console,
auth and IAM policies, the PostgreSQL store, scheduler, pipeline validation and
execution, logs, editorial, the Kubernetes operator, feature gateway, storage proxy,
integration worker, log exporter and CLI. The other parts live beside it in the
[kiongahq](https://github.com/kiongahq) organization:
[sdk-python](https://github.com/kiongahq/sdk-python),
[agent-runtime](https://github.com/kiongahq/agent-runtime),
[pipeline-runner](https://github.com/kiongahq/pipeline-runner),
[serving](https://github.com/kiongahq/serving),
[trace-proxy](https://github.com/kiongahq/trace-proxy),
[workspace](https://github.com/kiongahq/workspace),
[contracts](https://github.com/kiongahq/contracts) (a submodule here, at `contracts/`),
[deploy](https://github.com/kiongahq/deploy) and
[docs](https://github.com/kiongahq/docs). See
[Repositories](https://kiongahq.github.io/docs/overview/repositories/) for how they
connect.

## Quick start

Run the whole platform from [deploy](https://github.com/kiongahq/deploy), which
clones this repository and the others side by side and builds them together:

```bash
mkdir ~/kionga && cd ~/kionga
git clone git@github.com:kiongahq/deploy.git
make -C deploy workspace     # clones platform, sdk-python, agent-runtime, ...
make -C deploy local-up
```

Open [http://localhost:8080](http://localhost:8080) and sign in with the local development account
`admin` / `mlaiops-local`. Create an API key in **Settings → API keys** and export it
as `MLAIOPS_TOKEN` to use the SDK or CLI.

## Build and verify

```bash
git clone --recurse-submodules git@github.com:kiongahq/platform.git
make verify              # gofmt, vet, race tests, builds, console syntax
make test-ui             # console unit tests (jsdom)
make test-integration    # PostgreSQL integration tests (Docker)
make test-browser        # Playwright acceptance against a running stack
make run                 # gateway alone on :8080 with a file-backed store
```

Binaries land in `bin/` (`mlaiops-gateway`, `-operator`, `-integration-worker`,
`-feature-gateway`, `-storage-proxy`, `-metrics-collector`, `-log-exporter`, `-cli`).
CI publishes the same services as `ghcr.io/kiongahq/<service>`.

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
          ├── RustFS / S3      (artifacts)
          ├── Kafka            (events)
          ├── OpenFaaS         (functions and event-driven microservices)
          └── Langfuse         (LLM traces and prompts)

  Access grant ──► KiongaWorkspace ──► bounded Jupyter + IDE + persistent disk
  Lifecycle CRDs ──► operator reconciliation ──► ID-addressed workloads and traffic
  Scale path: Kubernetes + KFP/Argo + KServe; external functions: OpenFaaS
```

## Documentation

[https://kiongahq.github.io/docs/](https://kiongahq.github.io/docs/), built from [kiongahq/docs](https://github.com/kiongahq/docs):
architecture, every service and module, installation, configuration, the REST API,
RBAC and operations. Start with
[implementation status](https://kiongahq.github.io/docs/reference/implementation-status/)
for what is bundled, optional, external or on the Kubernetes scale path, and the
[managed Kubernetes](https://kiongahq.github.io/docs/operations/managed-kubernetes/) and
[public VM](https://kiongahq.github.io/docs/operations/public-vm/) guides before a
public deployment.

## Important scope boundary

The Kionga repositories implement the platform-owned integration and control services. It does not
fork or vendor Kafka, RustFS, KFP/Argo, MLflow, Feast, KServe, Redis, PostgreSQL, Langfuse,
or OpenFaaS. Compose bundles the upstream services listed in the implementation-status
matrix; Kubernetes engines and OpenFaaS are connected through standard APIs.

The architecture exclusion policy is enforced in CI.
