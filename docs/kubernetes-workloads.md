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
`kiongamodeldeployments`; list on `pods`; get on `pods/log`; list on `events`; get on `secrets`
(named, to check `secret_refs`); get on `deployments`.

`secret_refs` may name only Secrets labelled `kionga.dev/workload-secret=true`;
the operator skips any other Secret, so the gateway answers 422 for a
missing or unlabelled one. The gateway creates no workload Secrets itself.

## Applications

`/api/v1/applications` creates, lists, reads, replaces, deletes and redeploys
applications, and returns their pod logs and events. On Compose every endpoint
answers `409` with `{"error":"not_configured","message":…,"steps":[…]}`.

Names are DNS labels that start with a letter. Platform host names (`api`,
`auth`, `console`, `www` and others) and the workspace host families
(`workbench-`, `ide-`, `langflow-`, `kionga-`) are reserved, and a name whose
host equals the console host is refused. `visibility` is `sso` (the default:
signed-in users, through the gateway), `public`, or `internal` (no host; the
application has no `url`).

The `KiongaApplication` spec carries `image` or `source {git, ref, contextDir,
dockerfile}`, `port`, `env [{name, value}]`, `secretRefs [{name}]`,
`resources {requests, limits}`, `replicas {min, max}`, `scaling {metric,
target}`, `healthPath`, `visibility` and, after a redeploy, `restartedAt`.
The gateway reads `status.phase`, `status.url`, `status.readyReplicas`,
`status.message`, `status.conditions` and `status.lastBuild`. Status maps to
`failed` (phase `Failed`, or `Ready=False` with reason `Failed`), `ready`
(`Ready=True`), `scaled-to-zero` (`Ready=True` with no ready replicas) and
otherwise `pending`. Responses carry `generation` (`metadata.generation`) and
`observed_generation` (`status.observedGeneration`); while the operator has
not observed the current generation the status is `pending`, so a client
waiting after an update never reads the previous rollout. The operator must
write `status.observedGeneration`.

Logs and events come from pods labelled `kionga.dev/application=<name>`;
events are matched by object UID. `GET …/logs?follow=true` is a Server-Sent
Events stream: each line is `id: <cursor>`, `event: log`,
`data: {"pod", "container", "time", "line"}`, and the stream ends with
`event: end` and a reason. It is bounded (20 pods, `max_seconds` default 600,
at most 3600) and resumes from `Last-Event-ID` or `?since=`.

The id is an opaque cursor (`v1.` followed by base64url, at most about 440
characters) that records the last line delivered for each pod and container.
Send the last id back unchanged: each pod continues exactly after its own
last delivered line, so a line one pod delivers late is not lost and lines
sharing a timestamp are neither repeated nor dropped. A pod the cursor does
not know, such as one started after it was issued, is read from the time
the stream first opened. An RFC 3339 timestamp is accepted in place of a
cursor (every pod then skips lines at or before it), which is what clients
written before the cursor send. A resume point that is neither is refused
with `400 invalid_request`. A stream with no resume point starts each pod at
the first of its last `tail` lines; when several lines share that line's
timestamp all of them are sent, so a pod can deliver a few more than `tail`.

### Sign-in for `sso` applications

For `visibility: sso` the operator sends the application's host to the
gateway (an ExternalName Service `<name>-sso`, `KIONGA_SSO_UPSTREAM` on the
operator) and records the upstream in the annotation `kionga.dev/upstream`
(else `status.internalURL`). The gateway, with
`KIONGA_APPLICATIONS_BASE_DOMAIN` and `KIONGA_ALLOWED_ORIGIN` set:

1. redirects a request without an application session to
   `<console>/applications/<name>/sso?next=<path>`, which needs a signed-in
   console session (signed-out visitors go to sign-in and come back);
2. checks the caller may use the application's project (applications
   service, project assignment, not suspended), then hands a signed ticket
   (60 seconds, this application only) to `https://<name>.<base>/_kionga/sso`;
3. sets a host-only `kionga_app_session` cookie (8 hours) and returns to the
   path;
4. re-checks access on every request, then reverse-proxies to the upstream
   with `X-Kionga-Subject` set and its own cookie removed.

Tickets and sessions are signed with a key derived from
`KIONGA_CREDENTIAL_KEY` (or `KIONGA_INTERNAL_TOKEN`), so every gateway replica
accepts them. Public and internal applications are never served by the
gateway.

## Functions

On Kubernetes, `POST /api/v1/functions` creates a `KiongaFunction` (`image`,
`port` 8080, `env`, `resources`, `visibility` (default `internal`, or `sso` or `public`), `replicas {min: 0, max: 5}` and `scaling
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

## Agents

Agent deploys (`POST /api/v1/agents` and `POST /api/v1/agent-versions/{id}/deploy`)
accept `scaling {metric, target}`: `requests` (1 to 10000 concurrent requests
per replica, default 100) or `cpu` (1 to 100 percent). It becomes
`spec.scaling` on the `KiongaAgent`. The gateway invokes an agent at its `status.url`, which leads
through the KEDA HTTP interceptor when the agent scales on requests; until the
operator publishes it, the gateway falls back to the agent's Service.

## Model deployment

`POST /api/v1/models/{id}/deploy` applies one `KiongaModelDeployment` per
model name and project with `modelName`, `version`, `canaryWeight` and, for a
canary, `canaryVersion`. The stable version is `serving` once `Ready=True`
with `status.url`; the canary once `CanaryReady=True` (or `Ready=True` when
the operator reports no `CanaryReady`) with `status.canaryURL`. While a canary
or another change rolls out, a stable version that was serving keeps serving.
Status written for an older generation is ignored. Phase `Failed`, or the
readiness condition `False` with reason `Failed`, makes it `failed` with the
reason in `deployment_message`.
`GET /api/v1/models/{id}/deployment` returns the live status.

A canary weight from 1 to 99 needs a serving stable version. Invocations
through `POST /api/v1/models/{id}/predict` for either version are split by the
weight (`status.canaryWeight` when the operator reports it, else `spec.canaryWeight`): without a routing key, exactly `w` of every 100 consecutive calls go to
the canary, evenly spread; with an `X-Kionga-Routing-Key` header the choice is
sticky for that key. The `X-Kionga-Model-Version` response header names the
version that answered.

On Compose, Deploy needs `SERVING_MANAGER_URL`. Without it Deploy answers
`409 not_configured` and records nothing.
