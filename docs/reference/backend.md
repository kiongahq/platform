# Backend contracts

Kionga's backend is a control plane: it validates intent, enforces authorization and
quotas, persists desired state and audit history, and delegates work to open-source
execution systems. The browser console, Python SDK, CLI, and internal workers all use
the same contracts.

## Request path

```text
browser / SDK / CLI
        │ authenticated HTTP + JSON
        ▼
Go gateway ──► RBAC + project/service/quota checks
        │
        ├──► PostgreSQL resource + audit + transactional outbox
        ├──► Prefect / OpenFaaS / MLflow / serving manager
        └──► Kafka lifecycle command ──► integration worker ──► Kionga CRD
```

The API never marks an external workload healthy merely because desired state was
accepted. Submission failures are persisted as failures, and connection readiness
comes from bounded active checks.

## Project contract

`GET /api/v1/project-templates` returns versioned templates. Creating a project
resolves defaults and persists:

- canonical template ID and version;
- selected framework and accelerator class;
- requested resource profile;
- capabilities and required service intent;
- namespace, owner, Git repository metadata, and scaffold command.

The backend rejects unknown templates, incompatible frameworks or accelerators, and
unknown profiles. For normal users it also requires every service named by the
template, verifies that the requested profile fits the provisioned CPU, memory, GPU,
and storage grant, and requires one GPU for `single-gpu` or at least two for
`multi-gpu`. Admin/operator principals bypass these admission limits for platform
operations. A requested profile is intent, not authority.

## Heavy training contract

Kionga separates project shape, execution resources, and physical capacity:

1. `distributed-training` declares a PyTorch DDP/multi-GPU capable project.
2. The administrator grants a GPU/custom profile and the required services.
3. Pipeline jobs declare their CPU, memory, GPU, retry, and dependency requirements.
   Admission groups jobs by dependency depth and sums each potentially concurrent
   layer; the widest CPU, memory, and GPU layer must fit the user's grant.
4. The bundled Prefect deployments execute the registered training flow and general
   container-only definitions. The Compose executor translates ready DAG layers into
   OCI jobs with declared limits, retries, parameters, dependency outputs, bounded
   logs, timeouts, and cleanup. Kubernetes remains the multi-user scale path.
5. Object storage holds checkpoints and artifacts; MLflow records experiment and
   model lineage; Git identifies the source revision.

This layering prevents a project template from silently allocating cluster capacity.
Insufficient entitlements fail at the gateway; insufficient cluster capacity remains
visible as scheduler/execution state.

Function definitions execute through configured OpenFaaS. Container definitions
execute through the bundled Prefect runner in the trusted Compose profile. Its Docker
socket is root-equivalent and therefore intentionally limited to a laptop or
single-operator VM; production distributed teams use Kubernetes-native workers/KFP
without passing the socket to user workloads.

## Pipeline graph contract

Pipeline definitions contain jobs and `depends_on` edges. Before persistence, the
gateway verifies unique job names, valid dependency references, project-owned
function references, and an acyclic graph. The validated graph is authoritative for
execution.

The console uses the open-source `@dagrejs/dagre` layout engine to position nodes and
edges. Dagre does not schedule work or infer dependencies. This distinction means a
missing browser asset can affect presentation without changing execution semantics.
The console has a compact layout fallback and continues to expose step state and logs.

Function admission applies the same principle to a single independent workload:
its CPU and memory request must fit the normal user's compute grant, in addition to
the function-count quota. The external function engine cannot be used to escape
control-plane limits.

## Agent contract

An agent deployment records a project, versioned OCI image, `module:function` graph
entry point, LLM backend, tool set, per-replica CPU/memory/GPU requirements,
minimum/maximum replicas, and canary weight. Normal-user admission checks the
aggregate capacity reserved by every owned agent at its maximum replica count,
including `100m` CPU and `128Mi` memory for each possible trace sidecar, against the
compute and workload grants. GPU type must match the assigned extended resource and
pass strict domain-qualified/non-reserved Kubernetes-name validation.

The dispatcher maps the immutable control-plane agent ID to a collision-safe,
hash-suffixed DNS name. On Kubernetes, the `KiongaAgent` controller uses it for the
Deployment and Service, applies resource requirements and health probes, and owns a
CPU-target HorizontalPodAutoscaler when maximum exceeds minimum; equal bounds are
fixed-size. It leaves `Deployment.spec.replicas` to the HPA after initial creation
and reports Ready only when at least the minimum replicas are available. The gateway
uses the same name under `MLAIOPS_AGENT_NAMESPACE`; Compose instead sets the static
`AGENT_RUNTIME_URL` shared-runtime override.

Agent list responses enrich persisted desired state with bounded live probes and an
`endpoint_url`. Invocation repeats a three-second health gate and returns `503
agent_not_ready` without forwarding when the selected runtime is unavailable. A
healthy runtime receives platform identity headers, loads the graph, restores its
Postgres checkpoint, accesses provisioned tools/features/memory, calls the LLM
through the trace proxy, and reports measured usage and session state.

Agent project scaffolds are runnable and testable before provider credentials are
added. The deterministic mock backend and evaluation fixtures provide a network-free
gate; hosted providers are runtime configuration rather than generated source.

## Persistence and delivery

Production state uses PostgreSQL. Resource mutations, audit records, and Kafka outbox
entries share a transaction. The Kafka REST lifecycle consumer disables auto-commit.
It dispatches a polled batch in order, retries a failed record up to five times with
bounded exponential backoff, and manually commits the highest successful offset plus
one per topic/partition only after the complete batch succeeds. Commit retries do not
redispatch an already successful batch. If dispatch or commit exhausts its retries,
the worker stops before polling again; restart resumes from the last durable commit.
Delivery is therefore at least once, and ID-derived create-or-update reconciliation
makes redelivery idempotent rather than creating parallel desired resources. Local
file-backed state exists only for lightweight development.

## Stable and deployment-specific interfaces

Stable platform interfaces:

- `/api/v1` JSON resources and scoped personal keys;
- S3-compatible object access;
- OCI workload images;
- Git repository and commit lineage;
- Kafka events and OpenAI-compatible LLM egress;
- `mlaiops.io/v1alpha1` Kionga resource APIs.

Deployment adapters:

- Prefect for Compose pipeline execution; KFP/Argo on Kubernetes;
- the serving manager locally; KServe on Kubernetes;
- shared Jupyter/IDE volumes in Compose; reconciled per-user workspaces on
  Kubernetes;
- external OpenFaaS/faasd for functions and event-driven microservices.

See the [REST API](api.md), [RBAC reference](rbac.md), and
[implementation status](implementation-status.md) for the exact exposed surface.
