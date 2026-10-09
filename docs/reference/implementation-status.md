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
| Users, groups, roles and IAM-style policies | **Bundled control plane** | Pure evaluator (default deny, explicit deny wins, `kionga:` resource patterns, IP/UTC-hours/resource-tag/resource-profile conditions) with managed role baselines; groups, versioned policies with immutable revisions and attachments stored as audited documents; anti-escalation, self-lockout and last-admin protection; `authorize()` enforces pipelines, projects, workspace launch, features, user provisioning, connections, blog and IAM administration; 403 denials carry the deciding statement; explain/effective APIs and a console simulator. Models, agents, functions and storage stay on role + project assignment. See [access control](../guides/access-control.md). |
| Shared Jupyter workbench | **Bundled** | Persistent `/workspace`, Jupyter AI, SDK/modules, coding-agent CLIs, and S3FS-mounted object-store buckets |
| Shared browser IDE | **Optional Compose profile** | `make ide-up`; code-server mounts the same persistent workspace as Jupyter |
| Per-user Jupyter/IDE workspaces | **Kubernetes scale path** | `KiongaWorkspace` reconciles bounded workloads, PVC, service, credentials, suspension, and revocation from user grants |
| Container pipelines | **Implemented and verified (trusted Compose profile)** | Prefect runs validated container DAGs with ready-set scheduling (a node starts when its own dependencies succeed), bounded parallelism, per-node timeout/retry backoff, `when` conditions, skip-downstream-on-failure, rerun-from-failure; nodes report start/end, attempt, exit code, container id and image digest. Verified end to end in the browser with parallel-branch overlap. |
| Pipeline definitions | **Implemented and verified** | `kionga.dev/v1 Pipeline` YAML and form editors over one canonical definition; field/node/line validation (cycle paths, missing deps, cron/timezone); immutable revisions with diff and rollback; runs pin revision + SHA-256, overrides, policy decision and image digests; JSON Schema in `contracts/pipeline`. Git: export/import through the YAML API; Kionga never commits for you. |
| Schedules | **Implemented and verified** | Cron + IANA timezone triggers, leased single scheduler, exactly-once slot claims, bounded catch-up, runs as the schedule owner with quotas; pause/resume in the console |
| Manual run configuration | **Implemented and verified** | Server-enforced preflight: overridable parameters, digest-pinned and registry-allowlisted images, resources within quota, retries/timeouts, node selection, parallelism, rerun-from-failure; preflight summary before start |
| Logs and events | **Implemented and verified locally** | Structured `log_entries` (PostgreSQL, RLS) with runner streaming, platform step events, redaction, filters, cursor pagination, SSE tail, retention, project isolation; node log panel and Logs page |
| Log export to Elasticsearch/OpenSearch | **Implemented, unverified against a live cluster** | Separate adapters (data stream vs daily index), at-least-once cursor, partial-failure retry, dead-letter counts, TLS/API key/basic auth from files, health states on the Platform page; tested against protocol fakes only |
| Kubernetes execution of pipeline nodes | **Planned** | The gateway does not create Jobs/Pods for nodes; no Pod events are collected. Executor contract and typed workload ids are in place. |
| General container-DAG execution | **Bundled locally; Kubernetes scale path** | `pipeline-definition/mlaiops` translates validated container jobs into Docker workloads in the trusted single-operator profile. Its socket is root-equivalent, so distributed teams use Kubernetes-native workers/KFP rather than exposing a node runtime socket |
| Workload resource admission | **Bundled control plane** | For normal users, project templates must fit service/profile/GPU grants; the widest concurrent pipeline layer must fit CPU/RAM/GPU; functions must fit CPU/RAM and count quotas; all owned agents reserve aggregate CPU/RAM/GPU and trace-sidecar capacity at maximum replicas; GPU types use strict Kubernetes extended-resource names; admin/operator capacity bypass is explicit |
| Experiments and model registry | **Bundled** | MLflow with PostgreSQL backend and MinIO artifacts |
| Local model serving | **Bundled** | Serving manager launches live `mlflow models serve` containers and proxies predictions |
| Agents, memory, tools, and observability | **Bundled LangGraph; opt-in framework adapters** | LangGraph checkpoints and callbacks; Agno, NOOA, and custom request/result adapters with example factories. Non-LangGraph persistence/provider setup is application-owned, SSE is buffered, and unknown usage is explicit. NOOA needs a separate Python 3.12–3.13 image and operator-provided isolation. Compose serves one factory; Kubernetes uses isolated per-agent workloads. See [framework guide](../guides/agent-frameworks.md). |
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
