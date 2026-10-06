# Build agents with your framework

Kionga retains the existing agent deployment API and `graph_module` field while
allowing factories to return framework adapters. No framework code runs in the
Go gateway. Each runtime image supplies the code and dependencies it needs.

| Integration | Entry point | State and telemetry |
| --- | --- | --- |
| LangGraph | Compiled graph or `build(model, checkpointer)` | Existing platform model factory, checkpoints and Langfuse callbacks |
| Agno | `AgnoAdapter(factory)` | Passes session/user IDs to `arun`; normalizes response and run token metrics |
| NOOA | `NooaAdapter(factory, "method")` | Calls one typed async method; no invented token counts or automatic state storage |
| Other frameworks | `CallableAdapter(run)` | Plain request/result contract; your handler owns provider calls and persistence |

These are **runtime adapter integrations**, not automatic conversions between
frameworks. The existing `agent-system` project scaffolder still generates
LangGraph; use the checked-in factories below or add your own module to your
project/image for other frameworks. The console offers entrypoint presets in
**Agents → Deploy agent**, but choosing one does not install dependencies.

## The common contract

```python
from mlaiops_sdk.agent_adapters import (
    AgentResult, CallableAdapter, agent_factory,
)

@agent_factory("my-framework")
def build():
    async def run(request):
        # request.message, request.session_id, request.user_id
        # Call your framework here; this echo is a runnable offline example.
        return AgentResult(reply=f"Received: {request.message}")
    return CallableAdapter(run)
```

Set `MLAIOPS_GRAPH_MODULE=my_project.agent:build`. The decorator tells the runtime
**not** to construct a LangChain LLM or a LangGraph checkpointer. Your factory
owns that configuration. Do not select an arbitrary provider in the console and
expect it to configure another framework's client.

Handlers may be asynchronous or synchronous; synchronous work is offloaded to a
thread. Results can contain text, JSON-serializable structures, or Pydantic
objects. Provide `input_tokens` and `output_tokens` only when measured. Missing
usage is represented as `usage_available=false`; compatibility counters remain
zero, not a measurement of free inference. Costs are calculated using configured
per-token rates, not a provider billing ledger.

The adapters intentionally buffer responses. `/stream` announces
`streaming_mode=buffered`, emits the completed reply as one delta, then a summary.
It does not pretend that a completed reply is token streaming. LangGraph retains
its incremental path. Native tool events, vendor trace trees, async generators,
multimodal inputs, and interactive approval pauses are not automatically mapped.

## Agno

Install the optional integration alongside the runtime dependencies:

```sh
pip install './python[runtime,agno]'
```

The ready factory is `agents.framework_examples:build_agno`. Set `AGNO_MODEL`
and `OPENAI_API_KEY` in the runtime environment. This example uses Agno's
OpenAI client; replace its factory for other Agno providers, Teams, or tools.
Each request gets a fresh object and explicit `session_id` / `user_id` arguments.

For the Compose demo, configure the uncommitted root `.env`:

```dotenv
AGENT_RUNTIME_EXTRAS=runtime,agno
MLAIOPS_GRAPH_MODULE=agents.framework_examples:build_agno
AGNO_MODEL=your-provider-model-id
# Supply OPENAI_API_KEY privately; never include it in an image or contract.
```

Rebuild/recreate `agent-runtime`. Register exactly the same factory in the
console's agent record. Compose serves **one configured factory**; it does not
dynamically import the factory from every incoming request. A mismatched
entrypoint is rejected instead of silently running the demo agent.

For durable Agno conversation history, configure its native database in your
factory. The example is deliberately stateless across calls. Kionga's turn
records are observability data, not a replacement for framework memory.

## NVIDIA-labs OO-Agents (NOOA)

NOOA's object-oriented methods fit the same boundary. The example factory is
`agents.framework_examples:build_nooa`; it builds a fresh object and invokes its
`answer(message)` method. For richer typed arguments, map `AgentRequest` to those
arguments with `CallableAdapter` instead.

NOOA currently requires Python 3.12–3.13, so it is not installed into the default
Python 3.11 runtime or Jupyter image. Build a separate image:

```sh
docker build -f python/agent_runtime/Dockerfile \
  --build-arg PYTHON_VERSION=3.12 \
  --build-arg SDK_EXTRAS=runtime,nooa \
  -t your-registry/nooa-agent:dev .
```

Provide `NOOA_MODEL`, the chosen provider's credentials, and
`MLAIOPS_GRAPH_MODULE=agents.framework_examples:build_nooa` through your deployment
configuration. Pin tested dependency versions and image digests for releases.
The runtime Dockerfile supports selectable extras; it is not a dependency lock.

### Isolation is mandatory for generated code

NOOA can execute model-generated Python. Run it in an operator-provisioned,
dedicated container sandbox or VM with restricted egress, resource/time limits,
minimal secrets, and no Docker socket or sensitive host/workspace mounts.
Do not run the example in the gateway process or in a shared notebook with broad
credentials. In-process code checks are not a containment boundary.

Only after provisioning that boundary, set `KIONGA_ALLOW_CODE_EXECUTION=1` to
enable the example factory. **This flag acknowledges configuration; it neither
creates nor proves a sandbox.** The standard Kionga pod security settings alone
are not a claim of safe isolation for arbitrary generated code. NOOA integration
is opt-in and experimental. The adapter does not automatically export NOOA's
native trace tree or persist its live Python objects between turns.

## Deployment and team workflows

Package your module and framework dependencies in your own image. The existing
SDK `deploy_agent(..., graph_module="my_project.agent:build", image=...)`, REST
API, and administrator CPU/memory/GPU grants remain applicable. The Kubernetes
operator creates one workload per agent ID. Provider secrets and custom framework
environment need an administrator-managed deployment/secret-injection mechanism;
the agent form does not accept arbitrary secret values or configure a sandbox.

The default Compose override cannot host multiple different factories at once.
Use dedicated runtimes/Kubernetes for mixed-framework deployments. Tests ensure
the bundled runtime rejects a gateway request for a different configured factory.
Third-party HTTP runtimes must implement equivalent routing checks themselves.

Use separate framework stores or namespaces for teams/tenants. Treat client-supplied
session and user IDs as routing context, **not authorization proof**. The adapter
does not make a framework's memory multi-tenant-safe automatically. Configure your
application's identity mapping and data access at the authenticated boundary.

## Validation and sources

Offline tests cover Agno-shaped results, NOOA-style methods, custom sync/async
handlers, context forwarding, fresh instances, usage normalization, buffered SSE,
startup without LangGraph provider configuration, and mismatched-runtime refusal.
They do not call paid models or verify a production sandbox. Real Agno/NOOA
provider runs and image builds require your configured environment.

- [Agno running agents](https://docs.agno.com/agents/running-agents)
- [Agno run metrics](https://docs.agno.com/reference/run/metrics)
- [NOOA source and safety guidance](https://github.com/NVIDIA-NeMo/labs-OO-Agents)
- [NOOA Python requirements](https://github.com/NVIDIA-NeMo/labs-OO-Agents/blob/main/pyproject.toml)
- [NVIDIA-labs OO Agents paper](https://arxiv.org/abs/2607.20709)
- [Paper HTML](https://arxiv.org/html/2607.20709v1)
