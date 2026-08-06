# AI & observability services

The agent execution runtime, the LLM egress that captures every call, the
observability UI, and the real-time stream processor.

## agent-runtime

Serves platform-registered LangGraph agents over HTTP. Compose uses one shared
runtime and the gateway passes identity per request. Kubernetes instead runs the
agent's immutable image in a dedicated Deployment behind a collision-safe Service
whose name is derived from the immutable control-plane agent ID.

| | |
| --- | --- |
| **Build** | `python/agent_runtime/Dockerfile` (Python 3.11, FastAPI + Uvicorn) |
| **Host port** | `19000` (container listens on 9000; MinIO owns host 9000) |
| **Health** | `GET /healthz` |
| **Source** | `python/agent_runtime/` |

### What it does per turn

1. Loads the agent's graph (`MLAIOPS_GRAPH_MODULE`, or the per-request identity).
2. Loads the **Postgres checkpoint** for the session (state persists across turns,
   keyed per agent).
3. Runs the LangGraph graph — reasoning, tool calls, feature/memory retrieval.
4. Calls the LLM **through the trace-proxy** (`MLAIOPS_LLM_BASE_URL`).
5. Reports the session (turns, current node, tokens, cost) back to the gateway via
   `POST /api/v1/traces`.

Endpoints: `POST /invoke` (one turn) and `POST /stream` (SSE streaming). Token
counts come from LangChain usage metadata — **measured, never estimated**. Cost is
computed from `MLAIOPS_COST_PER_1K_INPUT/OUTPUT`.

New agent applications should start from the versioned `production-agent` project
template. Its generated source is runnable before customization and separates the
graph/tool loop, memory client boundary, checkpointer selection, Langfuse callback,
deterministic graph tests/golden evaluation runner, HTTP runtime, and container
packaging. The project is then deployed by immutable image and
`module:function` graph entry point; provider secrets are injected at runtime.
The deploy contract also carries per-replica CPU/memory/GPU requirements and
minimum/maximum replicas. For normal users, the gateway aggregates every owned
agent at maximum replicas, including `100m` CPU and `128Mi` memory per trace
sidecar, before admitting desired state. GPU types must be valid domain-qualified,
non-reserved Kubernetes extended resources and match typed grants.

The operator creates an owned 70%-CPU HorizontalPodAutoscaler only when the maximum
exceeds the minimum and leaves the replica field to that HPA after creation. A
working resource-metrics API is therefore required (the Kind bootstrap installs
metrics-server). Readiness reflects available pods and both runtime containers have
health probes. The gateway selects the ID-derived Service in
`MLAIOPS_AGENT_NAMESPACE`, enriches list responses with bounded live readiness, and
returns `503 agent_not_ready` before invocation if `/healthz` fails. Compose sets
`AGENT_RUNTIME_URL` to retain the shared runtime and does not emulate Kubernetes
autoscaling.

## trace-proxy

The **LLM egress**. All agent LLM calls point here; it forwards to the configured
provider and publishes every call to Kafka. This gives complete, centralized capture
without touching agent code — and provider keys never leak into traces or logs.

| | |
| --- | --- |
| **Source** | `go/cmd/trace-proxy`, `go/internal/traceproxy` |
| **Host port** | `8081` (`TRACE_PROXY_PORT`) |
| **Health** | `GET /healthz` |

| Config | Purpose |
| --- | --- |
| `LLM_UPSTREAM_URL` | Provider to forward to (`https://api.openai.com` / `.../anthropic`) |
| `TRACE_SINK_FORMAT` | `kafka-rest` |
| `TRACE_SINK_URL` | `http://kafka-rest:8082/topics/mlaiops.llm.traces` |

It presents an **OpenAI-compatible** surface, so any OpenAI-style client works by
setting its base URL to the proxy.

## langfuse

LLM/agent **observability UI**. Pinned to v2, which self-hosts on the platform
Postgres (v3 needs ClickHouse + Redis + workers — too heavy for one VM).

| | |
| --- | --- |
| **Image** | `langfuse/langfuse:2` |
| **Host port** | `3000` (`LANGFUSE_PORT`) |
| **Backend** | PostgreSQL (`langfuse` database) |
| **Local login** | `admin@local.dev` / `mlaiops-local-admin` |
| **API keys** | `pk-lf-local-dev` / `sk-lf-local-dev` (headless init) |

The gateway proxies Langfuse prompt management under `GET /api/v1/prompts` for the
console's Prompt Library, reporting `configured: false` honestly when keys are unset.

## realtime-processor

Kafka consumer service demonstrating three real-time patterns. It reads input
topics, enriches events with **online features**, scores them with a model or agent,
and publishes results.

| | |
| --- | --- |
| **Build** | `python/agent_runtime/Dockerfile` |
| **Entrypoint** | `python -m realtime.service` |
| **Source** | `python/realtime/` |

| Demo | Input topic | Output topic | Logic |
| --- | --- | --- | --- |
| **Fraud detection** | `mlaiops.transactions` | `mlaiops.fraud.alerts` | feature enrichment → live fraud model (`FRAUD_MODEL_ENDPOINT`) or built-in rule → alert |
| **Call-center analysis** | `mlaiops.callcenter.transcripts` | `mlaiops.callcenter.insights` | LangGraph agent (`CALLCENTER_AGENT_ID`) or keyword sentiment → intent/summary |
| **Recommendations** | `mlaiops.user.activity` | `mlaiops.recs.results` | profile features → plan-ranked catalog |

It reports live stats (throughput, latency, flagged counts) to
`POST /api/v1/realtime/{demo}`, surfaced on the console's Real-Time panel. The poll
loop is resilient to transient Kafka REST timeouts — it recreates its consumer and
continues rather than crash-looping.

Produce demo events:

```bash
docker compose -f deploy/compose.yaml exec jupyter \
  python -m realtime.produce --demo fraud --count 5
```

## jupyter

The development workbench — see the dedicated [workbench guide](../guides/workbench.md).
JupyterLab (with a browser terminal) preloaded with the SDK and every connection,
at <http://localhost:8888> (token `mlaiops-local`).

Configured MinIO buckets are mounted with S3FS under
`/workspace/object-store/<bucket>`. The optional code-server IDE (`make ide-up`,
<http://localhost:13337>) mounts the same `jupyter-data` volume but does not receive
FUSE privileges. Kubernetes replaces the shared development profile with isolated
per-user `KiongaWorkspace` workloads.
