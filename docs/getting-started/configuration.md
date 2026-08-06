# Configuration reference

Kionga is configured entirely through environment variables. Locally, sane defaults
mean the stack runs with **zero configuration**. For public hosting you supply a
`.env` file (copy `.env.example`). This page is the complete reference.

!!! danger "Secrets"
    Never commit `.env` (it is gitignored). Never put provider API keys or the
    internal token in the compose files. Local credentials shown here are
    development defaults — override every one for public.

## Published ports

Every port is overridable via the `*_PORT` variable. Defaults:

| Variable | Default | Service |
| --- | --- | --- |
| `GATEWAY_PORT` | `8080` | Console + API |
| `JUPYTER_PORT` | `8888` | Jupyter workbench |
| `MLFLOW_PORT` | `15000` | MLflow (container listens on 5000) |
| `PREFECT_PORT` | `4200` | Prefect |
| `LANGFUSE_PORT` | `3000` | Langfuse |
| `MINIO_PORT` / `MINIO_CONSOLE_PORT` | `9000` / `9001` | MinIO S3 / console |
| `POSTGRES_PORT` | `5432` | PostgreSQL |
| `REDIS_PORT` | `6379` | Redis |
| `KAFKA_PORT` | `9092` | Kafka broker |
| `KAFKA_REST_PORT` | `8082` | Kafka REST proxy |
| `FEATURE_GATEWAY_PORT` | `8083` | Feature gateway |
| `STORAGE_PROXY_PORT` | `8084` | Storage proxy |
| `SERVING_MANAGER_PORT` | `8085` | Serving manager |
| `TRACE_PROXY_PORT` | `8081` | Trace proxy |
| `AGENT_RUNTIME_PORT` | `19000` | Agent runtime (container listens on 9000; MinIO owns host 9000) |

## Gateway (control plane)

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Listen port |
| `DATABASE_URL` | *(set in compose)* | Postgres DSN. When unset, the gateway uses a **local JSON file** instead |
| `MLAIOPS_DATA_PATH` | `data/platform.json` | File-store path (file mode only) |
| `MLAIOPS_TENANT` | `local` | Tenant scoping |
| `KAFKA_REST_URL` | `http://kafka-rest:8082` | Outbox delivery target |
| `AGENT_RUNTIME_URL` | `http://agent-runtime:9000` in Compose; unset on Kubernetes | Static shared-runtime override. When non-empty it takes precedence over per-agent Kubernetes routing |
| `MLAIOPS_AGENT_NAMESPACE` | `default` | Kubernetes namespace used to derive each agent Service endpoint when `AGENT_RUNTIME_URL` is unset |
| `STORAGE_PROXY_URL` | `http://storage-proxy:8084` | Storage browse proxy target |
| `PREFECT_API_URL` | `http://prefect-server:4200/api` | Pipeline engine |
| `SERVING_MANAGER_URL` | `http://serving-manager:8085` | Model serving control |
| `LANGFUSE_URL` / `LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY` | local defaults | Prompt library proxy |
| `MLAIOPS_LOCAL_ROLE` | `admin` | RBAC role for local API requests when OIDC is disabled; the browser console still requires login |
| `MLAIOPS_ALLOWED_ORIGIN` | `*` | CORS origin (pinned to your domain in public) |
| `MLAIOPS_INTERNAL_TOKEN` | *(unset locally)* | Shared bearer token granting the `service` role |

## RBAC

| Variable | Values | Purpose |
| --- | --- | --- |
| `MLAIOPS_LOCAL_ROLE` | `admin` \| `operator` \| `user` \| `engineer` \| `viewer` | Role applied to requests when OIDC is off (`user` also requires a provisioned `local-dev` profile) |
| `MLAIOPS_LOCAL_USERNAME` / `MLAIOPS_LOCAL_PASSWORD` | `admin` / `mlaiops-local` | Local console login; change outside throwaway development |
| `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | provider client | Browser authorization-code flow |
| `OIDC_AUTH_URL` / `OIDC_TOKEN_URL` / `OIDC_REDIRECT_URL` | provider URLs | Browser login endpoints and callback |
| `MLAIOPS_INTERNAL_TOKEN` | random secret | Presenting it as a bearer token grants the `service` role (reporting endpoints only) |
| `OIDC_ISSUER` | URL | Enables OIDC auth; when set, roles come from token claims |
| `OIDC_JWKS_URL` | URL | Required with `OIDC_ISSUER`; JWKS for signature verification |
| `OIDC_AUDIENCE` | string | Expected token audience |

See [RBAC & security](../reference/rbac.md) for the full model.

Personal API keys for local CLI/SDK use are managed in the console under
**Settings**. They are separate from the internal service token and OIDC
browser session.

## LLM providers

Used by the agent runtime and trace proxy.

| Variable | Default | Purpose |
| --- | --- | --- |
| `MLAIOPS_LLM_BACKEND` | `mock` | `openai` \| `anthropic` \| `openai-compatible` \| `mock` |
| `MLAIOPS_LLM_MODEL` | *(empty)* | Model name for the chosen backend |
| `MLAIOPS_LLM_BASE_URL` | `http://trace-proxy:8081/v1` | Where agent LLM calls egress (the trace proxy, for capture) |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | *(empty)* | Provider keys (env-only; never logged or traced) |
| `LLM_UPSTREAM_URL` | `https://api.openai.com` | Where the trace proxy forwards calls |
| `MLAIOPS_COST_PER_1K_INPUT` / `_OUTPUT` | `0` | USD per 1k tokens for cost accounting |

