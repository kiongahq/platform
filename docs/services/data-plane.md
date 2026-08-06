# Data & messaging services

The stores that hold durable state and carry events, plus the two Go services that
front object storage and the online feature store.

## postgres

PostgreSQL 16 with the **pgvector** extension. One database backs many concerns.

| | |
| --- | --- |
| **Image** | `pgvector/pgvector:pg16` |
| **Host port** | `5432` (`POSTGRES_PORT`) |
| **Credentials** | `mlaiops` / `mlaiops-local`, db `mlaiops` |
| **Volume** | `postgres-data` |
| **Init** | `deploy/postgres/init.sql` (extensions, extra databases) |

Holds: control-plane state + the Kafka **outbox**, the MLflow backend store, the
Langfuse backend, LangGraph agent **checkpoints**, and the `agent_memories`
pgvector table for long-term memory / RAG.

## redis

The **online feature store** — low-latency reads for real-time scoring.

| | |
| --- | --- |
| **Image** | `redis:7.4.5-alpine` (append-only persistence) |
| **Host port** | `6379` (`REDIS_PORT`) |
| **Volume** | `redis-data` |
| **Accessed via** | the feature gateway (`REDIS_URL`) |

## kafka & kafka-rest

Apache Kafka in KRaft mode (no ZooKeeper), fronted by Karapace's
Confluent-v2-compatible REST API so Python services stay dependency-free (HTTP
only). Karapace is Apache-licensed, multi-architecture, and materially smaller than
the JVM proxy image it replaces.

| | |
| --- | --- |
| **Images** | `apache/kafka:3.9.1`, `ghcr.io/aiven-open/karapace:6.1.4` |
| **Host ports** | `9092` (broker), `8082` (REST) |

Topics (created by `scripts/local-topics.sh`): audit/lifecycle command topics,
`mlaiops.llm.traces`, `mlaiops.feature.updates`, and the real-time demo topics
(`mlaiops.transactions` → `mlaiops.fraud.alerts`, `mlaiops.callcenter.transcripts` →
`mlaiops.callcenter.insights`, `mlaiops.user.activity` → `mlaiops.recs.results`).

The Kubernetes lifecycle worker uses an explicit Kafka REST consumer instance with
`auto.commit.enable=false`. It dispatches records in poll order and commits the
highest processed offset plus one for each topic/partition only after the entire
batch succeeds. A failed record is retried up to five times with exponential backoff
from 250 ms, capped at two seconds. Commit requests have the same bounded retry but
do not redispatch the successful batch. If either phase still fails, the worker exits
before another poll; Kubernetes restarts it at the last committed offsets. This
at-least-once boundary can redeliver an already reconciled command, so lifecycle
dispatch is an ID-keyed create-or-update operation rather than a non-idempotent
create.

## minio & minio-init

S3-compatible object storage. `minio-init` is a one-shot job that creates the
buckets and exits.

| | |
| --- | --- |
| **Image** | `minio/minio:RELEASE.2025-07-23T15-54-02Z` |
| **Host ports** | `9000` (S3 API), `9001` (web console) |
| **Credentials** | `mlaiops` / `mlaiops-local-secret` |
| **Volume** | `minio-data` |
| **Buckets** | `mlaiops-models`, `-artifacts`, `-features`, `-traces`, `-agents`, `-pipeline-logs` |

The shared Compose workbench mounts these buckets through S3FS at
`/workspace/object-store/<bucket>`. The mount is a notebook convenience, not a POSIX
replacement: renames and directory listings map to object-store operations. Only
Jupyter receives FUSE/SYS_ADMIN; application services continue through S3 APIs or
the bounded storage proxy.

## mlflow

Experiment tracking and the model registry. Custom image (`mlaiops-mlflow`) pinning
`mlflow==3.1.1`, `scikit-learn==1.7.0`, and `uvicorn` so the tracking server, the
trainer, and the serving containers agree — preventing training-serving skew.

| | |
| --- | --- |
| **Image** | `mlaiops-mlflow` (built from `deploy/mlflow/Dockerfile`) |
| **Host port** | `15000` (container listens on 5000) |
| **Backend** | PostgreSQL |
| **Artifacts** | `s3://mlaiops-models` on MinIO |

## feature-gateway

Go service exposing an online feature-retrieval API with a Feast-compatible request
shape, backed by Redis.

| | |
| --- | --- |
| **Source** | `go/cmd/feature-gateway`, `go/internal/feature` |
| **Host port** | `8083` (`FEATURE_GATEWAY_PORT`) |
| **Health** | `GET /healthz` |
| **Config** | `REDIS_URL` |

Endpoints: `POST /get-online-features` (lookup) and
`PUT /internal/v1/features/{service}/{entity}` (write from the materializer).

## storage-proxy

Go service that is the **sole holder of object-store credentials**. It generates
short-lived AWS SigV4 URLs and provides bounded browse/preview for the console.

| | |
| --- | --- |
| **Source** | `go/cmd/storage-proxy`, `go/internal/storage` |
| **Host port** | `8084` (`STORAGE_PROXY_PORT`) |
| **Health** | `GET /healthz` |
| **Config** | `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` |

Endpoints: `GET /buckets`, `GET /objects`, `GET /object` (bounded preview),
`POST /presign`. The gateway proxies these under `/api/v1/storage/*`.

## feature-materializer

One-shot Python job: applies the feature definitions in `python/features/` and
populates the online store, reporting entity counts back to the control plane, then
exits (`restart: "no"`). Re-run it with:

```bash
docker compose -f deploy/compose.yaml up feature-materializer
```
