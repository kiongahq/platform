# Agents

Agents run through **LangGraph, Agno, NOOA, or a custom adapter** in the agent
runtime. See [framework integration](agent-frameworks.md) for factories, images,
provider configuration, and limitations. Compose deliberately
uses one shared runtime; Kubernetes reconciles an isolated Deployment and Service
per control-plane agent ID. LangGraph has built-in checkpoint and callback
integration. Other frameworks own their persistence and detailed tracing; the
common runtime reports turn summaries and usage when supplied by the framework.

Shell examples assume `export MLAIOPS_URL=http://localhost:8080`; change it when
the gateway port or host differs.

## Anatomy of an agent

An agent is a deployed record pointing at an **entrypoint** (`module:function`),
stored in the backwards-compatible `graph_module` field,
with an LLM backend, a tool list, per-replica CPU/memory/GPU requests, minimum and
maximum replicas, a canary weight, and status. The default agent is
`agents.customer_support.graph:build` — a `StateGraph` with a reason → tools →
respond loop and two registered tools:

- `feature_store_lookup` — reads online features via the feature gateway (with a
  demo fallback).
- `kb_search` — a knowledge-base lookup.

## Invoking

=== "Console"

    **Agents → Chat** on an agent. Each turn shows the reply, token count, and
    latency; the Session Monitor lists live sessions with turns, tokens, and cost.

=== "SDK"

    ```python
    agents = client.list_agents()
    reply = client.invoke_agent(agents[0].id, message="When are invoices issued?")
    ```

## Deploying from the console

Choose **Agents → Deploy agent** and provide the assigned project, semantic version,
immutable OCI image, graph entry point, LLM backend, registered tools, per-replica
CPU and memory, optional GPU count/type, and minimum/maximum replicas. The same modal
can be populated from a `production-agent` project's generated manifest. Submission
uses `POST /api/v1/agents`; a disabled or unassigned project is not selectable, and
the gateway enforces the boundary again.

After acceptance, open the agent card to inspect deployment metadata, endpoint,
sessions, traces, measured usage/cost, and canary traffic. `GET /api/v1/agents`
performs bounded live `/healthz` probes and returns `status: ready` only for a
reachable runtime; otherwise it returns `pending` and the console disables Chat.
The probe view is deliberately ephemeral—persisted desired state alone is not proof
that a workload is ready.

=== "API"

    ```bash
    curl -s -X POST "$MLAIOPS_URL/api/v1/agents/<id>/invoke" \
      -H 'Content-Type: application/json' \
      -d '{"message":"When are invoices issued?","session_id":"","user_id":"console"}'
    ```

## What happens per turn

1. The gateway resolves the selected runtime and performs a three-second
   `/healthz` gate. An unavailable runtime returns `503 agent_not_ready`; no turn is
   sent.
2. The gateway proxies to the healthy runtime with the agent's identity headers.
3. The runtime loads the **Postgres checkpoint** for the session (state persists
   across turns; sessions are scoped per agent).
4. The graph runs — reasoning, tool calls, feature/memory retrieval.
5. The LLM is called **through the trace-proxy**, which forwards to the provider and
   publishes the call to Kafka.
6. The runtime reports the session (turns, current node, tokens, cost) to the
   gateway; the reply returns.

## Sessions, traces, cost

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/agents/{id}/sessions` | Live sessions (turns, node, tokens, cost) |
| `GET /api/v1/agents/{id}/traces` | Trace records |
| `GET /api/v1/agents/{id}/usage` | Aggregated tokens/cost/active sessions |

Tokens come from LangChain usage metadata — **measured, never estimated**. Cost uses
`MLAIOPS_COST_PER_1K_INPUT/OUTPUT`. The **Prompt Library** panel proxies Langfuse
prompts; deep observability lives in the Langfuse UI (<http://localhost:3000>).

## Using a real LLM

By default agents use the `mock` backend (replies prefixed `[mock]`) so the stack
runs with zero keys. To use a real provider, set in `.env`:

```bash
MLAIOPS_LLM_BACKEND=anthropic          # or openai / openai-compatible
MLAIOPS_LLM_MODEL=claude-sonnet-4-5
ANTHROPIC_API_KEY=sk-ant-...
LLM_UPSTREAM_URL=https://api.anthropic.com
```

Calls still egress through the trace-proxy, so tokens and cost land in Kafka and the
cost dashboard. Keys are env-only and never written to traces, logs, or the store.

## Canary traffic

```bash
curl -s -X PUT "$MLAIOPS_URL/api/v1/agents/<id>/traffic" \
  -d '{"canary_weight":10}'
