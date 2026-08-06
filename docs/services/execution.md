# Execution & serving services

How pipelines actually run and how models actually serve — the Compose-native
equivalents of KFP/Argo and KServe/Knative.

## prefect-server

The pipeline execution engine. Pipelines submitted through the console/API become
real Prefect flow runs.

| | |
| --- | --- |
| **Image** | `prefecthq/prefect:3-latest` |
| **Host port** | `4200` (`PREFECT_PORT`) — UI + API |
| **Health** | `/api/health` |
| **Volume** | `prefect-data` |

The gateway talks to Prefect at `PREFECT_API_URL` (`http://prefect-server:4200/api`).
When you submit a pipeline, the gateway creates a flow run carrying the platform
run id and project id so the flow reports its steps back.

## pipeline-runner

Python service that **serves platform flows as Prefect deployments and executes
them**. This is where real training happens.

| | |
| --- | --- |
| **Build** | `python/pipelines/Dockerfile` (Python 3.10) |
| **Entrypoint** | `python -m pipelines.serve` |
| **Pins** | `mlflow==3.1.1`, `scikit-learn==1.7.0` — identical to the serving image |
| **Config** | `PREFECT_API_URL`, `MLAIOPS_URL`, `MLFLOW_TRACKING_URI`, `MLFLOW_S3_ENDPOINT_URL`, `AWS_*` |

The bundled `training_pipeline` flow runs `validate → train → evaluate → register`:
it trains a deterministic scikit-learn classifier, logs the run and model to MLflow,
and registers the model version with the control plane against the submitting
project. Each step reports its transition to
`POST /api/v1/pipelines/runs/{id}/steps`, which drives the live DAG.

The gateway owns graph validation and persisted step state. The console's pinned
open-source Dagre library only computes node coordinates; it cannot reorder or
schedule jobs.

!!! warning "Version pins matter"
    The runner's ML library versions are pinned **identically** to the MLflow
    serving image. Unpinned versions are how training-serving skew happens — a model
    trained under one scikit-learn version fails to load under another. If you change
    a pin, change it in `python/pipelines/Dockerfile` **and** `deploy/mlflow/Dockerfile`.

### Heavy training workloads

The bundled flow is deliberately small enough for local verification. Substantial
deep-learning projects start from the `distributed-training` template, which
provides a PyTorch DDP/`torchrun` boundary, mixed precision, rank-aware MLflow
logging, and resumable checkpoint storage. CPU or one-device smoke tests can run in
Compose; multi-node GPU execution is a Kubernetes scale-path workload and remains
subject to the user's GPU profile, job requests, node topology, and scheduler
capacity.

Kionga persists the project/template/version, pipeline definition, resources, Git
commit, step state, and artifact lineage regardless of execution adapter. It does
not claim local distributed capacity that the host does not have.

## serving-manager

Go service that turns a "deploy" into a **real live endpoint**. It launches an
`mlflow models serve` container per deployed model version over the Docker Engine
API and records the endpoint URL.

| | |
| --- | --- |
| **Source** | `go/cmd/serving-manager`, `go/internal/serving` |
| **Host port** | `8085` (`SERVING_MANAGER_PORT`) |
| **Health** | `GET /healthz` |
| **Special** | Runs as `user: "0:0"` and mounts `/var/run/docker.sock`; this is a privileged local control boundary (the pipeline-runner is the other socket consumer) |

Endpoints: `POST /deployments` (start a serving container), `GET /deployments`
(list), `DELETE /deployments/{name}` (undeploy). Serving containers attach to the
platform network (`PLATFORM_NETWORK`) so the gateway can reach them by name and
proxy predictions to `/invocations`.

| Config | Purpose |
| --- | --- |
| `SERVE_IMAGE` | Fallback image when a model has no `serving_image` (`mlaiops-mlflow`) |
| `SERVE_PIDS_LIMIT` | Per-container process/thread limit (`256`, capped at `4096`) |
| `PLATFORM_NETWORK` | Network to attach containers to (`mlaiops_default`) |
| `DOCKER_API_VERSION` | Pin the Docker API version if the daemon rejects the negotiated one |

### The deploy → predict flow

1. Gateway `POST /api/v1/models/{id}/deploy` → calls the serving manager.
2. Serving manager replaces any existing container, creates and starts a new one
   using the model's `serving_image` or the configured fallback, labels it with the
   model/artifact/image, and returns the endpoint URL. Containers drop all Linux
   capabilities, enable `no-new-privileges`, and receive a bounded PID limit.
3. Gateway records the endpoint and marks the model `serving` (fails closed to a
   `failed` status if the manager rejects it).
4. Gateway `POST /api/v1/models/{id}/predict` proxies to `endpoint/invocations`.

## serverless (OpenFaaS / faasd)

Not a Compose service — a VM-level install. When `OPENFAAS_URL` is set, the gateway
exposes project-owned function list/deploy/invoke/delete under `/api/v1/functions*`.
The Functions view configures CPU/memory and HTTP, async, Cron, or Kafka trigger
metadata; reusable function DAGs execute ready jobs in parallel with retries. See
[Public hosting](../hosting.md) for faasd installation. Until configured, the API
returns an honest `configured: false` surface and smoke tests skip serverless.
