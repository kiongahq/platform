# Connecting all the services

Everything in the platform is one Compose stack. This guide covers the three
ways things connect:

1. **Service → service** inside the stack (already wired; reference below).
2. **You → services** from the browser, the Jupyter workbench, or any local tool.
3. **Console connections** (Platform tab) — registering services with the
   control plane so health is actively checked and onboarding turns green.

## The map

```
                    you (browser / notebook / CLI)
                                  │
      ┌───────────┬───────────┬───┴────────┬───────────┬───────────┐
   console      MLflow      Prefect     Langfuse    MinIO console  Jupyter / IDE
   :8080*       :15000      :4200       :3000       :9001          :8888 / :13337
      │
   gateway ──► Prefect API ──► pipeline-runner (trains, logs to MLflow)
      │  ╲───► serving-manager ──► mlflow-serve containers (live endpoints)
      │  ╲───► agent-runtime ──► LLM via trace-proxy ──► Kafka traces
      │  ╲───► storage-proxy ──► MinIO        │
      │  ╲───► feature-gateway ──► Redis      └─► Langfuse (traces)
      │  ╲───► Langfuse API (prompts)
   Postgres (control plane, MLflow, Langfuse, agent checkpoints, pgvector)
   Kafka ◄── realtime-processor ──► feature-gateway, models, agents
```

Two DNS worlds:

- **Inside the stack** services reach each other by Compose service name:
  `http://gateway:8080`, `http://mlflow:5000`, `http://minio:9000`, …
- **From your machine** the same services are on published localhost ports
  (table below). Every port is overridable in `.env` (e.g. `GATEWAY_PORT`).

## Service reference

| Service | From your machine | Inside the stack | Credentials (local defaults) |
| --- | --- | --- | --- |
| Landing page (gateway) | http://localhost:8080 | `http://gateway:8080` | none |
| Console + API (gateway) | http://localhost:8080/console.html | `http://gateway:8080` | `admin` / `mlaiops-local`; API local role = `MLAIOPS_LOCAL_ROLE` |
| Jupyter workbench | http://localhost:8888 | `http://jupyter:8888` | token `mlaiops-local` (`JUPYTER_TOKEN`) |
| MLflow tracking/registry | http://localhost:15000 | `http://mlflow:5000` | none |
| Prefect UI/API | http://localhost:4200 | `http://prefect-server:4200` | none |
| Langfuse | http://localhost:3000 | `http://langfuse:3000` | `admin@local.dev` / `mlaiops-local-admin`; API keys `pk-lf-local-dev` / `sk-lf-local-dev` |
| MinIO S3 API | http://localhost:9000 | `http://minio:9000` | `mlaiops` / `mlaiops-local-secret` |
| MinIO web console | http://localhost:9001 | — | same as S3 |
| Kafka broker | localhost:9092 | `kafka:9092` | none |
| Kafka REST proxy | http://localhost:8082 | `http://kafka-rest:8082` | none |
| Postgres (+pgvector) | localhost:5432 | `postgres:5432` | `mlaiops` / `mlaiops-local` / db `mlaiops` |
| Redis (online features) | localhost:6379 | `redis:6379` | none |
| Feature gateway | http://localhost:8083 | `http://feature-gateway:8083` | internal token in public mode |
| Storage proxy | http://localhost:8084 | `http://storage-proxy:8084` | internal token in public mode |
| Serving manager | http://localhost:8085 | `http://serving-manager:8085` | internal token in public mode |
| Trace proxy (LLM egress) | http://localhost:8081 | `http://trace-proxy:8081` | none |
| Agent runtime | http://localhost:19000 | `http://agent-runtime:9000` | none |
| Browser IDE (optional) | http://localhost:13337 | `http://ide:8080` | password `mlaiops-local` (`IDE_PASSWORD`) |

All local defaults are development-only; public deployments override them in
`.env` (see `docs/hosting.md`).

## Registering connections in the console

The Platform tab's **Add connection** registers a service with the control
plane; the gateway then actively health-checks it (nothing is green by
wishful thinking). **The gateway performs the check from inside the stack, so
use in-stack hostnames**, not localhost.

| Service field | Connection name (suggested) | Health endpoint | Secret ref |
| --- | --- | --- | --- |
| MLflow | `local-mlflow` | `http://mlflow:5000/health` | `mlflow-credentials` |
| S3 / MinIO | `local-minio` | `http://minio:9000/minio/health/live` | `minio-credentials` |
| Kafka REST | `local-kafka` | `http://kafka-rest:8082/topics` | `kafka-credentials` |
| Langfuse | `local-langfuse` | `http://langfuse:3000/api/public/health` | `langfuse-credentials` |
| Prefect | `local-prefect` | `http://prefect-server:4200/api/health` | `prefect-credentials` |
| Redis / Feast | `local-features` | `http://feature-gateway:8083/healthz` | `redis-credentials` |
| Kubernetes | `kind-cluster` | your cluster API URL (optional) | `kubeconfig-secret` |

Notes:

- The **secret ref** is a reference name (a Kubernetes Secret name on the
  cluster path); raw credentials are never stored in the control plane.
