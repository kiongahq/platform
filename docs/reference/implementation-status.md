# Implementation status

This page is the source of truth for what ships in the repository. “Implemented”
means Kionga owns the API, persistence, authorization, UI, and integration contract.
It does not mean every upstream engine is embedded in the default Compose profile.

## Capability matrix

| Capability | Delivery | Current behavior |
| --- | --- | --- |
| Control plane, console, landing page, API docs | **Bundled** | Go gateway with PostgreSQL persistence, audit/outbox, local login, OIDC/JWKS, RBAC, SSE updates, and the embedded web surfaces |
| Projects and Git metadata | **Bundled** | Create/read projects; connect credential-free HTTPS/SSH repository metadata; scaffold or synchronize source inside a workspace |
| Versioned project templates | **Bundled** | Catalog/API/SDK contracts for production ML, distributed PyTorch training, production agents, full-stack AI, and blank expert projects; framework/accelerator/profile validation, required-service/GPU admission for normal users, and returned scaffold command |
| User access and resource provisioning | **Bundled control plane** | Admin profiles, service/project/bucket grants, CPU/RAM/GPU/storage/run/function quotas, access requests, personal API keys, and enforcement at the gateway |
| Shared Jupyter workbench | **Bundled** | Persistent `/workspace`, Jupyter AI, SDK/modules, coding-agent CLIs, and S3FS-mounted object-store buckets |
| Shared browser IDE | **Optional Compose profile** | `make ide-up`; code-server mounts the same persistent workspace as Jupyter |
| Per-user Jupyter/IDE workspaces | **Kubernetes scale path** | `KiongaWorkspace` reconciles bounded workloads, PVC, service, credentials, suspension, and revocation from user grants |
| Container pipelines | **Bundled (trusted Compose profile)** | Prefect executes the training flow and validated container definitions; ready layers run concurrently as OCI jobs with limits, dependency outputs, retries, timeouts, bounded logs and cleanup; run state/cancel/retry are persisted; pinned open-source Dagre provides presentation-only layout with a compact fallback |
| Reusable pipeline definitions | **Bundled control plane** | Versioned container/function DAGs, dependency/cycle validation, parameters, resources, retries, and Git lineage |
| General container-DAG execution | **Bundled locally; Kubernetes scale path** | `pipeline-definition/mlaiops` translates validated container jobs into Docker workloads in the trusted single-operator profile. Its socket is root-equivalent, so distributed teams use Kubernetes-native workers/KFP rather than exposing a node runtime socket |
| Workload resource admission | **Bundled control plane** | For normal users, project templates must fit service/profile/GPU grants; the widest concurrent pipeline layer must fit CPU/RAM/GPU; functions must fit CPU/RAM and count quotas; all owned agents reserve aggregate CPU/RAM/GPU and trace-sidecar capacity at maximum replicas; GPU types use strict Kubernetes extended-resource names; admin/operator capacity bypass is explicit |
| Experiments and model registry | **Bundled** | MLflow with PostgreSQL backend and MinIO artifacts |
| Local model serving | **Bundled** | Serving manager launches live `mlflow models serve` containers and proxies predictions |
| Agents, memory, tools, and observability | **Bundled; Kubernetes scale path for isolated workloads** | LangGraph runtime, PostgreSQL checkpoints/pgvector memory, trace proxy, Kafka traces, and Langfuse; Compose uses a shared runtime override; Kubernetes uses immutable-ID-derived Services, live readiness/invoke gating, resource-aware pods, and a 70%-CPU HPA when maximum exceeds minimum replicas |
| Functional agent project starter | **Bundled developer tooling** | `production-agent` generates runnable graph/runtime boundaries, typed tool/state examples, deterministic tests/evals, container packaging, and a platform manifest; provider access remains external configuration |
| Heavy/distributed training project | **Bundled starter; Kubernetes scale path for multi-node** | `distributed-training` establishes PyTorch DDP launch, mixed precision, checkpoints, rank-aware tracking, and GPU intent; actual GPU topology/capacity comes from provisioned profiles and the target scheduler |
| Feature store | **Bundled** | Redis online retrieval plus Parquet snapshots in MinIO; Feast-compatible online request shape |
| Real-time processing | **Bundled demos** | Kafka fraud, call-center, and recommendation flows with live stats; deterministic fallbacks until a model/agent is selected |
| Object storage filesystem in Jupyter | **Bundled local workbench** | S3FS mounts configured MinIO buckets under `/workspace/object-store`; production multi-user mounts require per-workspace isolation |
| Functions and event-driven microservices | **External integration** | Project-owned OCI functions, quotas, HTTP/async/Cron/Kafka trigger metadata, invocation, and function DAG execution require `OPENFAAS_URL` |
| Public VM edge | **Deployment overlay** | Caddy TLS and Dex OIDC are added with `make public-up`; internal service ports are closed |
| Kubernetes-native execution/serving | **Scale path** | CRDs, operator, RBAC, NetworkPolicy, workspace reconciliation, KFP/Argo and KServe contracts, plus an at-least-once lifecycle worker with manual Kafka commits; elastic agent scaling requires resource metrics |

## Honest readiness rules

- A bundled service is ready only after its health check passes.
- An external integration remains visibly **not configured** until its endpoint and
  credentials are supplied.
- Control-plane records do not claim that an external workload exists when its
  engine rejected creation.
- Compose is the quickest complete single-user/single-VM experience. Kubernetes is
  required for isolated per-user workspaces and distributed-team resource
  reconciliation.

## Verified local baseline

The repository is expected to pass:

```bash
make verify
make test-integration
make docs-build
make local-up
GATEWAY=http://localhost:8080 ./scripts/demo-smoke.sh
```

The smoke test reports a dynamic pass/skip count. OpenFaaS is skipped unless it is
configured; optional observability checks may also skip while an upstream service
is initializing. Treat any **failure** as a real integration problem.

See [Installation](../getting-started/installation.md) for host requirements and
[Troubleshooting](../operations/troubleshooting.md) for Docker disk, port, and DNS
diagnostics.
