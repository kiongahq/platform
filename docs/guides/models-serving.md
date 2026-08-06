# Models & serving

The classical-ML lifecycle, made real: register a model, gate it, promote it, deploy
it to a **live REST endpoint**, split traffic with a canary, and roll back.

Shell examples assume `export MLAIOPS_URL=http://localhost:8080`.

## Registry

Models are registered against a project — usually automatically by the training
pipeline's `register` step, or manually:

```bash
curl -s -X POST "$MLAIOPS_URL/api/v1/models" \
  -H 'Content-Type: application/json' \
  -d '{"project_id":"<id>","name":"churn-classifier","version":"1",
       "artifact_uri":"models:/m-...",
       "serving_image":"ghcr.io/acme/churn-sklearn@sha256:<digest>",
       "metrics":{"accuracy":0.968}}'
```

Each model carries `metrics`, a **quality gate** status, a **stage**, and (once
deployed) an `endpoint_url` and `deployment_status`. The console's Models tab shows
a per-version quality bar chart and cards with the gate, stage, and metrics.

## Promotion

Move a model between stages (e.g. `candidate` → `production`):

```bash
curl -s -X POST "$MLAIOPS_URL/api/v1/models/<id>/promote" \
  -d '{"stage":"production"}'
```

## Deploying to a live endpoint

Deployment is **real** — the serving-manager launches an `mlflow models serve`
container for the model version and records the endpoint URL:

```bash
curl -s -X POST "$MLAIOPS_URL/api/v1/models/<id>/deploy" \
  -d '{"canary_weight":0}'
```

1. The gateway calls the serving-manager (`POST /deployments`).
2. The manager starts the container on the platform network and returns the URL
   (e.g. `http://mlaiops-serve-churn-classifier:5001`).
3. The gateway records it and marks the model `serving` — or, if the manager
   rejects it, marks it `failed` (fail-closed, `502`).

The model card then shows a green **● live** tag and a **Test** button.

## Predicting

Hit the live endpoint through the gateway (it proxies to `/invocations`):

=== "Console"

    Click **Test** on a live model, edit the payload, **Send prediction request**:

    ```json
    {"predictions": [1]}
    ```

=== "API"

    ```bash
    curl -s -X POST "$MLAIOPS_URL/api/v1/models/<id>/predict" \
      -H 'Content-Type: application/json' \
      -d '{"inputs": [[0.1,-1.2,0.5,2.0,0.3,-0.7,1.1,0.0,-0.4,0.9,-1.5,0.2]]}'
    ```

## Canary & rollback

- **Canary weight** (on deploy, or via agent traffic for agents) splits traffic
  between versions behind the edge router.
- **Rollback** removes the serving container and reverts:

    ```bash
    curl -s -X POST "$MLAIOPS_URL/api/v1/models/<id>/rollback" -d '{}'
    ```

## Avoiding training-serving skew

A model trained under one library or framework version can fail to load under
another. Register `serving_image` with each model version so XGBoost, PyTorch, and
other heavyweight artifacts run in an OCI image containing the same dependencies
used for training. Prefer an immutable digest. The image must contain the `mlflow`
CLI and artifact-store support because the manager runs `mlflow models serve`
inside it. If the field is absent, `SERVE_IMAGE` remains the compatible default for
the bundled scikit-learn path.

Serving images run with the platform's model-store credentials. Treat the registry
and allowed image publishers as part of the deployment trust boundary; container
capability dropping, `no-new-privileges`, and the PID limit reduce runtime privilege
but do not make an untrusted image safe.

## Kubernetes fidelity path

On the scale path, serving integrates with KServe/Knative and the operator reconciles a
`KiongaModelPromotion`. Locally and on a single VM, mlflow-serve containers provide
the same control-plane lifecycle without Kubernetes.
