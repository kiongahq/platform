# Services overview

The default stack declares 19 regular and one-shot services on one Compose network
(`deploy/compose.yaml`), plus the optional IDE profile and dynamically launched
model-serving containers. This section documents each one.

## Full inventory

| Service | Image / build | Host port | Group | Role |
| --- | --- | --- | --- | --- |
| **gateway** | Go (`SERVICE=gateway`) | 8080 | Control plane | REST API + embedded console |
| **postgres** | `pgvector/pgvector:pg16` | 5432 | Data plane | Control-plane, MLflow, Langfuse, checkpoints, vectors |
| **redis** | `redis:7.4.5-alpine` | 6379 | Data plane | Online feature store |
| **kafka** | `apache/kafka:3.9.1` | 9092 | Data plane | Events, traces, real-time topics |
| **kafka-rest** | `ghcr.io/aiven-open/karapace:6.1.4` | 8082 | Data plane | Confluent-compatible HTTP access to Kafka |
| **minio** | `minio/minio` | 9000 / 9001 | Data plane | S3 object storage + console |
| **minio-init** | `minio/mc` | — | Data plane | One-shot bucket creation |
| **mlflow** | `mlaiops-mlflow` (built) | 15000 | Data plane | Tracking + registry |
| **feature-gateway** | Go (`SERVICE=feature-gateway`) | 8083 | Data plane | Online feature retrieval |
| **storage-proxy** | Go (`SERVICE=storage-proxy`) | 8084 | Data plane | SigV4 S3 URLs + browse |
| **prefect-server** | `prefecthq/prefect:3-latest` | 4200 | Execution | Pipeline engine |
| **pipeline-runner** | Python (`pipelines/Dockerfile`) | — | Execution | Serves + runs platform flows |
| **serving-manager** | Go (`SERVICE=serving-manager`) | 8085 | Execution | Launches model-serving containers |
| **agent-runtime** | Python (`agent_runtime/Dockerfile`) | 19000 | AI | Serves LangGraph agents |
| **trace-proxy** | Go (`SERVICE=trace-proxy`) | 8081 | AI | LLM egress + trace capture |
| **langfuse** | `langfuse/langfuse:2` | 3000 | AI | LLM/agent observability |
| **realtime-processor** | Python (`agent_runtime/Dockerfile`) | — | AI | Kafka stream scoring |
| **feature-materializer** | Python (`agent_runtime/Dockerfile`) | — | Data plane | One-shot feature materialization |
| **jupyter** | `mlaiops-jupyter` (built) | 8888 | Dev | Notebooks + terminal |
| **ide** *(profile)* | code-server image (built) | 13337 | Dev | Browser IDE sharing the Jupyter workspace |

## The four groups

- **[Gateway (control plane)](gateway.md)** — the single service that owns the API,
  serves the console, and orchestrates everything else. Plus the supporting Go
  services (operator, metrics collector) that ship as binaries.
- **[Data & messaging](data-plane.md)** — Postgres, Redis, Kafka, MinIO, MLflow,
  and the two Go data services (feature gateway, storage proxy).
- **[Execution & serving](execution.md)** — Prefect, the pipeline runner, and the
  serving manager.
- **[AI & observability](ai-observability.md)** — the agent runtime, trace proxy,
  Langfuse, and the real-time processor.

## Health checks

Core stateful services have Docker health checks (postgres, redis, kafka, mlflow,
prefect-server). Go services expose `GET /healthz`; the gateway exposes
`GET /api/v1/health`; the agent runtime exposes `GET /healthz`. Check everything at
once:

```bash
docker compose -f deploy/compose.yaml ps
```

## Dependency & startup order

Compose `depends_on` enforces a sane boot order: Postgres/Kafka/MinIO come up first,
then MLflow and the Go/Python services that need them. `minio-init` and
`feature-materializer` are **one-shot** jobs (`restart: "no"`) that complete and
exit. `pipeline-runner`, `realtime-processor`, `serving-manager`, and `agent-runtime`
run continuously (`restart: unless-stopped` where applicable).

OpenFaaS/faasd is not a Compose service. It is an external function engine connected
with `OPENFAAS_URL`. Model-serving containers are created dynamically by the
serving manager and therefore do not appear as static Compose services.
