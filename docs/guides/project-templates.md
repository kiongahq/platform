# Project templates

Kionga projects start from a **versioned platform contract**, not an untyped label.
The selected template records the framework, accelerator class, requested resource
profile, capabilities, and exact generator command on the project. That metadata
stays attached to pipeline runs, model versions, agent deployments, and Git lineage.

The catalog is returned by `GET /api/v1/project-templates`. It is the authoritative
source for the console, Python SDK, and the `kionga` workspace command.

In **Projects → New project**, the console loads this API rather than maintaining a
second hardcoded list. It shows framework, accelerator, capabilities, required
services, and recommended profile; options outside a normal user's grant are
identified before submission. The gateway repeats every check, so browser state is
never the security boundary.

## Template catalog

| Template | Use it for | Frameworks | Accelerators | Recommended profile |
| --- | --- | --- | --- | --- |
| `production-ml` | Reproducible training, evaluation gates, MLflow registration, batch inference, serving, and monitoring | `scikit-learn`, `xgboost`, `pytorch` | CPU, single GPU, multi-GPU | `power` |
| `distributed-training` | Large deep-learning jobs with PyTorch DDP, mixed precision, durable checkpoints, and reproducible evaluation | `pytorch-ddp` | Single GPU or multi-GPU | `gpu` |
| `production-agent` | Stateful LangGraph agents with tools, checkpoints, semantic memory, tracing, evaluations, and an HTTP runtime | `langgraph` | CPU or single GPU | `team` |
| `fullstack-ai` | A model or agent backend, governed API, event function, browser client, containers, CI, and tests | `fastapi-ml`, `fastapi-agent` | CPU or single GPU | `power` |
| `blank-python` | An expert-owned typed Python package with containers, CI, linting, and tests | `python` | CPU, single GPU, multi-GPU | `starter` |

Template versions are immutable contracts. A later template release may add files
or defaults without silently changing projects already pinned to version `1.0.0`.

## What the generator writes

Every starter includes a typed package layout, `pyproject.toml`, Dockerfile,
`.env.example`, tests, Ruff/pytest CI, `platform/project.yaml`, an immutable
`.kionga/template.json` selection record, and a new Git repository when Git is
available. Workload-specific additions are:

| Template | Generated workload boundary |
| --- | --- |
| `production-ml` | Framework-specific training, model artifact/metric output, control-plane registration hook, prediction API, tests, and pipeline manifest |
| `distributed-training` | PyTorch DDP worker, `torchrun` launcher, CPU `world_size=1` smoke path, AMP, resumable rank-zero checkpoint, rank-zero MLflow logging, tests, and API-ready container pipeline JSON/YAML for a matching executor |
| `production-agent` | LangGraph tool loop, pure safe tool, `AgentMemoryClient` boundary, PostgreSQL/in-memory checkpointer selection, Langfuse callback, HTTP runtime, deterministic graph test, golden JSONL evaluation runner, and deployable `KiongaAgent` manifest with supported resources and min/max replicas |
| `fullstack-ai` | FastAPI ML or agent backend, matching browser client, real event-function source/Dockerfile/API request, tests/evaluations, application container, CI, and deployment requests |
| `blank-python` | Minimal typed/tested/container-ready Python service foundation without an imposed ML or agent architecture |

Generation fails when the destination is non-empty. This prevents a template or
coding agent from silently overwriting an existing checkout; use `kionga project
sync` for an already connected repository.

## Discover before creating

=== "SDK"

    ```python
    templates = client.list_project_templates()
    for template in templates:
        print(template.id, template.frameworks, template.accelerators)

    distributed = client.get_project_template("distributed-training")
    print(distributed.required_services)
    ```

=== "API"

    ```bash
    export MLAIOPS_URL="${MLAIOPS_URL:-http://localhost:8080}"
    curl -s "$MLAIOPS_URL/api/v1/project-templates" | python -m json.tool
    curl -s "$MLAIOPS_URL/api/v1/project-templates/production-agent" | python -m json.tool
    ```

=== "Workspace CLI"

    ```bash
    kionga templates
    kionga templates --json
    ```

The gateway rejects an unsupported framework/accelerator combination instead of
creating a project that cannot run. Omitting those fields selects the template's
first supported framework and accelerator and its recommended resource profile.

## Create a heavyweight ML project

The distributed template makes the compute intent explicit at creation time:

