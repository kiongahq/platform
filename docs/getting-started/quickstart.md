# Quickstart

This walks the full lifecycle after `make local-up`. Do it in the console, the SDK,
or the [Jupyter workbench](../guides/workbench.md) — all three drive the same API.

## 0. Confirm the stack is up

```bash
export MLAIOPS_URL="${MLAIOPS_URL:-http://localhost:8080}"
curl -s "$MLAIOPS_URL/api/v1/health"
# {"status":"ok","service":"mlaiops-gateway","version":"0.1.0"}
curl -s "$MLAIOPS_URL/api/v1/me"
# identity + roles + effective permissions
```

Open `$MLAIOPS_URL`, enter the console, and sign in locally with
`admin` / `mlaiops-local`. The local API development principal is separate from
the browser session.

## 1. Create a project

=== "Console"

    Click **＋ New project**, choose a versioned template, framework, accelerator,
    and resource profile, then create it. You land in the Projects view.

=== "SDK"

    ```python
    from mlaiops_sdk import MLAIOpsClient

    with MLAIOpsClient(base_url="http://localhost:8080",
                       actor="you@example.com") as client:
        project = client.create_project(
            "churn",
            template="production-ml",
            framework="scikit-learn",
            accelerator="cpu",
        )
        print(project.id)
    ```

=== "curl"

    ```bash
    curl -s -X POST "$MLAIOPS_URL/api/v1/projects" \
      -H 'Content-Type: application/json' \
      -d '{"name":"churn","template":"production-ml","framework":"scikit-learn","accelerator":"cpu"}'
    ```

The response includes the template version, resolved capabilities, requested
profile, and a `kionga scaffold ...` command. Browse all choices with:

```bash
curl -s "$MLAIOPS_URL/api/v1/project-templates" | python -m json.tool
```

For multi-GPU work choose `distributed-training` + `pytorch-ddp` + `multi-gpu`;
for a runnable LangGraph service choose `production-agent`. These selections record
intent but never bypass the administrator's CPU/RAM/GPU/service grants. See the
[project-template catalog](../guides/project-templates.md).

## 2. Run a real pipeline

=== "Console"

    Go to **Pipelines → ▶ Run pipeline**, choose your project and
    `training-pipeline`, submit. Watch the **DAG go green** as each step reports
    live. The open-source Dagre renderer lays out the validated backend graph; it
    does not determine execution order. The run executes a real Prefect flow:
    `validate → train → evaluate → register`.

=== "SDK"

    ```python
    run = client.submit_pipeline(project.id, name="training-pipeline")
    print(run.id, run.status)          # queued
    # poll until it completes
    run = client.get_pipeline_run(run.id)
    ```

The training step trains a real scikit-learn model (deterministic, fixed
`random_state`), logs it to MLflow, and registers it with the control plane against
your project.

## 3. Promote and deploy the model

=== "Console"

    Open **Models**. Your `churn-classifier` version is listed with its accuracy and
    quality gate. Click **Promote**, then **Deploy** — the serving-manager starts a
    live `mlflow models serve` container and the card shows a green **● live** tag.

=== "SDK"

    ```python
    models = client.list_models()
    model = models[0]
    client.promote_model(model.id, stage="production")
    client.deploy_model(model.id, canary_weight=0)
    ```

## 4. Get a live prediction

=== "Console"

    On the live model card (or the Storage → endpoints table), click **Test**, edit
    the payload, and **Send prediction request**. You get a real response from the
    serving container:

    ```json
    {"predictions": [1]}
    ```

=== "curl"

    ```bash
    curl -s -X POST "$MLAIOPS_URL/api/v1/models/<model-id>/predict" \
      -H 'Content-Type: application/json' \
      -d '{"inputs": [[0.1,-1.2,0.5,2.0,0.3,-0.7,1.1,0.0,-0.4,0.9,-1.5,0.2]]}'
    ```

## 5. Talk to an agent

The stack ships a customer-support agent (`agents.customer_support.graph:build`).

=== "Console"

    Open **Agents**, click **Chat** on the customer-support agent, and ask a
    question. The turn runs through the real agent runtime; the reply, token count,
    and latency are shown, and the **Session Monitor** records the session.

=== "SDK"

    ```python
    agents = client.list_agents()
    reply = client.invoke_agent(agents[0].id, message="When are invoices issued?")
    print(reply)
    ```

!!! note "Mock vs real LLM"
    By default the agent uses the `mock` backend, so it runs with **zero API keys**
    (replies are prefixed `[mock]`). To get real answers, set `MLAIOPS_LLM_BACKEND`
    and a provider key in `.env` — see [Configuration](configuration.md#llm-providers).

## 6. Explore features, storage, and real-time

- **Features:** the Feature Catalog shows `customer_profile` and
  `transaction_stats_5m` with online entity counts (served from Redis).
- **Storage:** browse MinIO buckets (models, artifacts, features, …) and preview
  objects.
- **Real-time:** produce demo events and watch them scored live —

    ```bash
    docker compose -f deploy/compose.yaml exec jupyter \
      python -m realtime.produce --demo fraud --count 5
    ```

    Then open the **Real-Time** panel to see throughput, latency, and flagged events.

## 7. Develop interactively

Open the **Jupyter workbench** at <http://localhost:8888> (token `mlaiops-local`).
It has a browser terminal (File → New → Terminal) and a seeded `quickstart.ipynb`
that reproduces everything above in code, with every connection preconfigured. See
the [workbench guide](../guides/workbench.md).

## 8. Add Git, functions, and reusable flows

Connect a project to an HTTPS/SSH Git remote from its detail panel. Open
**Pipelines → Define flow** to persist a versioned container or function DAG with
resources, retries, parameters, and commit lineage. Functions become deployable and
invocable after an administrator configures `OPENFAAS_URL`; until then the console
correctly reports that serverless is not configured.

For a distributed team, an administrator provisions each OIDC subject under
**Users & access**, choosing services and a starter/team/power/GPU/custom resource
profile. Compose provides the shared workbench; Kubernetes reconciles those grants
into isolated `KiongaWorkspace` workloads.

## Where to go next

- [Connecting all services](../connecting-services.md) — the full connection map.
- [Models & serving](../guides/models-serving.md), [Agents](../guides/agents.md),
  [Feature store](../guides/features.md), [Real-time](../guides/realtime.md).
- [REST API reference](../reference/api.md) and [CLI](../reference/cli.md).
- [Implementation status](../reference/implementation-status.md) — bundled versus
  optional, external, and scale-path capabilities.
