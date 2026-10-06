# Moving between projects and services

The console uses one project selector across projects, pipelines, functions,
models, agents, and workspace launches. Choose **All assigned projects** to
clear the filter. Shared infrastructure, feature definitions, and the catalog
remain platform-wide within your grants.

Each section is bookmarkable: `/console.html?view=pipelines&project=PROJECT_ID`.
Project, run, model, agent, and feature details add a `resource` parameter.
Refreshing and browser Back/Forward restore the view and project. Project cards
offer direct links to their runs, functions, models, agents, Jupyter, and IDE.

An individual service grant is sufficient to load that service's page. Related
panels appear only with the corresponding grants. Project selectors use a
minimal metadata endpoint limited to assigned or owned projects. A failed page
shows a persistent error and Retry button without preventing other navigation.
An expired session returns to sign-in with the current console URL preserved.

## Developer workspaces

Choose Jupyter or IDE in the sidebar, settings, or project details. The gateway
checks availability and authorization. The workspace toolbar preserves the
project while switching tools; both tools use the same persistent storage.
Project launches prepare `projects/<project-namespace>` before opening the
corresponding directory. Jupyter uses its contents API; IDE-only workspaces use
a small directory helper reached through code-server's authenticated loopback
proxy. Either service can prepare the folder independently.

Compose uses an authenticated gateway proxy. Jupyter's diagnostic port is
loopback-only; code-server has no published port. Platform cookies and API keys
are stripped before forwarding. The gateway injects the upstream credential.
Workspace access is checked on every HTTP request and WebSocket handshake.
Existing upgraded WebSocket connections end when closed by the client or server;
revoking a grant blocks new requests but does not retroactively close a socket.

On Kubernetes, `KIONGA_WORKSPACE_NAMESPACE` enables discovery of the requesting
subject's KiongaWorkspace, its internal Service, and `<workspace-name>-auth`
Secret. Apply `config/rbac/gateway-workspaces.yaml` together with the deployment
manifests. Change its Role/RoleBinding namespace when changing the workspace
namespace. The gateway needs read access to workspace CRs and their credentials.
Hosted users are never routed to the shared local administrator workspace.

Custom upstream templates can contain `{subject}`, expanded to the subject's
SHA-256 hex digest. Prefer Kubernetes discovery when using the bundled operator.
The deployment must provision isolated upstreams and use matching Jupyter base
paths. Use a separate, authenticated workspace origin for deployments executing
untrusted browser content; the bundled proxy shares the console's origin.

## Connections and execution

Connection checks accept only successful HTTP responses. Redirects, missing
routes, and authentication failures are shown as failures. Use a Prefect API
base address or an OpenFaaS gateway address; other connection types accept a
health endpoint. Credential references are `none` or `env:VARIABLE` on the
gateway. Prefect uses a bearer credential; OpenFaaS uses its gateway password
and `OPENFAAS_USER` (default `admin`). Secret values never appear in API responses.

After testing, **Use for new operations** persists the selected Prefect or
OpenFaaS connection. Subsequent execution uses that stored address and credential
without restarting the gateway. Switching is rejected while pipelines are queued
or running, to avoid changing their execution destination. The most recently
activated connection of each type wins. Workers and deployments must already be
registered with the chosen engine. Other connection cards are explicitly labeled
availability monitors; their runtime configuration remains deployment-managed.

## Local authentication and automation

Both console files and API operations require authentication. Local login uses
`MLAIOPS_LOCAL_USERNAME` / `MLAIOPS_LOCAL_PASSWORD`; the username is the grant
subject. Logging out invalidates that local session, including API calls.
Personal SDK/CLI use requires a scoped API key from Settings, passed through
`MLAIOPS_TOKEN`. Workers use the separate `MLAIOPS_INTERNAL_TOKEN` credential.
Compose supplies it to reporting services, not to interactive workspaces.
`scripts/demo-smoke.sh` accepts a personal key or signs in with local credentials.

## Verification

`make test-ui` installs pinned test dependencies and exercises the real console
HTML/JavaScript using jsdom: individual grants, routing, project preservation,
errors, retry, and workspace availability. Go tests cover session revocation,
workspace authorization/proxying, HTTP checks, and persisted runtime activation.
