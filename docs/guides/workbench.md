# Jupyter workbench

The workbench is a JupyterLab service that runs **inside the platform network**, so
every service is one hostname away and all credentials are already in the
environment. It also provides the **terminal** for shell-based dev work.

- **URL:** [http://localhost:8888](http://localhost:8888)
- **Token:** `mlaiops-local` (override with `JUPYTER_TOKEN`)
- **Build:** `deploy/jupyter/Dockerfile` (Python 3.11)
- **Persistence:** your work lives in the `jupyter-data` volume across restarts
- **Object store mount:** `/workspace/object-store/<bucket>`
- **AI chat:** Jupyter AI chat panel in the left sidebar
- **Coding agents:** `codex`, `claude`, and the `kionga` scaffolder in terminals

Each MinIO/S3 bucket is mounted as a directory, so notebook code can use normal
filesystem APIs:

```python
from pathlib import Path

features = Path("/workspace/object-store/mlaiops-features")
print(list(features.iterdir()))
```

Writes through these paths persist directly to object storage. Only the
workbench receives the FUSE device and `SYS_ADMIN` capability required by
S3FS; these permissions must not be copied to other platform services.

## What's preloaded

- **The platform SDK** (`mlaiops_sdk`) and the runnable modules (`pipelines`,
  `features`, `realtime`, `agents`).
- **ML libraries pinned to the serving image** (`scikit-learn==1.7.0`,
  `mlflow==3.1.1`), so a model you train in a notebook deploys to a live endpoint
  without training-serving skew.
- **Every connection, preconfigured via environment:**

  | Environment variable | Points to |
  | --- | --- |
  | `MLAIOPS_URL` | Gateway (control plane) |
  | `MLFLOW_TRACKING_URI` | MLflow |
  | `MLFLOW_S3_ENDPOINT_URL` + `AWS_*` | MinIO |
  | `MLAIOPS_FEATURE_GATEWAY_URL` | Feature gateway |
  | `KAFKA_REST_URL` | Kafka REST proxy |
  | `PREFECT_API_URL` | Prefect |
  | `DATABASE_URL` | PostgreSQL + pgvector |
  | `REDIS_URL` | Redis |
  | `LANGFUSE_HOST` + keys | Langfuse |

## The terminal

**File → New → Terminal** gives you a bash shell with the same environment, plus
`git` and `curl`. Everything the platform modules expose is available:

```bash
python -m realtime.produce --demo fraud --count 5
python -m realtime.produce --demo callcenter --count 2
curl -s "$MLAIOPS_URL/api/v1/models" | python -m json.tool
kionga agents
kionga scaffold fraud-api --template fullstack-ai --prompt "Create a fraud scoring API"
```

Use `--agent codex` or `--agent claude` to let the selected coding agent
complete the generated starter:

```bash
kionga scaffold support-agent \
  --template production-agent \
  --agent codex \
  --prompt "Build a tested support agent with retrieval and escalation"
```

The command confines the agent to the newly generated project directory.
Provider credentials come from `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`.

The five canonical project templates are discoverable through the control plane.
For heavyweight ML, create a `distributed-training` project with a GPU profile and
run its returned scaffold command. For agents, `production-agent` produces a
runnable graph/runtime and offline test/evaluation path rather than an empty stub.
See [Project templates](project-templates.md).

## Natural-language coding

Open the **Jupyter AI** chat panel from the left sidebar, select the OpenAI or
Anthropic provider, and describe the analysis or code you need. The chat can
reference notebook cells and generate code without leaving JupyterLab. For
repository-wide edits, open a terminal and run `codex`, `claude`, or
`kionga scaffold`.

## The seeded quickstart

A `quickstart.ipynb` is seeded into the workspace on first start (it never
overwrites your edits). It walks the whole platform in code:

1. **Identity & health** — `GET /me`, `GET /health`.
2. **Browse** — projects, registered models, feature views.
3. **Train & log** — a scikit-learn model logged to MLflow.
4. **Register** — the model version registered with the control plane (shows up in
   the console's Models tab).
5. **Online features** — a real lookup from Redis via the feature gateway.
6. **Real-time** — produce fraud events and read live stream stats.

## From notebook to live endpoint

Because the workbench pins match the serving image, the round trip works end to end:

```python
import os, httpx, mlflow
from sklearn.linear_model import LogisticRegression
from sklearn.datasets import make_classification

X, y = make_classification(n_samples=1200, n_features=12, n_informative=6,
                           n_clusters_per_class=1, class_sep=1.5, random_state=7)
model = LogisticRegression(max_iter=500).fit(X, y)

mlflow.set_experiment("workbench")
with mlflow.start_run():
    logged = mlflow.sklearn.log_model(model, name="model",
                                      registered_model_name="notebook-classifier")

gateway = os.environ["MLAIOPS_URL"]
version = mlflow.MlflowClient().get_latest_versions("notebook-classifier")[0].version
httpx.post(f"{gateway}/api/v1/models", json={
    "project_id": httpx.get(f"{gateway}/api/v1/projects").json()[0]["id"],
    "name": "notebook-classifier", "version": version,
    "artifact_uri": logged.model_uri, "metrics": {"accuracy": 0.9},
})
```

Then open the console's **Models** tab, deploy `notebook-classifier`, and hit it
from the predict console.

## Public deployments

In the public overlay the workbench port is **closed** — reach it over an SSH
tunnel to the VM rather than exposing 8888 to the internet. For multi-user
Kubernetes deployments, each authorized subject receives an isolated
`KiongaWorkspace` instead of this shared Compose volume.
