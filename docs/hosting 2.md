# Hosting the platform on a public VM

The public deployment is the local Compose stack plus an authenticated TLS
edge. No service is swapped between environments: what runs on your laptop is
what runs on the VM.

```text
Internet ──► Caddy (443, auto Let's Encrypt TLS)
              ├── /dex/*       ──► Dex (OIDC login)
              ├── /langfuse/*  ──► Langfuse UI
              └── /*           ──► mlaiops-gateway (console + API, JWT enforced)
                                    └── all platform services (internal network only)
```

## Prerequisites

- A Linux VM (4 vCPU / 8 GB RAM minimum; 8/16 recommended, and at least 35 GB free
  Docker storage for a cold build) with Docker Engine
  and the Compose plugin installed.
- A DNS A record for your domain pointing at the VM's public IP.
- Ports 80 and 443 open in the VM firewall / cloud security group.

## Steps

1. Clone the repository on the VM and create the environment file:

   ```bash
   git clone git@github.com:ml-ai-ops/platform.git && cd platform
   cp .env.example .env
   ```

2. Fill in `.env`:
   - `MLAIOPS_DOMAIN`, `MLAIOPS_ACME_EMAIL` — your DNS name and ACME contact.
   - `DEX_CLIENT_SECRET`, `MLAIOPS_INTERNAL_TOKEN` — `openssl rand -hex 24` each.
   - `DEX_ADMIN_EMAIL` and `DEX_ADMIN_PASSWORD_HASH`:

     ```bash
     docker run --rm httpd:2.4-alpine htpasswd -bnBC 10 "" 'your-password' | tr -d ':\n'
     ```

   - LLM provider: `MLAIOPS_LLM_BACKEND=openai` (or `anthropic`) plus the API
     key. Leave `mock` to run without any provider.
   - Langfuse: set fresh `LANGFUSE_*` keys/secrets — the local defaults are
     for development only.

3. Deploy:

   ```bash
   make public-up
   ```

   The script validates configuration, builds images, starts the stack,
   creates Kafka topics, and waits for the gateway. First TLS issuance takes
   Caddy a few seconds after DNS resolves.

4. Open `https://<your-domain>` and choose **Open console**. Kionga redirects to
   Dex, validates the authorization callback, and establishes a secure HttpOnly
   browser session;
   sign in with the admin user from `.env`.

## Serverless functions (optional)

faasd is installed on the VM itself (its supported single-node mode), not in
Compose. Commands for you to run on the VM (require sudo):

```bash
# install faasd (containerd-based, ~1 minute)
git clone https://github.com/openfaas/faasd --depth=1 && cd faasd
sudo ./hack/install.sh
# credentials for the platform
sudo cat /var/lib/faasd/secrets/basic-auth-password
```

Then set in `.env` and restart the gateway:

```text
OPENFAAS_URL=http://<vm-private-ip>:8080
OPENFAAS_USER=admin
OPENFAAS_PASSWORD=<the printed password>
```

The console's Functions view then lists project-owned functions, and
`POST /api/v1/functions` deploys OCI images with limits and trigger annotations.
Function DAGs can chain these workloads; connector deployment and scale-to-zero
behavior remain OpenFaaS operator configuration.

## Build once, deploy anywhere

Every image is environment-agnostic; targets are configuration:

| Target | How |
|---|---|
| External S3 / GCS / Azure Blob | Point `S3_ENDPOINT` + credentials at the provider's S3-compatible endpoint |
| Managed PostgreSQL | Set `DATABASE_URL` (and Langfuse's) to the managed instance |
| Managed Redis | Set `REDIS_URL` on the feature gateway |
| External LLM providers | `KiongaConnection` secrets + `LLM_UPSTREAM_URL` on the trace proxy |
| Kubernetes at scale | `make kind-up` locally; `config/` holds CRDs, RBAC, network policies for the operator path |

## Operations

- Update: `git pull && make public-up` (Compose recreates changed services).
- Stop: `make public-down` (volumes persist).
- Logs: `docker compose -f deploy/compose.yaml -f deploy/compose.public.yaml logs -f gateway`.
- Backups: Postgres (control plane, MLflow, Langfuse, checkpoints) and MinIO
  volumes hold all durable state — snapshot `postgres-data` and `minio-data`.

## Roles (RBAC)

Every API request is authorized against a role, in every mode:

| Role | May do |
| --- | --- |
| `admin`, `operator` | Everything, including platform connections |
| `user` | Only provisioned services/projects/buckets and assigned resource quotas |
| `engineer` | Full ML lifecycle (projects, pipelines, models, agents, tools, features, functions) — not connections |
| `viewer` | Read-only |
| `service` | Internal reporting only (traces, run steps, materializations, realtime stats); granted by presenting `MLAIOPS_INTERNAL_TOKEN` |

- **Public (OIDC) mode:** roles come from the ID token's `roles` claim, or the
  `groups` claim when no `roles` claim exists. In Dex, put users in groups
  named after the roles (e.g. a `viewer` group for read-only stakeholders).
- Provision normal users by immutable OIDC `sub`, not a mutable display name or
  email alias. Approval, suspension, token, and quota changes remain audited.
- **Local mode:** every request acts as `MLAIOPS_LOCAL_ROLE` (default `admin`).
  Set it to `viewer` to preview the read-only console.
- `GET /api/v1/me` returns the caller's identity, roles, and effective
  permissions; the console uses it to disable controls the API would deny.

## Security notes

- The gateway enforces OIDC on every `/api/*` route in public mode
  (`OIDC_ISSUER` set) and pins CORS to your domain.
- Internal service APIs (feature gateway writes, storage proxy, serving
  manager) require `MLAIOPS_INTERNAL_TOKEN`.
- Only Caddy publishes ports; every other service is internal-network only.
- LLM provider keys live in `.env` on the VM and in process environments —
  they are never written to traces, logs, or the control-plane store.
