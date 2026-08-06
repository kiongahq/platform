# Kionga — the ml-ai-ops-platform

Kionga is a **self-hosted platform that brings the whole AI lifecycle together** —
classical ML, data-centric AI, and agentic AI — behind one control plane and one
web console. Its bundled application services run together with Docker Compose on
a laptop or single VM; Kubernetes adds the isolated, multi-user scale path.

<div class="grid cards" markdown>

- :material-rocket-launch: **[Get started](getting-started/installation.md)**

    One command (`make local-up`) brings up the full stack. Then walk the
    [quickstart](getting-started/quickstart.md).

- :material-sitemap: **[Architecture](overview/architecture.md)**

    How the Go control plane, Python workloads, and the data plane fit together.

- :material-server: **[Services](services/index.md)**

    Every container in the stack, its port, its job, and how it connects.

- :material-package-variant: **[Modules](modules/go-packages.md)**

    The Go packages and Python SDK/workloads that make it all work.

- :material-api: **[REST API](reference/api.md)**

    The full control-plane surface — every endpoint, by resource.

- :material-shield-key: **[RBAC & security](reference/rbac.md)**

    Roles, permissions, tokens, and how authorization is enforced everywhere.

</div>

## What you can do with it

Run `make local-up`, open the landing page at <http://localhost:8080>, then sign in
to the console with `admin` / `mlaiops-local`. If 8080 is occupied, use
`GATEWAY_PORT=18080 make local-up`. The bundled lifecycle includes:

- **Create a production-shaped project** from a versioned catalog: classical ML,
  distributed PyTorch training, a functional LangGraph agent, a full-stack AI
  application, or a blank expert workspace.
- **Provision authenticated users** with explicit services, projects, storage,
  CPU, memory, GPU, function, and concurrency limits.
- **Connect every project to Git**, preserve repository/branch/commit lineage, and
  scaffold full-stack ML/API starters from Jupyter or the browser IDE.
- **Run the bundled training pipeline or a general container DAG** and watch its real
  Prefect steps update live in an open-source Dagre-rendered graph; execute function
  DAGs through OpenFaaS and use Kubernetes-native execution for multi-user isolation.
- **Register and promote a model**, deploy it to a **live inference endpoint**, and
  hit it from the built-in test console.
- **Deploy a LangGraph agent** that answers via a real LLM, keeps session state in
  Postgres, retrieves features and long-term memory, emits full traces, and carries
  enforceable CPU/RAM/GPU and fixed-or-autoscaled replica intent.
- **Build an agent project** with runnable graph/runtime code, typed tools,
  deterministic evaluation fixtures, containers, and Git-native delivery.
- **Browse features, artifacts, prompts, and live cost** — all real data.
- **Score events in real time** — fraud detection, call-center analysis, and
  personalized recommendations demos, streaming through Kafka.
- **Develop interactively** in a built-in [Jupyter workbench](guides/workbench.md)
  with a browser terminal, wired into every service.
- **Deploy independent OCI functions** and compose them into reusable DAGs when
  an OpenFaaS gateway is configured.
- **Put the same stack on a public VM** behind TLS + OIDC with one script.

## Design principles

| Principle | What it means in practice |
| --- | --- |
| **One application stack** | The public VM uses the Compose application services plus a Caddy/Dex edge; Kubernetes is the separate distributed-team scale path. |
| **Functional, not simulated** | Bundled pipelines execute, models serve live, agents run through a real runtime, and features come from Redis. External capabilities report `not configured` until connected. |
| **Compose-native** | Kubernetes-only tools (KServe, KFP, Istio) are fulfilled by Docker-friendly equivalents (mlflow-serve, Prefect, Caddy). Kubernetes stays a documented scale path. |
| **Honest state** | Nothing is green by wishful thinking — every connection is actively health-checked; costs and tokens are measured, never estimated. |
| **Secure by default in public** | OIDC on every route, RBAC on every request, secrets in `.env`, LLM keys never written to traces or logs. |

## How this documentation is organized

- **[Overview](overview/what-is-kionga.md)** — what Kionga is, its architecture, the
  concepts, and the exact technology used.
- **[Project templates](guides/project-templates.md)** — production ML, distributed
  training, agent, full-stack, and expert starter contracts.
- **[Getting started](getting-started/installation.md)** — install, quickstart, and
  the complete configuration reference.
- **[Services](services/index.md)** — a reference for every container in the stack.
- **[Modules](modules/go-packages.md)** — the Go packages and Python modules.
- **[Guides](connecting-services.md)** — task-focused walkthroughs (connecting
  services, the workbench, pipelines, serving, agents, features, real-time).
- **[Reference](reference/api.md)** — the REST API, RBAC, and CLI.
- **[Backend contracts](reference/backend.md)** — validation, persistence,
  orchestration, graph, and agent execution semantics.
- **[Implementation status](reference/implementation-status.md)** — exactly which
  capabilities are bundled, optional, external, or Kubernetes-only.
- **[Operations](operations/local.md)** — running locally, public hosting, and
  troubleshooting.

!!! note "Local credentials are development defaults"
    Everything in this documentation that shows a password, key, or token
    (`mlaiops-local`, `pk-lf-local-dev`, etc.) is a **development default**.
    Override every one of them for any public deployment — see
    [Public hosting](hosting.md).
