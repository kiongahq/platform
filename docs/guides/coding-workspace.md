# AI coding and IDE workspace

Kionga treats `/workspace` as the primary development surface. JupyterLab and
the optional browser IDE mount the same persistent volume, so a project created
in either interface appears immediately in the other.

## Start the IDE

```bash
make ide-up
```

Sign in to the console, then choose **IDE workspace**. It opens through the
authenticated gateway at `/workspace.html?tool=ide`; no separate password or
published IDE port is needed in Compose. The link explains when the IDE is
offline. Select a project first to open its shared folder. The workspace toolbar
switches between Jupyter and the IDE and takes you back to the console.
Stop it without deleting projects:

```bash
make ide-down
```

The IDE is code-server, the open-source browser build of VS Code. It is an
opt-in Compose profile and is not exposed by the public deployment overlay.

## Generate a starter

Run this in either the Jupyter terminal or the IDE terminal:

```bash
kionga scaffold churn-service \
  --template production-ml \
  --agent codex \
  --prompt "Train, evaluate, and expose a churn classifier with tests"
```

Canonical templates are `production-ml`, `distributed-training`,
`production-agent`, `fullstack-ai`, and `blank-python`. Use `--agent none` for a
deterministic runnable starter without an LLM, or choose `codex` or `claude`. The
[template catalog](project-templates.md) lists supported frameworks, accelerators,
required services, and generated boundaries.

Other command-line agents can be connected without changing Kionga:

```bash
export KIONGA_CUSTOM_AGENT_COMMAND="your-agent --non-interactive"
kionga scaffold experiment --template production-ml --agent custom \
  --prompt "Build a reproducible classification experiment"
```

Kionga appends the task as the final command argument and runs it from the new
project directory. Only configure agents whose permission model you understand.

```bash
kionga agents
```

shows whether each CLI and its API credential are available.

For substantial workloads, create the project through the API/console first and
run the returned `scaffold_command`. This preserves template version, framework,
accelerator, resource-profile intent, capabilities, and Git lineage in control-plane
state instead of leaving those decisions only in generated files.

## Direct agent use

Pinned Codex and Claude Code CLIs are installed in both development surfaces:

```bash
codex
claude
```

For unattended scaffolding, Kionga runs Codex with workspace-write sandboxing.
Claude Code starts in the generated project with edit acceptance. Review agent
changes and tests before committing.

## Security boundary

- Agent API keys are injected from `.env`; they are not written into projects.
- Agent configuration volumes persist login/config state separately from code.
- The IDE has password authentication and binds only to the configured local
  port. Never expose it directly to the internet.
- Jupyter and the IDE share source files, but only Jupyter receives FUSE and
  `SYS_ADMIN` for the object-store mount.

For production multi-user deployments, replace the single shared code-server
container with isolated per-user `KiongaWorkspace` pods. A GPU or custom resource
profile bounds the workspace; pipeline job requests are enforced separately.