- Any HTTP(S) response below 500 counts as healthy — a `401` from a
  Kubernetes API server is a healthy signal.
- **Onboarding readiness** (Overview ring / Platform panel) turns green from
  these connections: workspace = any project, plus healthy `mlflow`,
  `s3`/`minio` (storage), `kafka` (events), and `kubernetes` connections.
  Kubernetes is the documented scale path — on the Compose-native stack the
  execution engine is Prefect + the serving manager, so that one item stays
  pending unless you connect a real cluster (e.g. Kind).
- Re-test any connection with its **Test** button (admin/operator role).

## Connecting from the Jupyter workbench

`make local-up` now includes a JupyterLab workbench at
**http://localhost:8888** (token `mlaiops-local`) with:

- **Terminal included**: File → New → Terminal — bash with `git`, `curl`,
  and every platform module (`python -m realtime.produce --demo fraud --count 3`).
- The platform SDK (`mlaiops_sdk`) and modules (`pipelines`, `features`,
  `realtime`, `agents`) preinstalled.
- ML pins identical to the serving image (`scikit-learn==1.7.0`,
  `mlflow==3.1.1`) — models trained in a notebook serve without skew.
- Every connection preconfigured via environment: `MLAIOPS_URL`,
  `MLFLOW_TRACKING_URI`, `MLFLOW_S3_ENDPOINT_URL` + `AWS_*`,
  `MLAIOPS_FEATURE_GATEWAY_URL`, `KAFKA_REST_URL`, `PREFECT_API_URL`,
  `DATABASE_URL`, `REDIS_URL`, `LANGFUSE_*`.
- `quickstart.ipynb` (seeded into the workspace volume) walks the whole
  platform: identity → registry → train + log to MLflow → register with the
  control plane → online features → real-time events.

Your notebooks persist in the `jupyter-data` volume across restarts.

## Connecting from external tools on your machine

- **MLflow**: `MLFLOW_TRACKING_URI=http://localhost:15000`, and for artifacts
  `MLFLOW_S3_ENDPOINT_URL=http://localhost:9000` with
  `AWS_ACCESS_KEY_ID=mlaiops` / `AWS_SECRET_ACCESS_KEY=mlaiops-local-secret`.
- **S3 clients** (aws cli, boto3, rclone): endpoint `http://localhost:9000`,
  same keys, path-style addressing.
- **psql**: `psql postgres://mlaiops:mlaiops-local@localhost:5432/mlaiops`.
- **Kafka**: brokers `localhost:9092`, or REST at `http://localhost:8082`.
- **Platform API**: set `MLAIOPS_URL=http://localhost:8080` (or your overridden
  gateway port), then `curl "$MLAIOPS_URL/api/v1/me"` — see
  `/api/openapi.json` for the surface.

## Wiring the moving parts together

- **Real LLMs for agents**: in `.env` set `MLAIOPS_LLM_BACKEND=openai` (or
  `anthropic`), the API key, and optionally `MLAIOPS_LLM_MODEL`; calls egress
  through the trace-proxy so tokens/cost land in Kafka + the cost dashboard.
- **Real-time demos → live components**: set `FRAUD_MODEL_ENDPOINT` to a live
  model endpoint (Models tab → deploy → endpoint URL) and
  `CALLCENTER_AGENT_ID` to a deployed agent id, then
  `docker compose -f deploy/compose.yaml up -d realtime-processor`.
- **Serverless (OpenFaaS/faasd)**: install faasd on the VM
  (`docs/hosting.md`), then set `OPENFAAS_URL`, `OPENFAAS_USER`,
  `OPENFAAS_PASSWORD` — the Functions view lists, deploys, and invokes project-owned
  functions once configured.

## Swapping in external / production services

Build once, point anywhere — set the URL and credentials, no image changes:

| Replace | With | How |
| --- | --- | --- |
| MinIO | AWS S3 / GCS / Azure (S3 API) | `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` (storage-proxy), `MLFLOW_S3_ENDPOINT_URL` + `AWS_*` (mlflow, runner) |
| Postgres | RDS / Cloud SQL | `DATABASE_URL` on gateway, agent-runtime, langfuse, mlflow backend URI |
| Redis | managed Redis | `REDIS_URL` on feature-gateway |
| Kafka | managed Kafka + REST proxy | `KAFKA_REST_URL` everywhere it appears |
| Local LLM egress | any OpenAI-compatible provider | `LLM_UPSTREAM_URL` on trace-proxy |

## Troubleshooting

- **Connection shows unhealthy**: the URL must resolve *from the gateway
  network*. The gateway is distroless; use a disposable curl container on
  `mlaiops_default` to reproduce the check.
- **Works in console, fails from your laptop**: you used an in-stack hostname
  outside the stack (or vice versa) — swap per the two-DNS-worlds table.
- **Everything down?** `make local-up` is idempotent; `docker compose -f
  deploy/compose.yaml ps` shows per-service state;
  `GATEWAY=http://localhost:8080 ./scripts/demo-smoke.sh` verifies the configured
  capabilities end to end.
