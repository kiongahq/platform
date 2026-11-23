# Architecture

This document describes how the Kionga control plane is organized. Read it before
you add a route, a store method or a service. The
[engineering handbook](https://kiongahq.github.io/docs/engineering/) has the longer
version.

## Bird's-eye view

```text
Console / SDK / CLI ──HTTP + JSON──► gateway ──► PostgreSQL (state, audit, outbox)
                                       │
                                       ├── Prefect          pipeline runs (Compose)
                                       ├── Kubernetes Jobs  pipeline runs (opt-in)
                                       ├── serving manager  model servers (Compose)
                                       ├── MLflow, Langfuse, object storage, Kafka REST
                                       └── workspaces       Jupyter and IDE, proxied
Kubernetes: operator reconciles Kionga CRDs; integration worker turns Kafka
commands into CRDs.
```

The gateway is one Go binary that serves the REST API under `/api/v1`, the OpenAPI
document at `/api/openapi.json`, and the console's static files, all embedded.
Every request passes through sign-in, role and policy checks before a handler runs.
Handlers persist through one `store.Repository` interface, backed by PostgreSQL
(`DATABASE_URL`) or, for development, a JSON file. External engines are called over
their standard HTTP APIs; the repository vendors none of them.

## Code map

All Go code is in the module `github.com/kiongahq/platform` under `go/`.

### `go/cmd`

One directory per binary; `make build` writes `bin/kionga-<name>`.

| Command | Role |
| --- | --- |
| `gateway` | REST API, console, scheduler, log retention, blog publisher; port `8080` (`PORT`), internal workspace listener on `8087` |
| `operator` | Kubernetes controllers for agents, pipeline runs, model promotions and workspaces |
| `integration-worker` | Consumes lifecycle commands from Kafka REST and applies them as CRDs |
| `feature-gateway` | Online feature reads and writes over Redis or Feast; port `8083` |
| `storage-proxy` | SigV4 presigned URLs and bucket browsing; port `8084` |
| `metrics-collector` | Prometheus metrics for component health and platform usage; port `9090` |
| `log-exporter` | Ships one tenant's logs to Elasticsearch or OpenSearch |
| `sandbox-manager` | Runs agent sandboxes under gVisor through the container engine or Kubernetes; internal only, port `8088` |
| `cli` | The `kionga` CLI |

`frontend/` contains the Next.js App Router frontend. The `app/` directory defines
routes; `components/` provides React/TypeScript page compositions, console views,
dialogs and shared UI. `hooks/` and `lib/` contain API access, types and content
serialization. The public blog uses React, and the studio uses Tiptap with the
gateway's editorial API and article block schema.

React owns all page state, form submission, dialogs, graph editing and editorial
workflows. Console pages use `/console/<screen>` routes, studio pages use
`/studio/<screen>`, and project/resource context uses query parameters. Shared
hooks handle cancellation, refresh events and unsaved changes.

The Next.js static export is generated into `go/cmd/gateway/frontend-dist/` and
embedded into the gateway, including `_next` chunks. Run `make frontend` before
building Go directly. Generated HTML is a deployment artifact; frontend source
changes belong in `frontend/`. `httpapi/frontend.go` serves extensionless routes
and rejects retired `.html` page URLs. Production runs as a single Go service without
a separate Node.js server.

### `go/internal`

- `httpapi`: the HTTP layer on `net/http.ServeMux`. `server.go` builds the router
  and middleware (`logging`, `securityHeaders`, `cors`, `auth.RBACWithResolver`);
  domain files register their routes through `registerRoutes` in `routes.go`.
- `auth`: OIDC token verification, single sign-on and local sessions, personal API
  keys, the sign-in `Authenticator`, roles and the `RBAC` middleware.
- `policy`: the pure IAM evaluator: default deny, explicit deny wins, built-in role
  baselines.
- `store`: `Repository` (`repository.go`), the file implementation (`store.go`) and
  PostgreSQL (`postgres.go`, migrations in `migrations/`), with the transactional
  outbox (`outbox.go`).
- `pipelinespec`: parses and validates `kionga.dev/v1 Pipeline` YAML into the
  canonical form whose SHA-256 identifies a revision.
- `execution`: ready-set scheduling of a validated DAG. `k8sexec` runs nodes as
  Kubernetes Jobs. `scheduler` fires cron triggers under a PostgreSQL lease.
- `integrations`: clients for Prefect, MLflow, Langfuse, OpenFaaS and Kafka REST;
  `lifecycle_worker.go` is the at-least-once Kafka boundary.
- `operator`: reconcilers behind the `operator` command. Agents, applications and
  functions become Deployments with KEDA HTTP (requests) or CPU autoscaling;
  applications get an Ingress at `<name>.<baseDomain>`; model deployments become
  KServe `InferenceService`s in RawDeployment mode. See "Kubernetes workloads".
- `feature`, `storage`, `metrics`, `logexport`: the logic behind the matching
  commands.
- `editorial`: the engineering blog's blocks, sanitized rendering, workflow and
  media.
- `platform`: the component health grid and discovery catalog, derived from live
  state.
- `sandbox`: isolated workspaces for model-chosen commands, with Docker and Kubernetes
  drivers; `sandbox/client` is the typed client and fake for the gateway. See its README.
- `agentteam`: the pure core of Agent Teams: validation, the worker state machine,
  crash and retry rules, provenance framing, prompt assembly, compaction.
- `redact`: removes secrets from text before it is stored or exported.
- `runtimeconfig`: loads `KIONGA_CONFIG_FILE` and `<NAME>_FILE` secrets, and the
  production start-up checks.

### `go/pkg`

- `api`: wire types shared by the gateway, CLI and clients, including the versioned
  project template catalog (`project_templates.go`).
- `kube/v1alpha1`: CRD types in group `kionga.dev` (`KiongaAgent`,
  `KiongaApplication`, `KiongaFunction`, `KiongaModelDeployment`,
  `KiongaModelPromotion`, `KiongaWorkspace`). `make generate` writes the deep-copy
  code and the CRDs in `config/crd`; CI fails when they are stale.

### Other directories

- `go/integration`: PostgreSQL integration tests (`-tags=integration`).
- `tests/ui`: console unit tests on jsdom; `tests/e2e`: Playwright acceptance tests.
- `contracts/`: submodule of [contracts](https://github.com/kiongahq/contracts).
- `scripts/`: the integration test runner and the vendor manifest tool.

## Invariants

- **Default deny.** No route is reachable without passing authentication and the
  role and policy check; a resolved account that is disabled has no access.
- **Durable mutations.** In PostgreSQL, a mutation writes the resource, its audit
  event and its outbox record in one transaction. The outbox worker publishes to
  Kafka after commit.
- **Pure core logic.** `policy`, `pipelinespec` and `execution` do no I/O and read no
  clock they are not given, so they are tested as tables.
- **Production fails closed.** With `KIONGA_ENVIRONMENT=production` the gateway
  refuses to start without PostgreSQL, OIDC, the internal token, the credential key
  and HTTPS URLs.
- **Secrets stay out of plain config.** `KIONGA_CONFIG_FILE` rejects secret names;
  secrets come from the environment or `<NAME>_FILE`.
- **Registered resources read scoped secrets only.** A registered record names a
  caller-chosen endpoint, so its `env:` reference resolves only from its scope: the
  prefix `KIONGA_KB_SECRET_*`, `KIONGA_CONNECTION_SECRET_*`, `KIONGA_FEATURE_SECRET_*`,
  `KIONGA_STORAGE_SECRET_*` or `KIONGA_PLATFORM_SECRET_*`, the documented names, or the
  matching `..._REFS` allowlist. The gateway's own secrets are always refused, apart
  from a service's own integration secret on a connection of that type
  (`httpapi/secret_refs.go`). The gateway logs refused records at startup; to migrate
  one, list its variable in the allowlist, or copy a gateway secret into a variable
  with the scope's prefix and point the record there.
- **Banned technologies.** CI fails on any reference to excluded products.
- **Sandboxes fail closed.** With the runtime missing or not proven to be gVisor from
  inside a real container, creates return 503 and nothing is left running. Model output
  never executes outside a sandbox, and git credentials never enter one.
- **A team worker runs under one lease.** The scheduler takes a worker with
  compare-and-set through `store.UpdateDoc`, and every later write checks the lease
  token, on every replica. A tool call commits `started_at` before it runs, so a
  resume never repeats a call that may have happened (see `contracts/agent-team`).
- **Team token budgets count provider usage.** That includes context re-sent on every
  turn, which is real cost, so `max_tokens` spends faster than a transcript grows. The
  defaults are 1,000,000 per worker and 5,000,000 per team.
- **One schedule slot fires once.** The scheduler holds a lease and claims each
  slot before submitting.

## How it connects to other components

- **sdk-python**, the CLI and the console call the REST API.
- **pipeline-runner** executes Prefect runs the gateway submits and reports steps
  and logs back. The Go `execution` package shares its scheduling rules.
- **serving** starts model servers for the gateway (`SERVING_MANAGER_URL`). The
  gateway waits `KIONGA_SERVING_REQUEST_TIMEOUT_SECONDS` (default 90) for the
  manager's answer; keep it above the manager's `SERVE_READY_TIMEOUT_SECONDS`
  (default 80) so a model server that never becomes ready is reported by the
  manager rather than cut off by the gateway.
- **agent-runtime** serves agents the gateway proxies to. Each Kubernetes agent
  receives an independent invocation token from an agent-owned Secret; it has no
  gateway service authority. Published graph callbacks use the scoped API sidecar.
- **trace-proxy** runs as the operator's sidecar in every agent pod.
- **workspace** images are proxied under `/workspaces/` and call back through the
  `8087` listener.
- **contracts** defines the pipeline YAML; **deploy** builds and runs all of it.

## Cross-cutting concerns

- **Testing.** Table-driven unit tests per package, integration tests on a real
  PostgreSQL, console tests on jsdom, and Playwright against a live stack.
- **Security gates.** CI runs govulncheck, gosec and gitleaks; see
  [CONTRIBUTING.md](CONTRIBUTING.md).
- **Naming.** Binaries, environment variables and the CRD group keep the `kionga`
  prefix; newer settings use `KIONGA_`.

## Kubernetes workloads

The gateway records a resource and creates its `kionga.dev/v1alpha1` custom
resource. The operator reconciles the resource and writes status back, and the
gateway reads that status. Every object the operator creates has a controller
reference, so deleting the custom resource deletes them.

- **Applications and functions** (`workload_controller.go`) share one
  reconciler. Each gets a Deployment with startup, readiness and liveness
  probes on `healthPath`. Without `healthPath` the probes use TCP. Rollouts
  never take a pod away before its replacement is ready. Pods carry
  `kionga.dev/application=<name>` or `kionga.dev/function=<name>`, which the
  gateway uses for logs and events. Each workload also gets a ClusterIP Service
  on the container port, and a PodDisruptionBudget when min >= 2.
  `spec.restartedAt` (set by a redeploy) rolls the pods. `status.message`
  repeats the Ready condition message.
  Applications default to `visibility: sso` and one replica. Functions default
  to `internal`, scale to zero on requests and have no Ingress.
- **Scaling** (`workload_scaling.go`, `workload_objects.go`). `requests` creates
  a KEDA HTTP add-on `HTTPScaledObject` and an ExternalName Service
  `<name>-route` pointing at the interceptor. Callers reach the workload
  through `status.internalURL` (or the interceptor with `status.host` as the
  `Host` header), and the interceptor counts each request and can wake a
  scaled-to-zero Deployment. It also accepts the Service name,
  `<name>.<namespace>.svc.cluster.local`. The `HTTPScaledObject` names its
  Deployment in `scaleTargetRef.name` (with `apiVersion`, `kind`, `service`
  and `port`), the shape of HTTP add-on 0.8 and later; tested against the
  v0.16.0 CRD. `cpu` creates a HorizontalPodAutoscaler,
  with at least one replica. The operator checks discovery at startup for the
  add-on and KServe. If the add-on is missing, `requests` falls back to `cpu`
  and the `ScalingReady` condition reports it. Agents use the same scaling
  and expose `status.url`.
- **Ingress.** The host is `<name>.<KIONGA_APPLICATIONS_BASE_DOMAIN>`, using
  the wildcard Secret `KIONGA_APPLICATIONS_TLS_SECRET` and the class
  `KIONGA_INGRESS_CLASS`. For `public`, the Ingress goes to the Service, or to
  the route Service when scaling is `requests`. For `sso`, the Ingress goes to
  `KIONGA_SSO_UPSTREAM` (the gateway) through an ExternalName Service
  `<name>-sso`. The annotation `kionga.dev/upstream` carries the internal URL
  that the gateway proxies to once the user has signed in. Without an
  upstream, an sso host is not published at all. The ingress controller must
  accept ExternalName backends.
- **Secrets.** `secretRefs` loads only Secrets labelled
  `kionga.dev/workload-secret=true`. Platform Secrets in the same namespace
  stay out of reach. Changing a Secret's data rolls the pods.
- **Source builds** (`build.go`). A rootless BuildKit Job
  (`KIONGA_BUILDKIT_IMAGE`) builds `source.git` and pushes the image to
  `KIONGA_BUILD_REGISTRY` with the credentials in `KIONGA_BUILD_PUSH_SECRET`.
  It writes the digest to its termination message. The Deployment then rolls
  to `<registry>/<namespace>-<name>@<digest>`. A new `spec.restartedAt`
  rebuilds the same ref. A failed build keeps the
  previous image running. Builds carry no Git credentials, so `source.git`
  must be a public `https://` URL. SSH URLs and URLs with credentials in them
  are rejected. Rootless BuildKit needs unconfined seccomp and
  AppArmor profiles, so Pod Security `baseline` and `restricted` reject the
  build pod.
- **Model deployments** (`modeldeployment_controller.go`). MLflow's
  `get-download-uri` resolves the version to an `s3://` URI. `runs:/`,
  MLflow 3 `models:/m-...` and `mlflow-artifacts:/` (through
  `KIONGA_MLFLOW_ARTIFACTS_DESTINATION`) are resolved too. The operator copies
  the keys from `KIONGA_MODEL_STORAGE_SECRET` into an owned Secret carrying the
  `serving.kserve.io/s3-*` annotations, bound through a ServiceAccount for
  KServe's storage initializer. It then creates the `InferenceService` in
  `RawDeployment` mode. Raw mode has no `canaryTrafficPercent` (KServe 0.21
  documents canary as serverless-only), so `spec.canaryVersion` becomes a
  second `InferenceService`, `<name>-canary`, and the gateway sends
  `spec.canaryWeight` percent of requests to `status.canaryURL`. Promotion only changes the MLflow stage.
- **Pipelines.** On Kubernetes the gateway runs pipeline nodes as Jobs
  (`k8sexec`). There is no pipeline custom resource.


## Agent runtime credentials

The operator creates `kionga-agent-runtime-<ID digest>` Secrets owned by each
KiongaAgent, preserving their random credentials across reconciles. The runtime
still reads `KIONGA_INTERNAL_TOKEN` with `KIONGA_RUNTIME_AUTH=required`, but the
value is never the gateway service credential. The old operator setting
`KIONGA_AGENT_RUNTIME_TOKEN_SECRET` is removed.

Existing agents gain their Secret and a changed pod template on the next
reconcile. A runtime-token hash annotation triggers a rollout after credential
replacement or regeneration of a missing key. Old pods retain their previous
environment until replaced; complete their rollout before treating the shared
credential as removed, then rotate the gateway service credential if it was
previously exposed to agent code.

The gateway resolves the named Secret on every invocation in
`KIONGA_AGENT_NAMESPACE` (default `default`), using its existing in-cluster
identity and the same named Secret GET mechanism used for workspaces. This
change adds no RBAC grants or Secret list/watch operations. Deployment RBAC lives
outside this clone: if the existing identity cannot read that named Secret in
the agent namespace, invocation returns 503; do not broaden Secret access as
part of this rollout. Missing, invalid, or unreadable credentials also return
503, with no shared-token fallback. During rollout or rotation, old and new pods
can temporarily disagree with the gateway and return 401.

For the shared Compose runtime (`AGENT_RUNTIME_URL`), set a separate
`KIONGA_AGENT_RUNTIME_TOKEN` in the gateway and the same value as
`KIONGA_INTERNAL_TOKEN` in the runtime. An unset dedicated token sends no bearer
and is rejected by runtimes requiring authentication. This shared local runtime
does not provide per-agent isolation. Runtime callbacks that previously relied
on the service token can no longer authenticate as the gateway service.
