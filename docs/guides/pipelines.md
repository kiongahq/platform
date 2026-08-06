# Pipelines

The bundled Kionga training flow **really executes** through Prefect, and function
flows invoke deployed OpenFaaS jobs in dependency order. Both report the same
persisted step graph and logs. Container-only definitions also execute through the
bundled `pipeline-definition/mlaiops` Prefect deployment in the Compose profile:
each ready DAG layer starts concurrently as resource-bounded OCI jobs.

Gateway examples assume `export MLAIOPS_URL=http://localhost:8080`.

## The moving parts

- **prefect-server** — the engine (UI at <http://localhost:4200>).
- **pipeline-runner** — serves the platform flows as Prefect deployments and runs
  both the registered training flow and general container definitions; pins
  `mlflow==3.1.1` + `scikit-learn==1.7.0`.
- **gateway** — accepts submissions, creates flow runs, and recomputes run status
  from the steps the flow reports.
- **OpenFaaS** — runs independent function jobs and function-only DAGs, including
  parallel ready jobs and configured retries.

## Submitting a run

=== "Console"

    **Pipelines → ▶ Run pipeline**, pick a project and `training-pipeline`, submit.
    The run appears immediately as `queued`, then the DAG animates as steps run.

=== "SDK"

    ```python
    run = client.submit_pipeline(project.id, name="training-pipeline")
    ```

=== "API"

    ```bash
    curl -s -X POST "$MLAIOPS_URL/api/v1/pipelines/submit" \
      -H 'Content-Type: application/json' \
      -d '{"project_id":"<id>","name":"training-pipeline"}'
    ```

What happens under the hood:

1. The gateway persists a `queued` run (+ audit + outbox).
2. It creates a **Prefect flow run** carrying the platform run id and project id.
3. If the engine rejects the submission, the run is marked **failed** (fail-closed).

## The training flow

`python/pipelines/training.py` defines `training_pipeline` with four steps:

```
validate  →  train  →  evaluate  →  register
```

- **validate** — builds a deterministic synthetic dataset (fixed `random_state`), so
  every environment produces identical metrics.
- **train** — fits a scikit-learn classifier.
- **evaluate** — computes metrics.
- **register** — logs the run and model to **MLflow** and registers the model version
  with the control plane against the submitting project.

Each step is wrapped in `reported_step`, which posts `running → succeeded/failed` to
`POST /api/v1/pipelines/runs/{id}/steps`. The gateway recomputes status and progress
deterministically and pushes a digest change over SSE so the console's DAG updates.

## Watching a run

=== "Console"

    Click a run row to open its detail sheet: a **DAG** laid out by the pinned
    Dagre library with per-step status dots, plus a **live log tail**. Cancel and
    retry buttons are there (engineer+).

=== "API"

    ```bash
    curl -s "$MLAIOPS_URL/api/v1/pipelines/runs/<run-id>" | python -m json.tool
    ```

## How the DAG visualization works

Kionga uses the open-source, MIT-licensed `@dagrejs/dagre` library to calculate a
left-to-right layout for the graph returned by the API. The renderer shows:

- one node per persisted step, including status and progress semantics;
- one directed edge for every validated `depends_on` relationship;
- parallel branches at the same rank when their dependencies are satisfied;
- the live log tail alongside the graph; and
- refreshed state from the platform SSE stream as jobs transition;
- focusable nodes with accessible names containing job, target, and status; and
- visible authoring warnings for duplicates, missing dependencies, self-edges, and
  cycles before a definition reaches server admission.

The **Define flow** modal renders this graph as jobs are edited, so parallelism and
invalid edges are visible before save. Run detail re-renders from persisted live
state rather than reusing the draft preview.

The browser never decides execution order. The gateway validates references and
cycles before saving a definition, and the execution adapter schedules only from
that validated graph. Dagre receives a presentation copy. A compact renderer is
available when the pinned browser asset cannot load, so logs and step state remain
usable without changing backend behavior.

This keeps the runtime independent of Graphviz binaries or a server-side rendering
service while still using an established open-source DAG layout algorithm. The
dependency analysis and SVG renderer live in `pipeline-graph.js` behind the
testable `KiongaPipelineGraph.analyze()` and `.render()` interface.

## Cancel & retry

```bash
curl -s -X POST "$MLAIOPS_URL/api/v1/pipelines/runs/<id>/cancel" -d '{}'
curl -s -X POST "$MLAIOPS_URL/api/v1/pipelines/runs/<id>/retry" -d '{}'
```

Cancellation is authoritative in the control plane and also propagates to Prefect to
stop the actual execution.

## Reusable definitions and function flows

Use **Pipelines → Define flow** to save a versioned DAG of container or function
jobs. Function references must already exist in the same project. The gateway
validates dependency references and rejects cycles before persisting the definition.
Submitting with a `definition_id` preserves the definition, parameters, execution
mode, Git repository, and commit lineage on the run.

Ready function jobs execute concurrently, then pass their output to downstream jobs.
For a container definition, the bundled runner starts the declared image and command,
passes run parameters plus dependency outputs as bounded JSON environment values,
applies admitted CPU/RAM/GPU limits, retries each job as configured, captures bounded
logs, enforces a timeout/PID limit, and removes the container after every attempt.
See [Distributed workspaces, functions, and Git](distributed-workspaces.md) for a
complete example.

The Compose executor mounts the Docker socket into the pipeline runner. That is a
deliberate root-equivalent trust boundary for a trusted laptop or single-operator VM,
not hostile multi-tenant isolation. Child jobs never receive the socket and run with
all capabilities dropped plus `no-new-privileges`. Distributed-team deployments
should use Kubernetes-native workers/KFP and the cluster scheduler instead of
exposing a node runtime socket.

## Writing your own container flow

For most jobs, define the image, command, dependencies, resources, retries, and
environment through the console/API; no custom Prefect Python is required. For a
specialized native flow, add it under `python/pipelines/`, wrap each step in
`reported_step(run_id, "step-name")`, and serve it from `python/pipelines/serve.py`.
Keep ML library pins aligned with the model-specific `serving_image` (or the generic
fallback) so trained models load under serving. The SDK's `pipelines.py` compiler
emits the same definition contract.

## Heavy and distributed training

Start with the [`distributed-training`](project-templates.md#create-a-heavyweight-ml-project)
template for PyTorch DDP workloads. It establishes portable single-process tests,
`torchrun` launch, mixed precision, rank-aware MLflow logging, and resumable
checkpoint paths. Express orchestration as ordinary jobs around the distributed
trainer, then deploy that flow to a matching executor, for example:

```text
validate-data ──► distributed-train ──► evaluate ──► register ──► deploy
                         │
                         └── checkpoints and artifacts ──► object storage
```

The project template records accelerator intent; the user's provisioned profile
and each job's resource request remain the enforcement boundary. Compose is useful
for CPU or single-device smoke tests. Multi-node/multi-GPU execution belongs on the
Kubernetes scale path, where the scheduler can satisfy topology and capacity.

For normal users, definition admission groups jobs by dependency depth and sums the
CPU, memory, and GPU requests in each potentially concurrent layer. The definition
is rejected with `resource_not_provisioned` if any layer exceeds the administrator's
grant. This permits large sequential workflows without allowing a wide parallel
branch to overcommit the user's allocation. Admin/operator principals bypass this
admission check for platform operations.

Keep these rules for large jobs:

- pin the container image and Git commit;
- checkpoint at bounded intervals to provisioned object storage;
- make retry/resume idempotent;
- have only rank zero publish shared MLflow/model-registry results;
- validate data and environment compatibility before allocating GPUs;
- separate evaluation and registration so quality gates remain auditable.

## Kubernetes fidelity path

On the scale path, pipelines integrate with KFP/Argo workflows and the operator reconciles
`KiongaPipelineRun` CRDs. The KFP integration client stays wired for that path; it's
not needed for local or single-VM use. Migrating an existing cluster to the renamed
resource kinds requires the procedure in
[Kionga identifier migration](../reference/kionga-migration.md).
