# Pipelines

A **flow** is a versioned directed acyclic graph (DAG) of container jobs or deployed
functions. A **run** is one execution of a flow (or of the built-in training
pipeline). Every run records exactly what it executed: the definition revision and
its SHA-256, the image digests the runner pulled, the parameters and overrides, the
policy decision, and the workload that ran each node.

Gateway examples assume `export MLAIOPS_URL=http://localhost:8080`.

## The moving parts

- **gateway** validates and stores definitions and their revisions, runs the
  scheduler, enforces run preflight, dispatches runs, records step facts and logs.
- **pipeline-runner** serves the Prefect deployments `training-pipeline/mlaiops`
  and `pipeline-definition/mlaiops`. It runs one container per node through the
  Docker socket (trusted local profile; see [Trust boundary](#trust-boundary)).
- **prefect-server** is the engine for container flows (UI at <http://localhost:4200>).
- **OpenFaaS** (optional) runs function nodes; function flows are orchestrated by the
  gateway itself.

## Defining a flow

Open **Pipelines → ＋ Define flow**. The editor has two tabs over **one** definition:

- **Form**: nodes, dependencies ("runs after"), resources, retries, timeouts, a
  schedule, default parameters and what a manual run may override.
- **YAML**: the same definition as `kionga.dev/v1 Pipeline`, for review in Git.

Every change is validated live by `POST /api/v1/pipelines/validate`, which returns
the canonical YAML, its SHA-256, the dependency stages and every issue with the
field, node and YAML line it belongs to. Switching tabs converts through that
response, so the form and YAML never diverge. Kionga stores YAML in canonical form:
comments and key order you type are not kept.

```yaml
apiVersion: kionga.dev/v1
kind: Pipeline
metadata: {name: churn-training, project: prj-…, version: "3"}
spec:
  executionMode: prefect            # containers; "functions" for OpenFaaS nodes
  parameters: {window: daily}
  overridable: {parameters: [window], resources: true, nodes: true}
  triggers:
    - {type: schedule, cron: "30 2 * * *", timezone: Africa/Nairobi}
  nodes:
    - {id: extract, type: container, image: ghcr.io/acme/churn@sha256:…}
    - {id: features, type: container, image: …, dependsOn: [extract]}
    - {id: report, type: container, image: …, dependsOn: [extract],
       when: {param: window, equals: daily}}
    - {id: train, type: container, image: …, dependsOn: [features, report],
       resources: {cpu: "4", memory: 8Gi, gpu: 1},
       retry: {max: 2, backoffSeconds: 60}, timeoutSeconds: 7200}
```

The structural schema is published in the repository at
`contracts/pipeline/v1.schema.json` (with `contracts/pipeline/examples/`) for editor
tooling. The gateway additionally rejects what a schema cannot express: duplicate
node ids, dependencies on missing nodes, cycles (reported as a path such as
`a → b → c → a`), invalid cron or timezone, timeouts above 24 hours, retries above
10, and overridable parameters that are not declared.

### Revisions, diff and rollback

Each saved change creates an immutable **revision** with author, message and hash;
saving identical content does not. In a flow's detail view, **Revisions** lists
them, **Compare with current** shows a line diff, and **Roll back** saves the old
content as a new revision, so history is never rewritten and every run still points
at what it actually executed.

```bash
curl -s "$MLAIOPS_URL/api/v1/pipelines/definitions/<id>/revisions"
curl -s "$MLAIOPS_URL/api/v1/pipelines/definitions/<id>/yaml"
curl -s -X POST "$MLAIOPS_URL/api/v1/pipelines/definitions/yaml" \
  -H 'Content-Type: application/json' \
  -d "{\"yaml\": $(jq -Rs . < flow.kionga.yaml), \"message\": \"tune train\"}"
```

To review definitions in Git, keep them as `pipelines/<name>.kionga.yaml` in the
project repository and use the CLI (`MLAIOPS_URL` and an API key in `MLAIOPS_TOKEN`):

```bash
mlaiops pipeline export <definition-id> > pipelines/churn.kionga.yaml   # start from Kionga
mlaiops pipeline validate pipelines/churn.kionga.yaml                   # in CI: issues with line numbers, exit 1
mlaiops pipeline apply pipelines/churn.kionga.yaml <definition-id> "PR #42"  # after merge
```

Applying unchanged YAML creates no revision. Kionga never commits to your
repository; Git stays the review step and Kionga the execution record.

## Schedules

A `schedule` trigger uses five-field cron in an IANA timezone. The flow list and
detail show whether the schedule is active or paused, the next and last run, and
**Pause** / **Resume** buttons.

- One gateway replica fires schedules at a time (a lease held in the database).
- Each schedule slot is claimed exactly once, so restarts never double-run a slot.
- After downtime a schedule runs once for the most recent missed slot, not once per
  missed slot.
- Scheduled runs execute **as the flow's owner**, with the owner's project access
  and run quota at the moment the slot fires. If the owner lost access or is
  suspended, the slot is recorded as failed with that reason and the schedule
  moves on.

## Running a flow manually

**Pipelines → ▶ Run pipeline** (or **Run** on a flow) opens the run configuration:

- **Parameters**: each default beside a field for this run. Parameters not listed in
  `spec.overridable.parameters` show a lock and the reason.
- **Nodes** (when allowed): run a subset (Kionga adds every node they depend on),
  replace an image (must be pinned by digest, and from an approved registry when
  `KIONGA_IMAGE_REGISTRIES` is set), change CPU/memory within your grant, or change
  retries and timeouts.
- **Check run** shows the preflight: project access, concurrent-run quota, capacity
  of the widest parallel stage, image rules, engine availability and exactly what
  this run changes. **Start run** is enabled only after it passes.

The gateway runs the same preflight on submit, so a hand-crafted request cannot
apply overrides the flow, policy or quotas do not allow. The trigger is derived from
how the request arrived (`manual` from the console, `api` from an API token), never
from the request body. Priority is not offered: the bundled executor cannot honour
it, so it would be a misleading control. Flows that declare no parameters pass run
parameters through unchecked, with a preflight warning.

**Rerun from failure** (on a failed run) starts a new run that reuses the recorded
outputs of nodes that succeeded and runs the failed node and everything downstream
again. Reused nodes are shown as skipped with the run they were reused from.

## How execution works

Both executors (Python for containers, Go for functions) use the same **ready-set**
semantics:

- a node starts as soon as **its own** dependencies have succeeded; independent
  branches run in parallel (default at most 8 at once, overridable per run);
- a node whose `when` condition is false is skipped, and its dependents still run;
- nodes downstream of a failure are skipped with the reason, while unrelated
  branches finish;
- each attempt honours the node's timeout and retry backoff.

The runner reports, per node: start and end time, attempt, exit code, workload kind
and id (`docker-container` with the container id locally; function nodes report
`openfaas-call`), and the image digest it actually ran. A local container is never
labelled a Kubernetes Pod.

Runs are dispatched with the **revision pinned at submission**, so editing a flow
while a run is queued does not change what that run executes.

If no engine is configured, submission is refused with the reason instead of
leaving a run "queued" forever.

## Watching a run

Click a run to open it: status, trigger, owner, definition revision and hash,
policy decision and overrides, then the execution graph. The graph supports zoom,
fit-to-view, panning (drag) and keyboard navigation (arrow keys between nodes,
Enter to open one). Status is shown by colour, symbol and text.

Selecting a node shows its timing, attempt, exit code, workload, image digest,
dependencies and **logs and events** for that node, with severity and source
filters, search, earlier pages and a live tail. See [Logs](logs.md).

## Trust boundary

The Compose executor mounts the Docker socket into the pipeline runner. That is a
deliberate root-equivalent trust boundary for a trusted laptop or single-operator VM,
not hostile multi-tenant isolation. Child jobs never receive the socket and run with
all capabilities dropped, `no-new-privileges` and a PID limit.

## Heavy and distributed training

Start with the [`distributed-training`](project-templates.md#create-a-heavyweight-ml-project)
template for PyTorch DDP workloads and express orchestration as ordinary nodes
around the trainer. For normal users, the widest set of nodes that can run at the
same time must fit the CPU, memory and GPU grant; definitions and manual overrides
that exceed it are rejected before anything starts.

- pin images by digest and record the Git commit;
- checkpoint at bounded intervals to provisioned object storage;
- make retry/resume idempotent;
- have only rank zero publish shared MLflow/model-registry results;
- separate evaluation and registration so quality gates remain auditable.

## Kubernetes

The control plane's executor contract (dispatch with a pinned revision, step facts,
typed workload ids, ready-set semantics) is executor-neutral, but **the gateway does
not yet create Kubernetes Jobs or Pods for pipeline nodes**. The operator contains a
`KiongaPipelineRun` reconciler that targets KFP; the gateway does not create those
resources. Multi-node and isolated multi-tenant execution therefore remain on the
roadmap; see [Implementation status](../reference/implementation-status.md).

## Writing a native Prefect flow

For most jobs, a container node is enough. For a specialised native flow, add it
under `python/pipelines/`, wrap each step in `reported_step(run_id, "step-name")`,
and serve it from `python/pipelines/serve.py`. Keep ML library pins aligned with
the serving image so trained models load under serving.
