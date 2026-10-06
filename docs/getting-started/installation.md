# Installation

Kionga runs as one Docker Compose stack. The fastest path to a working platform is
`make local-up`.

## Prerequisites

| Requirement | Why | Notes |
| --- | --- | --- |
| **Docker Engine + Compose v2** | Runs the whole stack | Docker Desktop on macOS/Windows, Docker Engine on Linux |
| **~8 GB RAM free for Docker** | The full multi-service stack | Raise Docker Desktop's memory if services get OOM-killed; 12 GB is more comfortable |
| **25–35 GB free Docker storage** | First build downloads and compiles several large images | Cached rebuilds need less; check with `docker system df` |
| **Make** | Convenience targets | Optional — you can call `docker compose` directly |
| **Go 1.25+** | Only for building Go binaries outside Docker or running Go tests | Not needed just to run the stack |
| **Python 3.11+** | Only for the SDK, tests, or docs outside Docker | Not needed just to run the stack |
| **Node.js** | Only for `make verify` (JS syntax check) | Not needed to run the stack |

!!! tip "macOS + iCloud"
    Don't keep the repository under an iCloud-managed folder (`~/Documents`,
    `~/Desktop`). iCloud can evict files and stall `git`. Clone to a plain path like
    `~/dev/mlops`.

## Fastest path: the full stack

```bash
git clone https://github.com/ml-ai-ops/platform.git
cd platform
make local-up
```

`make local-up`:

1. Verifies that Docker and Compose v2 are available and the daemon is running.
2. Downloads missing upstream images with conservative concurrency and retries
   transient registry/CDN failures such as `EOF`.
3. Reuses cached images or builds missing images, then starts every service in
   `deploy/compose.yaml`.
4. Waits for the control-plane health endpoint.
5. Creates the Kafka topics (`scripts/local-topics.sh`).

The shorter `make local` alias does the same thing. The bootstrap is idempotent: if
a download fails even after the bounded retries, run the command again and Docker
resumes from layers already cached. Tune unusual environments with
`COMPOSE_PARALLEL_LIMIT`, `KIONGA_START_RETRIES`, and
`KIONGA_START_TIMEOUT_SECONDS` without editing the Compose file.

Routine `make local-up` runs reuse cached images and build only images that are
missing. After changing application source, a Dockerfile, or an image dependency,
use `make local-rebuild`; it performs the same resilient bootstrap with explicit
image rebuilding.

The first run builds several images (Go services, MLflow, agent runtime, pipeline
runner, and Jupyter workbench). Allow roughly 10–25 minutes depending on bandwidth,
CPU architecture, and cache state. Subsequent runs are much faster.

When it finishes, open the landing page:

<http://localhost:8080>

The operational console is at <http://localhost:8080/console.html>.
Sign in with `admin` / `mlaiops-local`.

If port 8080 is already used, `make local-up` selects a free port in 18080–18084
and prints the URL. To choose a port yourself without changing the container network:

```bash
GATEWAY_PORT=18080 make local-up
export GATEWAY=http://localhost:18080
```

Bucket creation uses the `boto3` already included in the local MLflow image;
it does not pull a separate MinIO client image. A registry error that says a
repository or tag does not exist fails immediately rather than consuming all
retry attempts. On macOS, if iCloud has evicted `.dockerignore` or `Dockerfile`,
download those files in Finder before a cold build; the startup preflight will
report the affected file.

### Verify it works

```bash
./scripts/demo-smoke.sh
```

This exercises the configured platform end to end — creates a project, runs a real
pipeline, deploys a model and gets a live prediction, invokes an agent, reads
features from Redis, browses storage, and scores fraud events. Use
`GATEWAY=http://localhost:18080 ./scripts/demo-smoke.sh` when the port was
overridden. The pass/skip count is dynamic; the command must finish with zero
failures. OpenFaaS is skipped until an external gateway is configured.

### Service URLs

| Service | URL | Credentials (local defaults) |
| --- | --- | --- |
| Landing page | <http://localhost:8080> | none |
| Console + API | <http://localhost:8080/console.html> | `admin` / `mlaiops-local` locally; OIDC when hosted |
| Jupyter workbench | <http://localhost:8080/workspace.html?tool=workbench> | Kionga session |
| Browser IDE | <http://localhost:8080/workspace.html?tool=ide> | Kionga session; start with `make ide-up` |
| MLflow | <http://localhost:15000> | none |
| Prefect | <http://localhost:4200> | none |
| Langfuse | <http://localhost:3000> | `admin@local.dev` / `mlaiops-local-admin` |
| MinIO console | <http://localhost:9001> | `mlaiops` / `mlaiops-local-secret` |

See [Configuration reference](configuration.md) for every port and setting, and
[Connecting all services](../connecting-services.md) for the full connection map.

## Stopping and restarting

```bash
make local-down     # stop the stack (volumes persist)
make local-up       # bring it back
make local-status   # show every service, including completed one-shot jobs
make local-logs     # show the latest logs from the stack
make local-rebuild  # rebuild changed local source images, then start
```

Durable state lives in named volumes (`postgres-data`, `minio-data`, `redis-data`,
`prefect-data`, `jupyter-data`), so your projects, models, and notebooks survive
restarts.

## Alternative: build and run the Go control plane only

For control-plane development without the full stack:

```bash
make install        # pip installs the SDK + dev deps
make run            # runs the gateway on :8080 (file-backed store)
```

In this mode there is no execution engine, serving, or agent runtime — the gateway
uses a local JSON file (`MLAIOPS_DATA_PATH`, default `data/platform.json`) and
downstream integrations are simply not configured. Good for API and console work.

## Building the binaries

```bash
make build
```

Produces static binaries in `bin/`:

```
mlaiops-gateway  mlaiops-operator  mlaiops-integration-worker
mlaiops-trace-proxy  mlaiops-feature-gateway  mlaiops-storage-proxy
mlaiops-metrics-collector  mlaiops-serving-manager  mlaiops-cli
```

All services are also built from the root `Dockerfile` with a `SERVICE` build arg:

```bash
docker build --build-arg SERVICE=gateway -t mlaiops/gateway .
```

## Running the test suites

```bash
make verify           # Go/Python tests + vet/ruff, builds, JS check, banned-tech scan
make test-integration # Postgres outbox + pgvector round-trip
make test-e2e         # Kind-based end-to-end (optional scale path)
make test-load        # k6 load test against the gateway
```

## Public / production install

To host on a public VM behind TLS + OIDC, see **[Public hosting](../hosting.md)**.
The short version:

```bash
cp .env.example .env    # fill in domain, OIDC, secrets
make public-up
```

## Documentation site (this site)

```bash
make docs-install       # pip install mkdocs-material
make docs-serve         # live preview at http://localhost:8000
make docs-build         # static build into site/ (strict)
```