```

## Writing your own agent

Create a versioned `production-agent` project rather than starting from an empty
module:

```python
project = client.create_project(
    "incident-response-agent",
    template="production-agent",
    framework="langgraph",
    accelerator="cpu",
)
print(project.scaffold_command)
```

Run the returned command in Jupyter or the IDE. The generated project is functional
before customization: it includes a LangGraph tool loop, safe example tool,
`AgentMemoryClient` integration boundary, PostgreSQL/in-memory checkpointer
selection, Langfuse callback, deterministic graph test and golden evaluation runner,
an HTTP health/invocation runtime, container packaging, and a deployable platform
manifest. Use `--agent codex`,
`--agent claude`, or the configured custom command to extend only that generated
directory.

The graph entry point must expose a `build(model, checkpointer)` compatible callable
that returns a compiled graph, `StateGraph`, or factory. Register tools with
`mlaiops_sdk.register_tool` and convert them with `langchain_tools([...])`.

Build and push an immutable image, then deploy it:

```bash
curl -s -X POST "$MLAIOPS_URL/api/v1/agents" \
  -H 'Content-Type: application/json' \
  -d '{
    "project_id":"<project-id>",
    "name":"incident-response",
    "version":"1.0.0",
    "image":"ghcr.io/acme/incident-response@sha256:<digest>",
    "graph_module":"incident_response_agent.graph:build",
    "llm_backend":"openai-compatible",
    "autoscaling":{"min_replicas":2,"max_replicas":6},
    "resources":{"cpu":"1","memory":"2Gi","gpu":0},
    "tools":["runbook_search","create_escalation"]
  }'
```

The gateway enforces project assignment and agent service access before persisting
desired state. CPU and memory use Kubernetes quantity syntax. When GPU is positive,
`gpu_type` defaults to `nvidia.com/gpu`; set it explicitly for another extended
resource. Omitted resources default to `500m` CPU and `1Gi` memory. The legacy
top-level `replicas` field remains an input compatibility alias, but `autoscaling`
is the canonical response and reconciliation contract.

For a normal user, the gateway reserves every owned agent at `max_replicas` and
adds the trace-sidecar CPU and memory request for each possible pod. The aggregate
CPU, memory, GPU, and maximum replica count must fit the user's compute/workload
grant. GPU resource types must be domain-qualified Kubernetes extended resources
(for example `nvidia.com/gpu`), must not use Kubernetes-reserved domains, must not
collide with native keys such as `cpu` or `memory`, and must match the grant. The
lifecycle dispatcher repeats this check before constructing a Kubernetes resource
map. Admin/operator principals explicitly bypass capacity admission, not schema
validation. Invalid quantities or a maximum below the minimum (or above 100) are
rejected before desired state is persisted.

On Kubernetes, the dispatcher derives one collision-safe DNS name from the immutable
agent ID (a readable stem plus digest). The operator uses that same name for the
`KiongaAgent` workload and Service, and the gateway routes to
`http://<derived-name>.<MLAIOPS_AGENT_NAMESPACE>.svc`. Human names can therefore be
reused without overwriting another agent. Readiness becomes true only when the
Deployment has at least `min_replicas` available, and `status.workloadRef` identifies
the generated workload.

If `max_replicas` exceeds `min_replicas`, the operator owns a
HorizontalPodAutoscaler targeting 70% average CPU utilization and does not reset the
HPA-owned replica field during reconciliation. Equal values keep a fixed replica
count and remove a stale autoscaler. A working Kubernetes resource-metrics API is
required; the Kind bootstrap installs metrics-server for this reason. In Compose,
`AGENT_RUNTIME_URL` intentionally overrides per-agent DNS and points every agent at
the shared runtime; Kubernetes HPA behavior does not apply.

See [Project templates](project-templates.md#create-a-functional-agent-project) for
the full source-control and scaffolding workflow.

## Production acceptance gates

Before shifting traffic to a new agent version:

1. run deterministic graph/tool tests with the mock model;
2. run golden evaluation cases and failure-path tests;
3. verify tool schemas, timeouts, permissions, and idempotency;
4. verify checkpoint and semantic-memory isolation by agent and tenant;
5. inspect Langfuse traces for secrets and unexpected tool payloads;
6. deploy by immutable image digest with a small canary weight;
7. compare measured quality, latency, tokens, cost, and error rate;
8. promote or return traffic to the stable version.

## Long-term memory (pgvector)

`AgentMemoryClient` gives agents `remember`/`recall` over a pgvector table, plus
`get_entity_features`. It uses a deterministic `HashingEmbedder`, so memory tests run
offline. Checkpoints (session state) and memories (semantic recall) are distinct: one
is per-thread conversation state, the other is durable semantic memory.