!!! example "Enable real Anthropic-backed agents"
    ```bash
    # .env
    MLAIOPS_LLM_BACKEND=anthropic
    MLAIOPS_LLM_MODEL=claude-sonnet-4-5
    ANTHROPIC_API_KEY=sk-ant-...
    LLM_UPSTREAM_URL=https://api.anthropic.com
    ```

## Agent runtime

| Variable | Default | Purpose |
| --- | --- | --- |
| `MLAIOPS_GRAPH_MODULE` | `agents.customer_support.graph:build` | The graph to serve (`module:function`) |
| `MLAIOPS_AGENT_NAME` / `MLAIOPS_AGENT_ID` | `customer-support` / *(empty)* | Default identity (overridden per request by the gateway) |
| `MLAIOPS_URL` | `http://gateway:8080` | Where the runtime reports sessions |
| `DATABASE_URL` / `MLAIOPS_CHECKPOINT_DSN` | Postgres DSN | Session checkpoint storage |
| `MLAIOPS_FEATURE_GATEWAY_URL` | `http://feature-gateway:8083` | Feature retrieval for tools/memory |
| `LANGFUSE_HOST` / `LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY` | local defaults | Tracing |
| `MLAIOPS_RUNTIME_HOST` / `MLAIOPS_RUNTIME_PORT` | `0.0.0.0` / `9000` | Bind address |

## Serving manager

| Variable | Default | Purpose |
| --- | --- | --- |
| `SERVE_IMAGE` | `mlaiops-mlflow` | Fallback for models registered without a framework-specific `serving_image` |
| `SERVE_PIDS_LIMIT` | `256` | Serving-container process/thread limit (manager caps it at `4096`) |
| `PLATFORM_NETWORK` | `mlaiops_default` | Docker network to attach serving containers to |
| `DOCKER_API_VERSION` | *(unset = negotiate)* | Pin the Docker Engine API version if needed |
| `MLFLOW_TRACKING_URI` / `MLFLOW_S3_ENDPOINT_URL` / `AWS_*` | compose defaults | Model source |

## Storage proxy

| Variable | Default | Purpose |
| --- | --- | --- |
| `S3_ENDPOINT` | `http://minio:9000` | Object store endpoint |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | `mlaiops` / `mlaiops-local-secret` | Credentials (sole holder) |

## Feature gateway

| Variable | Default | Purpose |
| --- | --- | --- |
| `REDIS_URL` | `redis://redis:6379` | Online store |

## Trace proxy

| Variable | Default | Purpose |
| --- | --- | --- |
| `LLM_UPSTREAM_URL` | `https://api.openai.com` | Forward target |
| `TRACE_SINK_FORMAT` | `kafka-rest` | Trace sink format |
| `TRACE_SINK_URL` | `http://kafka-rest:8082/topics/mlaiops.llm.traces` | Where traces are published |
| `TRACE_SINK_TOKEN` | *(empty)* | Optional sink auth |

## Real-time demos

| Variable | Default | Purpose |
| --- | --- | --- |
| `KAFKA_REST_URL` | `http://kafka-rest:8082` | Kafka access |
| `MLAIOPS_FEATURE_GATEWAY_URL` | `http://feature-gateway:8083` | Online feature enrichment |
| `FRAUD_MODEL_ENDPOINT` | *(empty)* | Live fraud model; falls back to a built-in rule when unset |
| `CALLCENTER_AGENT_ID` | *(empty)* | Deployed agent for transcript analysis; falls back to keyword sentiment |

