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

One directory per binary; `make build` writes `bin/mlaiops-<name>`.

| Command | Role |
| --- | --- |
| `gateway` | REST API, console, scheduler, log retention, blog publisher; port `8080` (`PORT`), internal workspace listener on `8087` |
| `operator` | Kubernetes controllers for agents, pipeline runs, model promotions and workspaces |
| `integration-worker` | Consumes lifecycle commands from Kafka REST and applies them as CRDs |
| `feature-gateway` | Online feature reads and writes over Redis or Feast; port `8083` |
| `storage-proxy` | SigV4 presigned URLs and bucket browsing; port `8084` |
| `metrics-collector` | Prometheus metrics for component health and platform usage; port `9090` |
| `log-exporter` | Ships one tenant's logs to Elasticsearch or OpenSearch |
| `cli` | The `mlaiops` CLI |

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
- `integrations`: clients for Prefect, MLflow, KFP, Langfuse, OpenFaaS and Kafka
  REST; `lifecycle_worker.go` is the at-least-once Kafka boundary.
- `operator`: reconcilers behind the `operator` command.
- `feature`, `storage`, `metrics`, `logexport`: the logic behind the matching
  commands.
- `editorial`: the engineering blog's blocks, sanitized rendering, workflow and
  media.
- `platform`: the component health grid and discovery catalog, derived from live
  state.
- `redact`: removes secrets from text before it is stored or exported.
- `runtimeconfig`: loads `KIONGA_CONFIG_FILE` and `<NAME>_FILE` secrets, and the
  production start-up checks.

### `go/pkg`

- `api`: wire types shared by the gateway, CLI and clients, including the versioned
  project template catalog (`project_templates.go`).
- `kube/v1alpha1`: CRD types in group `mlaiops.io` (`KiongaAgent`,
  `KiongaPipelineRun`, `KiongaModelPromotion`, `KiongaWorkspace`).

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
- **Banned technologies.** CI fails on any reference to excluded products.
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
- **Naming.** Binaries, environment variables and the CRD group keep the `mlaiops`
  prefix; newer settings use `KIONGA_`.


## Agent runtime credentials

The operator creates `kionga-agent-runtime-<ID digest>` Secrets owned by each
KiongaAgent, preserving their random credentials across reconciles. The runtime
still reads `MLAIOPS_INTERNAL_TOKEN` with `MLAIOPS_RUNTIME_AUTH=required`, but the
value is never the gateway service credential. The old operator setting
`KIONGA_AGENT_RUNTIME_TOKEN_SECRET` is removed.

Existing agents gain their Secret and a changed pod template on the next
reconcile. A runtime-token hash annotation triggers a rollout after credential
replacement or regeneration of a missing key. Old pods retain their previous
environment until replaced; complete their rollout before treating the shared
credential as removed, then rotate the gateway service credential if it was
previously exposed to agent code.

The gateway resolves the named Secret on every invocation in
`MLAIOPS_AGENT_NAMESPACE` (default `default`), using its existing in-cluster
identity and the same named Secret GET mechanism used for workspaces. This
change adds no RBAC grants or Secret list/watch operations. Deployment RBAC lives
outside this clone: if the existing identity cannot read that named Secret in
the agent namespace, invocation returns 503; do not broaden Secret access as
part of this rollout. Missing, invalid, or unreadable credentials also return
503, with no shared-token fallback. During rollout or rotation, old and new pods
can temporarily disagree with the gateway and return 401.

For the shared Compose runtime (`AGENT_RUNTIME_URL`), set a separate
`KIONGA_AGENT_RUNTIME_TOKEN` in the gateway and the same value as
`MLAIOPS_INTERNAL_TOKEN` in the runtime. An unset dedicated token sends no bearer
and is rejected by runtimes requiring authentication. This shared local runtime
does not provide per-agent isolation. Runtime callbacks that previously relied
on the service token can no longer authenticate as the gateway service.
