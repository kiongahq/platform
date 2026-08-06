# Technology & modules used

Every technology in the stack, why it's here, and where it's pinned. Versions
reflect the images and dependency pins in the repository.

## Languages & runtimes

| Technology | Version | Role |
| --- | --- | --- |
| **Go** | 1.25 (`go/go.mod`) | Control plane, operator, feature gateway, storage proxy, trace proxy, serving manager, metrics collector, CLI |
| **Python** | 3.11 (SDK, runtime); 3.10 (pipeline runner & serving, pinned for parity) | SDK, agent runtime, pipelines, features, real-time demos |
| **JavaScript** | vanilla (no framework) | Embedded web console, no build toolchain |

## Data plane

| Technology | Image / pin | Role |
| --- | --- | --- |
| **PostgreSQL + pgvector** | `pgvector/pgvector:pg16` | Control-plane state, Kafka outbox, MLflow & Langfuse backends, agent checkpoints, vector memory |
| **Redis** | `redis:7.4.5-alpine` | Online feature store |
| **Apache Kafka** | `apache/kafka:3.9.1` (KRaft) | Durable events, LLM traces, real-time topics |
| **Kafka REST Proxy** | Karapace `6.1.4` | Apache-licensed, Confluent-compatible HTTP access to Kafka (keeps Python dependency-free) |
| **MinIO** | `minio/minio` (pinned 2025-07 release) | S3-compatible object storage |
| **MLflow** | `mlflow==3.1.1` (custom image `mlaiops-mlflow`) | Experiment tracking + model registry |

## Orchestration & serving

| Technology | Pin | Role |
| --- | --- | --- |
| **Prefect** | `prefect>=3.0` (`prefecthq/prefect:3-latest`) | Pipeline execution engine (the Compose-native stand-in for KFP/Argo) |
| **scikit-learn** | `scikit-learn==1.7.0` | Demo training; **pinned identically** in trainer, serving, and workbench images to prevent training-serving skew |
| **PyTorch DDP** | Project-template dependency, selected by workload | Multi-process and multi-GPU training in the `distributed-training` starter; `torchrun` owns process launch |
| **mlflow models serve** | via serving-manager | Live model REST endpoints (the Compose-native stand-in for KServe/Knative) |
| **Docker Engine API** | — | The serving-manager launches serving containers over the Docker socket |
| **Dagre** | `@dagrejs/dagre@2.0.0` (MIT, pinned browser asset with integrity metadata) | `pipeline-graph.js` lays out accessible persisted pipeline dependency graphs; execution order remains server-side and a compact renderer remains available if the asset cannot load |

## Agentic AI

| Technology | Pin | Role |
| --- | --- | --- |
| **LangGraph** | `langgraph>=0.2` | Agent graphs (StateGraph, reason → tools → respond) |
| **LangGraph Postgres checkpoint** | `langgraph-checkpoint-postgres>=2.0` | Durable session state (AsyncPostgresSaver) |
| **LangChain** | `langchain-core / -openai / -anthropic >=0.3 / >=0.2` | LLM abstractions and tool conversion |
| **Langfuse** | `langfuse>=2.0` (`langfuse/langfuse:2`) | LLM/agent observability (Postgres-backed v2) |
| **FastAPI + Uvicorn** | `fastapi>=0.111`, `uvicorn>=0.30` | The agent-runtime HTTP service |
| **psycopg** | `psycopg[binary]>=3.1` | Postgres access for memory/checkpoints |

## Edge, auth & serverless (public)

| Technology | Image | Role |
| --- | --- | --- |
| **Caddy** | `caddy:2.8` | Automatic Let's Encrypt TLS + reverse proxy (stands in for Istio ingress) |
| **Dex** | `dexidp/dex:v2.41.1` | OIDC identity provider for console login |
| **OpenFaaS / faasd** | VM-level install | Open-source serverless (stands in for Knative) |
| **code-server** | optional Compose profile | Browser IDE sharing the persistent Jupyter workspace |
| **JupyterLab + Jupyter AI** | custom workbench image | Notebook/terminal primary interface and natural-language coding surface |

## Client & SDK

| Technology | Pin | Role |
| --- | --- | --- |
| **httpx** | `httpx>=0.27` | HTTP client (SDK, workloads) |
| **pydantic** | `pydantic>=2.7` | Typed models in the SDK |
| **pgx** | `github.com/jackc/pgx/v5` | The only third-party Go dependency (Postgres driver) |

## Development & CI

| Technology | Role |
| --- | --- |
| **pytest** (+ asyncio, cov) | Python tests |
| **ruff** | Python lint/format |
| **go test / go vet / gofmt** | Go test, vet, formatting |
| **k6** | Load testing (`tests/load/`) |
| **Kind** | Local Kubernetes for the optional scale-path fidelity tests |
| **GitHub Actions** | CI: `go` job (gofmt, vet, race tests, integration, build, banned-tech scan) and `python` job (ruff, pytest, compileall) |
| **MkDocs Material** | This documentation site |

## Kubernetes scale path (optional)

Maintained but **not required** for local or single-VM use:

| Asset | Location | Role |
| --- | --- | --- |
| **CRDs** | `config/crd/` | `KiongaAgent`, `KiongaPipelineRun`, `KiongaTool`, `KiongaConnection`, `KiongaModelPromotion`, `KiongaWorkspace` |
| **Operator RBAC** | `config/rbac/` | Scoped operator permissions, including owned agent HPAs |
| **NetworkPolicies** | `config/network/`, `config/network-istio/` | Default-deny + platform-allow, and strict mTLS |
| **Security** | `config/security/` | Dex and Vault auth assets |
| **Backup** | `config/backup/` | CloudNativePG backup |
| **Kind cluster** | `deploy/kind/cluster.yaml` | Local Kubernetes cluster definition |

## Technology choices

The platform uses open-source, non-proprietary tools throughout. Where a
capability is commonly associated with a Kubernetes-only tool, Kionga uses a
Docker-friendly equivalent for a laptop or single VM, with separate Kubernetes
integration contracts for the scale path:

| Capability | Kionga uses | Kubernetes-native equivalent |
| --- | --- | --- |
| Model serving | `mlflow models serve` containers | KServe / Knative |
| Pipelines | Prefect | KFP / Argo |
| Serverless | OpenFaaS / faasd | Knative |
| Edge / mesh | Caddy | Istio |

The Kubernetes-native equivalents remain available on the documented scale path.
