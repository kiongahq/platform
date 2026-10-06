# Hugging Face models in Kionga

Use **Models → Hugging Face Hub** to find pretrained models for classification,
embeddings, text generation, vision, speech, and other ML/AI workloads. Hub models
can be the starting point for notebooks, training pipelines, or agent tools.
Importing a model does not automatically make it a deployed inference service.

## 1. Connect your account

Public search and model registration work without a Hugging Face account.
For private repositories, connect your personal account in **Settings → Hugging
Face account**. Create a read or fine-grained token at
[Hugging Face token settings](https://huggingface.co/settings/tokens), grant only
the repository access you need, and paste it into the password field.

The gateway validates the token against the Hub identity endpoint. It encrypts
the token with AES-256-GCM, bound to your platform subject, before persisting it
in the existing file/PostgreSQL repository. It never returns the token in API
responses, puts it in browser storage, or includes it in generated code or audit
events. The token is sent only to `https://huggingface.co`, without following
redirects. Use HTTPS for the platform outside loopback development.

An administrator must first set `KIONGA_CREDENTIAL_KEY` on the gateway to a
base64-encoded 32-byte random key. Generate one with:

```sh
openssl rand -base64 32
```

For Compose, put the result in your uncommitted root `.env`, then recreate the
gateway. In Kubernetes, inject it using a Secret. Keep this key stable across
gateway replicas and restarts, back it up separately from the database, and
never commit it. Changing it requires users to reconnect; automated re-encryption
and managed-KMS rotation are not implemented. Without the key, private account
connections are disabled; public discovery still works.

**Disconnect** forgets the platform's saved credential. It does not revoke the
token on Hugging Face or log out any notebook/IDE. Revoke a compromised token on
Hugging Face and remove local Hub logins separately.

## 2. Find and register a model

1. Search by model/organization and optionally filter by task.
2. Open the model card and review license, required libraries, memory needs,
   intended uses, and limitations. A download count is not a quality guarantee.
3. Choose an assigned project and a branch, tag, or commit (default `main`).
4. Click **Register in project**.

Kionga resolves the revision to its immutable commit and stores an artifact URI
such as `hf://organization/model@<40-character-commit>`, plus repository, task,
library, and license metadata when available. The model starts as a candidate
with `needs_evaluation`; its weights have **not** been downloaded. Repeat imports
of the same repository/commit into the same project are rejected as duplicates.
Different projects can register the same source independently.

Users need the **models** service and access to the destination project. Viewers
can browse but cannot import. Private metadata registered in a project is visible
to that project's authorized members; their own Hub accounts still need permission
to download the underlying weights.

## 3. Pull weights in Jupyter, the IDE, or a local environment

After import, copy the generated Python code or download the starter notebook.
**Open Jupyter** preserves the destination project. Upload/open the notebook in
that workspace; Kionga does not silently execute it.

The rebuilt Jupyter image includes the SDK's `huggingface` extra. For an IDE or
local checkout, create an environment and install the SDK:

```sh
python -m venv .venv
source .venv/bin/activate
pip install './python[huggingface]'
```

Run this from the repository root; adjust the path when working elsewhere.
For private/gated models, authenticate **inside that environment**:

```python
from mlaiops_sdk.huggingface import connect
connect()  # Interactive official Hub login; do not paste a token into saved code.
```

This local login is deliberately separate from the encrypted console connection:
Kionga does not export the saved account credential to workspaces or API clients.
The official Hub client caches local credentials under the current OS user;
only use this on your personal workspace. In jobs, inject `HF_TOKEN` with your
deployment's secret manager, not a pipeline parameter or committed file.

An SDK workflow can register and pull a model directly:

```python
import os
from mlaiops_sdk import MLAIOpsClient
from mlaiops_sdk.huggingface import download_model

with MLAIOpsClient(os.environ["MLAIOPS_URL"]) as platform:
    # MLAIOPS_TOKEN is your scoped Kionga API key; it is not your HF_TOKEN.
    model = platform.import_huggingface_model(
        project_id="YOUR_ASSIGNED_PROJECT_ID",
        repo_id="sentence-transformers/all-MiniLM-L6-v2",
        revision="main",
    )
    path = download_model(model, cache_dir="/workspace/huggingface-cache")
    print(path)
```

`download_model` uses the official `snapshot_download` function with the registry's
exact commit. It returns the cache directory containing the snapshot; interrupted
downloads can be retried and cached files reused. Optional `allow_patterns` limits
the files pulled, and `local_files_only=True` supports offline reuse. Filtering
out required tokenizer/config/weight files can make a model unusable.

Provision enough disk before pulling large checkpoints. Downloads execute in the
calling workspace/job, subject to that environment's filesystem and resource
limits—not the gateway. There is no gateway background download queue or automatic
size-based quota reservation in this integration.

## 4. Build, evaluate, and deploy

Install the model's compatible runtime library in your project environment
(`transformers`, `sentence-transformers`, `diffusers`, etc.). Load from the returned
snapshot path. The download helper never executes repository Python and never
enables `trust_remote_code`; assess any model requiring custom code separately.

Use embeddings in retrieval tools, add an inference call to an agent's graph,
or fine-tune the checkpoint in a pipeline. These application integrations are
project code, not automatically generated deployments. Hub downloads alone do
not provide an LLM backend or guarantee CPU/GPU compatibility.

Evaluate on your own data and register the evaluated artifact with metrics and
an appropriate serving image through the normal model workflow. A raw imported
Hub reference cannot be promoted to production or deployed through the default
MLflow serving path. Preserve the source commit in your experiment and artifact
metadata. Select a runtime/image that understands the artifact format; large
generative models may require a dedicated GPU inference server.

## Troubleshooting

- **401/403 or gated access:** accept the model's terms/request approval on its
  Hub page, then use a token from the approved account. Registration of metadata
  is not proof that weights are downloadable.
- **404:** verify the repository/revision and your personal account's access.
- **429:** wait and retry; the console reports rate limiting explicitly.
- **Credential cannot be decrypted:** an administrator may have changed the key;
  reconnect your account.
- **Download fails:** check workspace connectivity, disk capacity, local Hub
  credentials, and the model's required access. Detailed download errors come
  from the official Hub client in your notebook/terminal.

Reference: [Hub downloads](https://huggingface.co/docs/huggingface_hub/guides/download),
[access tokens](https://huggingface.co/docs/hub/security-tokens),
[gated models](https://huggingface.co/docs/hub/models-gated).
