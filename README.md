# platform

[![Platform CI](https://github.com/kiongahq/platform/actions/workflows/cicd.yml/badge.svg)](https://github.com/kiongahq/platform/actions/workflows/cicd.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENCE)

The Kionga control plane: the gateway API, the web console and the services that run behind them.

## Background

Kionga is a self-hosted platform for machine learning, data and AI agents. This
repository is its control plane. The gateway serves the REST API and the web
console, signs people in (single sign-on or local accounts), enforces roles and
access policies, stores projects, pipelines, models, agents and audit events in
PostgreSQL, schedules and dispatches pipeline runs, and proxies personal Jupyter
and IDE workspaces.

Alongside the gateway, the repository builds the Kubernetes operator, the online
feature gateway, the object storage proxy, the Kafka integration worker, the
metrics collector, the log exporter and the `mlaiops` CLI. The other parts of
Kionga live in their own repositories in the [kiongahq](https://github.com/kiongahq)
organization.

## Install

Run the whole platform from [deploy](https://github.com/kiongahq/deploy), which
clones every component side by side and builds them together:

```bash
mkdir kionga && cd kionga
git clone git@github.com:kiongahq/deploy.git
make -C deploy workspace
make -C deploy local-up
```

Released images are published as `ghcr.io/kiongahq/<service>`, for example
`ghcr.io/kiongahq/gateway`.

## Usage

Open <http://localhost:8080> and sign in with the local development account
`admin` / `mlaiops-local`. To call the API from the CLI or the
[Python SDK](https://github.com/kiongahq/sdk-python), create a personal key in
**Settings > API keys** and export it:

```bash
export MLAIOPS_TOKEN=<your key>
make build && bin/mlaiops-cli project list
```

To run the gateway alone during development, with a file-backed store and no other
services, use `make run`.

### Agent builders

For the full Langflow canvas, open **Agents → Langflow canvas** (or
`/console.html?view=langflow`). Kionga manages separate, persistent Langflow
workspaces on Kubernetes and offers an optional local Compose instance. The
native designer remains available beside it. See the
[Langflow guide](https://kiongahq.github.io/docs/guides/langflow/).

#### Native agent designer

Open **Agents → Agent designer** (or `/console.html?view=agent-designer`) to build
an agent graph inside the Kionga console. Add prompt, model, tool, knowledge base,
API call, condition, and memory nodes. Nodes can be saved on their own while a
graph is being assembled. Connect their ports, drag nodes to arrange the canvas,
or duplicate a selected node before saving a project-scoped design. Select a node
and use arrow keys to move it (Shift moves it faster). The inspector lists each
connection so it can be removed individually. The canvas reports disconnected
nodes, checks basic fields before saving, and rejects cyclic links as they are
made. The API
persists the graph in the gateway's tenant-scoped document store and checks node
identities, connections, cycles, and project access. Saves use a
revision number to reject stale edits; the 20 most recent prior revisions can
be restored in the inspector and saved as a new revision. **Export JSON**
downloads the design.

The designer currently saves graph drafts. Testing, publishing, and deploying a
draft as an executable agent are not yet connected to the agent runtime. Use
**Deploy agent** with a built OCI image and Python graph module for production
execution. Do not put credentials in node settings; reference registered platform
resources instead.

## Documentation

- Product documentation: <https://kiongahq.github.io/docs/>, starting with
  [How Kionga works](https://kiongahq.github.io/docs/concepts/how-it-works/) and the
  [REST API reference](https://kiongahq.github.io/docs/reference/api/)
- Engineering handbook: [Gateway and control plane](https://kiongahq.github.io/docs/engineering/components/gateway/)
  and [Go packages](https://kiongahq.github.io/docs/engineering/components/go-packages/)
- How this repository is laid out: [ARCHITECTURE.md](ARCHITECTURE.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, the checks CI
runs and the commit conventions.

## License

[MIT](LICENCE) © Kionga contributors
