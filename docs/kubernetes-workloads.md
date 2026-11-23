# Kubernetes workloads

On Kubernetes the gateway records each workload and creates a
`kionga.dev/v1alpha1` custom resource. The operator reconciles it and writes
status; the gateway reads that status back. The gateway never creates
Deployments, Services or InferenceServices itself, and no endpoint reports a
workload as running before the operator says it is.

## Mode

The gateway is in Kubernetes mode when `KIONGA_RUNTIME=kubernetes`, or when
`KIONGA_RUNTIME` is unset and `KUBERNETES_SERVICE_HOST` is present (every pod
has it). `KIONGA_RUNTIME=compose` forces Compose mode.

## Gateway settings

| Variable | Default | Purpose |
| --- | --- | --- |
| `KIONGA_TARGET_NAMESPACE` | `default` | Namespace of the workload custom resources (the operator's namespace) |
| `KIONGA_APPLICATIONS_BASE_DOMAIN` | empty | Applications are served at `https://<name>.<base domain>`; empty disables applications |
| `KIONGA_KEDA_INTERCEPTOR_URL` | `http://keda-add-ons-http-interceptor-proxy.keda:8080` | KEDA HTTP add-on interceptor used to invoke functions |
| `KIONGA_EXECUTOR` | automatic | `kubernetes` in Kubernetes mode; any other value sends container flows to the pipeline runner |
| `KIONGA_WEBSITE_URL` | empty | Link to the Kionga website in the console and on sign-in; empty hides it |

The gateway's service account needs, in the target namespace: get, list,
create, update and delete on `kiongaapplications`, `kiongafunctions` and
`kiongamodeldeployments`; list on `pods`; get on `pods/log`; list on `events`.

## Applications

`/api/v1/applications` creates, lists, reads, replaces, deletes and redeploys
applications, and returns their pod logs and events. On Compose every endpoint
answers `409` with `{"error":"not_configured","message":…,"steps":[…]}`.

Names are DNS labels that start with a letter. Platform host names (`api`,
`auth`, `console`, `www` and others) and the workspace host families
(`workbench-`, `ide-`, `langflow-`, `kionga-`) are reserved, and a name whose
host equals the console host is refused.

The `KiongaApplication` spec carries `image` or `source {git, ref, contextDir,
dockerfile}`, `port`, `env [{name, value}]`, `secretRefs [{name}]`,
`resources {requests, limits}`, `replicas {min, max}`, `scaling {metric,
target}`, `healthPath`, `visibility` and, after a redeploy, `restartedAt`.
The gateway reads `status.phase`, `status.url`, `status.readyReplicas`,
`status.message`, `status.conditions` and `status.lastBuild`. Status maps to
`failed` (phase `Failed`, or `Ready=False` with reason `Failed`), `ready`
(`Ready=True`), `scaled-to-zero` (`Ready=True` with no ready replicas) and
otherwise `pending`. Logs and events come from pods labelled
`kionga.dev/application=<name>`.

## Functions

On Kubernetes, `POST /api/v1/functions` creates a `KiongaFunction` (`image`,
`port` 8080, `env`, `resources`, `replicas {min: 0, max: 5}` and `scaling
{metric: requests, target: 100}` by default). Compose keeps faasd.

Invocations of a function scaled on requests go to the KEDA HTTP add-on
interceptor with the function's host in the `Host` header: `status.host`
when the operator reports it, otherwise `<name>.<namespace>.svc.cluster.local`.
A function at zero replicas has no ready endpoints, so a call to its Service
fails and nothing scales it up. The interceptor counts the pending request,
which the add-on's scaler turns into the scale-from-zero signal, and holds the
request until a replica is ready. A function scaled on CPU always keeps a
replica and is called on its Service directly.

`invoke-async` on Kubernetes accepts the call and runs it in the background of
the gateway replica that received it. There is no queue: a call is lost if
that replica stops before it completes. The outcome is logged with the call id.

## Model deployment

`POST /api/v1/models/{id}/deploy` applies one `KiongaModelDeployment` per
model name and project with `modelName`, `version`, `canaryWeight` and, for a
canary, `canaryVersion`. The model is `pending` until the operator reports
`Ready=True` with `status.url` (or `CanaryReady=True` with `status.canaryURL`
for the canary version); then it is `serving` with that endpoint. Phase
`Failed`, or the readiness condition `False` with reason `Failed`, makes it
`failed` with the reason in `deployment_message`.
`GET /api/v1/models/{id}/deployment` returns the live status.

A canary weight from 1 to 99 needs a serving stable version. Invocations
through `POST /api/v1/models/{id}/predict` for either version are split by the
weight: without a routing key, exactly `w` of every 100 consecutive calls go to
the canary, evenly spread; with an `X-Kionga-Routing-Key` header the choice is
sticky for that key. The `X-Kionga-Model-Version` response header names the
version that answered.

On Compose, Deploy needs `SERVING_MANAGER_URL`. Without it Deploy answers
`409 not_configured` and records nothing.