## Serverless (OpenFaaS)

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENFAAS_URL` | *(unset)* | faasd/OpenFaaS gateway; unset means serverless is "not configured" |
| `OPENFAAS_USER` / `OPENFAAS_PASSWORD` | *(unset)* | Basic auth |

## Public hosting (compose.public.yaml)

| Variable | Purpose |
| --- | --- |
| `MLAIOPS_DOMAIN` | DNS name; Caddy provisions TLS for it |
| `MLAIOPS_ACME_EMAIL` | Let's Encrypt account email |
| `DEX_ADMIN_EMAIL` / `DEX_ADMIN_PASSWORD_HASH` | Console login (bcrypt hash) |
| `DEX_CLIENT_SECRET` | Console OAuth client secret |
| `MLAIOPS_INTERNAL_TOKEN` | Protects internal service APIs |
| `LANGFUSE_NEXTAUTH_SECRET` / `LANGFUSE_SALT` | Langfuse hardening |

## Jupyter workbench

| Variable | Default | Purpose |
| --- | --- | --- |
| `JUPYTER_TOKEN` | `mlaiops-local` | Login token |
| `S3_ENDPOINT` | `http://minio:9000` | S3-compatible endpoint mounted through S3FS |
| `S3_MOUNT_ROOT` | `/workspace/object-store` | Filesystem root for bucket mounts |
| `S3_MOUNT_BUCKETS` | platform bucket list | Space-separated buckets to mount |
| `KIONGA_WORKSPACE` | `/workspace` | Root in which the project generator and Git sync command may write |
| `KIONGA_CUSTOM_AGENT_COMMAND` | *(unset)* | Optional non-interactive coding-agent command used by the scaffolder |

## Browser IDE (optional profile)

| Variable | Default | Purpose |
| --- | --- | --- |
| `IDE_PORT` | `13337` | code-server host port |
| `IDE_PASSWORD` | `mlaiops-local` | Local code-server password |
| `KIONGA_CUSTOM_AGENT_COMMAND` | *(unset)* | Optional non-interactive coding-agent command used by the scaffolder |

`KIONGA_SUBJECT` is injected into reconciled Kubernetes workspaces for identity
context; users should not override it. The platform API remains configured by
`MLAIOPS_URL` and `MLAIOPS_TOKEN`.

## Local file mode & tests

| Variable | Purpose |
| --- | --- |
| `TEST_DATABASE_URL` | Postgres DSN for the Go integration lane |
| `TEST_MEMORY_DSN` | pgvector DSN for the memory round-trip test |
| `MLAIOPS_RUN_EVALS` | Set to `1` to run the paid agent eval lane (needs a real provider key) |

## Kubernetes scale path (optional)

Read by the operator and integration worker only:

| Variable | Default | Purpose |
| --- | --- | --- |
| `KFP_URL` / `KFP_TOKEN` / `KFP_EXPERIMENT_ID` | installation-specific | Kubernetes pipeline execution |
| `MLFLOW_URL` | in-cluster MLflow | Model lifecycle integration |
| `WORKBENCH_IMAGE` | `ghcr.io/ml-ai-ops/jupyter:latest` | Image for provisioned Jupyter containers |
| `IDE_IMAGE` | `ghcr.io/ml-ai-ops/ide:latest` | Image for provisioned IDE containers |
| `WORKSPACE_STORAGE_CLASS` | cluster default | Storage class for per-user PVCs |
| `WORKSPACE_FEATURE_URL` / `WORKSPACE_STORAGE_URL` | in-cluster platform services | Feature and object access injected into each workspace |
| `WORKSPACE_MLFLOW_URL` / `WORKSPACE_PREFECT_URL` | in-cluster upstreams | Experiment and orchestration endpoints injected into each workspace |
| `WORKSPACE_LANGFUSE_URL` / `WORKSPACE_KAFKA_REST_URL` | in-cluster upstreams | Observability and event endpoints injected into each workspace |
| `MLAIOPS_TARGET_NAMESPACE` | `default` | Namespace where lifecycle CRDs and workspaces are created |
| `MLAIOPS_AGENT_NAMESPACE` | `default` | Namespace the gateway uses for ID-derived agent Service DNS; keep equal to the agent lifecycle target namespace |
| `TRACE_PROXY_IMAGE` | `ghcr.io/mlaiops/trace-proxy:latest` | Sidecar image reconciled into every Kubernetes agent pod |

These configure the Kubernetes fidelity path and are not needed for local or
single-VM use. If `AGENT_RUNTIME_URL` remains set in a Kubernetes gateway it
deliberately disables per-agent Service selection, so reserve that override for the
shared Compose topology. Elastic agents additionally require a working Kubernetes
resource-metrics API (normally metrics-server). A `KiongaWorkspace` receives a
generated authentication secret; put its internal service behind the organization's
authenticated ingress before exposing it outside the cluster.
