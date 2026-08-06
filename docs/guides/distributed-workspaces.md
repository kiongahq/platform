# Distributed ML/AI workspaces, functions, and Git

This guide covers the shortest production path from an authenticated teammate to
a reproducible, event-driven application. Kionga keeps four contracts together:

1. an administrator grants services and a named resource profile;
2. Kubernetes reconciles that grant into a bounded workspace and persistent disk;
3. the project points at its Git source of truth; and
4. independent functions are composed into versioned pipeline definitions.

## Provision a teammate

In **Console → Access → Add user**, choose the identity-provider subject, role,
services, project scope, and one resource profile:

| Profile | vCPU | Memory | Storage | Concurrent runs | Functions |
| --- | ---: | ---: | ---: | ---: | ---: |
| Starter | 2 | 4 GB | 25 GB | 1 | 3 |
| Team | 4 | 8 GB | 100 GB | 3 | 10 |
| Power | 8 | 16 GB | 250 GB | 6 | 25 |
| GPU | 8 | 32 GB | 500 GB | 4 | 15 |

Choose **Custom** only when the preset boundaries do not fit. Memory is always an
integer number of GB in the admin surface; function and job memory uses familiar
Kubernetes quantities such as `512Mi`, `2Gi`, and `8Gi`.

On Kubernetes, the access outbox emits a workspace lifecycle command. The
integration worker creates or updates a `KiongaWorkspace`; the operator then owns:

- one persistent volume claim sized from `storage.size_gb`;
- one pod with only the assigned Jupyter and/or IDE containers;
- aggregate CPU and memory split across those containers;
- the assigned GPU resource on Jupyter;
- one internal service and a generated workspace authentication secret.

Suspending the grant scales the workspace to zero. Revoking it removes the
workspace custom resource and its owned workloads. Project, active-run, function,
bucket, and service boundaries remain enforced by the gateway even if a user calls
the API directly instead of using the console.

```bash
kubectl get kiongaworkspaces -A
kubectl get deployment,pvc,service -l app.kubernetes.io/component=workspace -A
```

The Compose profile intentionally keeps one shared Jupyter volume for a laptop or
single-user VM. Per-user resource reconciliation is the Kubernetes distributed-team
profile.

## Make a project Git-native

Create a project with an HTTPS or SSH repository URL, or open an existing project
card and choose **Connect Git repository**. Kionga stores the remote URL, provider,
default branch, and sync metadata; it rejects URLs containing embedded credentials.

Inside Jupyter or the IDE:

```bash
export MLAIOPS_URL=https://platform.example.com
export MLAIOPS_TOKEN='<personal key from Console → Settings>'
kionga project sync prj-123
```

To start a new container-ready ML/API application with a tiny browser client and
GitHub Actions test lane:

```bash
kionga scaffold risk-app --template fullstack-ai --agent none
# or let an installed coding agent complete the bounded starter
kionga scaffold risk-app --template fullstack-ai --agent codex \
  --prompt "Add a feature lookup, model scoring, tests, and deployment docs"
```

The command clones into `/workspace/<project-namespace>`. Existing checkouts are
updated with `fetch`, branch checkout, and `pull --ff-only`; a mismatched origin or
non-empty non-Git target fails closed. Private-repository credentials stay in the
workspace Git credential helper and never enter control-plane state.

For multi-GPU training, use the `distributed-training` project template and the
administrator-provisioned GPU profile. The starter uses rank-aware experiment
logging and durable checkpoint paths; the Kubernetes scheduler remains responsible
for satisfying the declared GPU topology. See [Project templates](project-templates.md).

## Deploy an independent function

Build and push any OCI image that accepts HTTP requests, then use **Functions →
Deploy Function**. Set CPU and memory and select one trigger:

- **HTTP / webhook** for synchronous APIs;
- **Asynchronous queue** for long-running or bursty work;
- **Cron schedule** for a five-field recurring schedule; or
- **Kafka topic** for event-driven processing through a connector.

The platform persists ownership and limits, enforces the user's function quota,
and deploys through the configured OpenFaaS REST API. OpenFaaS documents the
[trigger model](https://docs.openfaas.com/reference/triggers/),
[asynchronous invocation route](https://docs.openfaas.com/reference/async/), and
[cron annotations](https://docs.openfaas.com/reference/cron/). Connector licensing
and availability vary, so verify the connector selected for the deployment.

```bash
export MLAIOPS_FUNCTION_PAYLOAD='{"customer_id":"c-42"}'
mlaiops function deploy prj-123 score-events ghcr.io/acme/score-events:1.4.0
mlaiops function invoke score-events
mlaiops function invoke-async score-events
```

Set `OPENFAAS_URL`, `OPENFAAS_USER`, and `OPENFAAS_PASSWORD` on the gateway. The
same control-plane integration works with a Kubernetes OpenFaaS gateway or a faasd
gateway used by a local/single-VM environment.

## Compose functions into a pipeline

A pipeline definition is a versioned DAG. Each job has a stable name, kind,
dependencies, resources, environment, and retry count. The API rejects duplicate
names, missing dependencies, cycles, invalid quantities, and undeployed function
references before saving.

```json
{
  "project_id": "prj-123",
  "name": "event-score",
  "version": "4",
  "execution_mode": "functions",
  "commit_sha": "4cb92a1",
  "jobs": [
    {
      "name": "extract",
      "kind": "function",
      "function": "extract-events",
      "depends_on": [],
      "resources": {"cpu": "500m", "memory": "512Mi"},
      "retries": 2
    },
    {
      "name": "score",
      "kind": "function",
      "function": "score-events",
      "depends_on": ["extract"],
      "resources": {"cpu": "1", "memory": "1Gi"},
      "retries": 1
    }
  ]
}
```

Save this from **Pipelines → Define flow** or `POST
/api/v1/pipelines/definitions`, then run it with a parameter object. Ready jobs run
in parallel; dependency output is passed to downstream functions; every attempt and
step transition is persisted. Function-mode definitions dispatch through configured
OpenFaaS. Container-mode definitions dispatch to the bundled
`pipeline-definition/mlaiops` deployment, which runs admitted OCI jobs locally; the
Kubernetes scale path replaces that Docker-socket trust boundary with
scheduler-isolated workloads.

The console uses the pinned, MIT-licensed
[@dagrejs/dagre](https://github.com/dagrejs/dagre) layout engine to render the
actual dependency graph. Dagre is presentation only: execution order always comes
from the validated server-side DAG.

## Agent memory, kept simple

Workspace memory and agent memory are separate controls:

- workspace RAM comes from the admin resource profile;
- short-term agent checkpoints use `MLAIOPS_CHECKPOINT_DSN` (or `DATABASE_URL`);
- long-term semantic memory uses the same PostgreSQL service with pgvector;
- artifacts and larger context belong in object storage, not in prompts or queues.

For a standard installation, set one PostgreSQL URL on the agent runtime and use
the default checkpointer/memory clients. Split databases only when retention,
regional residency, or workload isolation requires it.

## Production checklist

- Use OIDC, provision by immutable subject, and assign only required services.
- Put workspace services behind the organization's authenticated ingress.
- Use a storage class that supports the required durability and expansion policy.
- Publish immutable function images and record the Git commit on every flow.
- Configure OpenFaaS connectors, retries, queue limits, and payload-size policy.
- Monitor audit/outbox lag, workspace reconciliation, pipeline steps, and function
  invocation failures.
- Back up PostgreSQL and object storage; test restore procedures before onboarding
  production teams.