```python
project = client.create_project(
    "foundation-model-evaluation",
    template="distributed-training",
    framework="pytorch-ddp",
    accelerator="multi-gpu",
    requested_profile="custom",
    repository_url="git@github.com:acme/foundation-model-evaluation.git",
)
print(project.scaffold_command)
```

For a normal user, the gateway verifies every template-required service before
creation. The owner therefore needs `pipelines`, `models`, `storage`, `git`, and
`workbench`; the requested CPU/RAM/storage profile cannot exceed the grant. A
single-GPU selection requires at least one provisioned GPU, and `multi-gpu` requires
at least two, which normally means a custom grant. Template intent never bypasses
RBAC, quotas, node capacity, or scheduler policy. Admin/operator principals may
create any template for provisioning and recovery workflows.

Run the returned command inside Jupyter or the browser IDE:

```bash
kionga scaffold foundation-model-evaluation \
  --template distributed-training \
  --template-version 1.0.0 \
  --framework pytorch-ddp \
  --accelerator multi-gpu \
  --profile custom
```

The distributed starter is designed around these production boundaries:

- `torchrun`/PyTorch DDP owns process-group launch; the training module remains
  importable and testable without a cluster;
- device and world-size configuration comes from the environment rather than
  being hardcoded;
- mixed precision is configurable and CPU fallback keeps smoke tests portable;
- checkpoints are written to the workspace/object-store path so workers can
  resume after preemption;
- only rank zero records shared MLflow metadata and final model artifacts;
- pipeline jobs declare CPU, memory, GPU, retry, and dependency requirements.

When a normal user saves that pipeline, Kionga calculates the resource total of
each concurrently runnable DAG layer and rejects the definition if its widest layer
exceeds the user's CPU, memory, or GPU grant. Large sequential flows remain possible
without allowing parallel branches to overcommit provisioned capacity.

For a laptop smoke test, use one process. Multi-node scheduling belongs on the
Kubernetes scale path with an appropriate GPU operator, storage class, and workload
scheduler; creating a multi-GPU project does not pretend that local hardware exists.

## Create a functional agent project

```python
project = client.create_project(
    "incident-response-agent",
    template="production-agent",
    framework="langgraph",
    accelerator="cpu",
)
```

Then run `project.scaffold_command`, optionally handing the generated directory to
a configured coding agent:

```bash
kionga scaffold incident-response-agent \
  --template production-agent \
  --template-version 1.0.0 \
  --framework langgraph \
  --accelerator cpu \
  --profile team \
  --agent codex \
  --prompt "Add a read-only runbook search tool and escalation policy"
```

The starter is executable before customization: it has a graph entry point, typed
state, a safe example tool, deterministic tests, a health/invocation runtime,
container packaging, evaluation fixtures, and a platform manifest. Real provider
keys remain external configuration. Deploying the agent records its project,
version, image, graph module, tools, per-replica CPU/memory/GPU, min/max replicas,
and traffic policy in the control plane. Normal-user deployment is admitted against
the provisioned compute and workload limits before Kubernetes reconciliation.

See [Agents](agents.md) for runtime behavior, memory, traces, and deployment.

## Full-stack AI applications

Choose `fullstack-ai` when the deliverable includes more than a training package.
The generated boundary separates the browser client, FastAPI inference/agent API,
event handler, tests, container image, and CI. The API and function can be deployed
independently, while a pipeline definition can connect ingestion, training,
evaluation, deployment, and event-processing jobs into one governed DAG.

## Git and reproducibility

Connect the repository when the project is created or from its detail panel. Kionga
stores repository metadata only; credentials remain in the workspace Git helper.
Every definition-backed run can preserve the repository URL and commit SHA.

Before a production run:

1. synchronize the project with `kionga project sync <project-id>`;
2. commit generated and customized source;
3. build an immutable image tagged by commit SHA;
4. submit the pipeline with the same repository and commit fields;
5. store checkpoints, datasets, and models in provisioned object-storage paths.

## Compatibility aliases

Older generic template values are normalized at the API boundary:

| Input alias | Canonical template |
| --- | --- |
| `tabular-classification`, `forecasting`, `ml` | `production-ml` |
| `rag-agent`, `agent` | `production-agent` |
| `fullstack` | `fullstack-ai` |
| `blank`, `python`, `api`, `function` | `blank-python` |

New automation should use canonical IDs because those IDs, versions, supported
frameworks, and accelerator classes form the durable project contract.
