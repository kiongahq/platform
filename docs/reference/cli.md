# CLI reference

`mlaiops` is a single static Go binary for quick operator/engineer tasks against the
control plane. It's intentionally small — a thin, scriptable wrapper over the REST
API. For anything richer, use the [Python SDK](../modules/python.md).

The development workspaces also contain a separate `kionga` command. `mlaiops`
operates control-plane resources; `kionga` generates/synchronizes project source and
can invoke a configured coding agent inside the generated directory.

## Build

```bash
make build          # produces bin/mlaiops-cli (and every other binary)
# or
cd go && go build -o mlaiops ./cmd/cli
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `MLAIOPS_URL` | `http://localhost:8080` | Gateway base URL |
| `MLAIOPS_TOKEN` | *(unset)* | Personal API key created under Console → Settings |
| `USER` | `cli` | Sent as `X-MLAIOps-Actor` for audit attribution |

## Commands

Read commands (list resources as pretty-printed JSON):

```bash
mlaiops project list
mlaiops pipeline list
mlaiops pipeline definitions
mlaiops function list
mlaiops model list
mlaiops agent list
mlaiops tool list
mlaiops connection list
mlaiops audit list
```

Submit a pipeline for a project (runs `training-pipeline`):

```bash
mlaiops pipeline submit <project-id>
mlaiops pipeline submit <project-id> <definition-id>
```

Connect Git and manage independent functions:

```bash
mlaiops project connect <project-id> <repository-url> [branch]
mlaiops function deploy <project-id> <name> <oci-image>
MLAIOPS_FUNCTION_PAYLOAD='{"id":"evt-1"}' mlaiops function invoke <name>
MLAIOPS_FUNCTION_PAYLOAD='{"id":"evt-2"}' mlaiops function invoke-async <name>
```

## Examples

```bash
# point at a remote gateway
MLAIOPS_URL=https://platform.example.com mlaiops project list

# find a project id, then submit a run
mlaiops project list
mlaiops pipeline submit prj-1783101021841102592
```

Non-2xx responses print the error body to stderr and exit non-zero, so the CLI
composes cleanly in shell scripts.

## Workspace project commands

```bash
kionga agents
kionga templates --json
kionga project sync <project-id>
kionga scaffold vision-training --template distributed-training --agent none
kionga scaffold support-agent --template production-agent --agent codex \
  --prompt "Add retrieval, refusal, escalation, and deterministic tests"
```

Prefer the `scaffold_command` returned by project creation: it pins the canonical
template selected by the control plane. See
[Project templates](../guides/project-templates.md).

!!! tip "Auth"
    Set `MLAIOPS_TOKEN` to a scoped personal key from **Console → Settings**.
    The CLI sends it as a bearer token and the gateway enforces the same service,
    project, and quota boundaries as the console.
