# Local operations

Day-to-day commands for running, verifying, and inspecting the local stack.

## Lifecycle

```bash
make local-up       # build + start everything, create Kafka topics
make local-down     # stop (named volumes persist)
make local-status   # inspect running, unhealthy, and completed services
make local-logs     # print the most recent stack logs
make local-rebuild  # rebuild Dockerized source/dependency changes, then start
make ide-up         # optional browser IDE sharing Jupyter's workspace
make ide-down       # stop the IDE without deleting the workspace
```

Restarting is idempotent — `make local-up` reconciles to the desired state and only
rebuilds changed images. It also caps concurrent downloads, retries transient image
registry failures, waits for the gateway, and creates Kafka topics. `make local` is
an equivalent shorter alias. Durable state survives in the `postgres-data`,
`minio-data`, `redis-data`, `prefect-data`, and `jupyter-data` volumes.

## Status & health

```bash
export GATEWAY="${GATEWAY:-http://localhost:8080}"
docker compose -f deploy/compose.yaml ps                    # per-service state
curl -s "$GATEWAY/api/v1/health"                            # gateway
curl -s "$GATEWAY/api/v1/components" | python -m json.tool # component grid
```

## Logs

```bash
docker compose -f deploy/compose.yaml logs -f gateway
docker compose -f deploy/compose.yaml logs -f realtime-processor
docker compose -f deploy/compose.yaml logs --tail 100 serving-manager
```

## Rebuilding one service

After editing code for a single service:

```bash
docker compose -f deploy/compose.yaml up -d --build gateway
```

The console (HTML/JS/CSS) is embedded in the gateway binary, so console changes need
a gateway rebuild.

## Re-running one-shot jobs

```bash
docker compose -f deploy/compose.yaml up feature-materializer   # re-materialize features
docker compose -f deploy/compose.yaml up minio-init             # ensure buckets
```

## Verifying the whole platform

```bash
GATEWAY="$GATEWAY" ./scripts/demo-smoke.sh # configured end-to-end checks
make verify                      # gate suite (Go+Python tests, lint, build, banned-tech)
make test-integration            # Postgres outbox + pgvector round-trip
```

The pass/skip count varies with optional services. The required invariant is zero
failures; OpenFaaS is skipped when it is not configured.

## Kubernetes fidelity stack

`make kind-up` installs the Kionga CRDs/controllers and loads the locally built
images. It also installs metrics-server with the Kind-specific kubelet TLS flag,
so elastic `KiongaAgent` deployments have a working resource-metrics API for their
CPU HorizontalPodAutoscalers. Run `make test-e2e` after bootstrap to exercise the
agent and workspace reconcilers.

## Changing ports

Every published port has a `*_PORT` override. Put them in `.env` (or export them),
then `make local-up`. If 8080 is occupied and no gateway override is set, the
script chooses a free port in 18080–18084 and prints it. Example — explicitly
move the console off 8080:

```bash
echo "GATEWAY_PORT=8090" >> .env
make local-up
export GATEWAY=http://localhost:8090
```

## Switching your local role

Preview the console as a non-admin without OIDC:

```bash
MLAIOPS_LOCAL_ROLE=viewer docker compose -f deploy/compose.yaml up -d gateway
# ... then restore:
docker compose -f deploy/compose.yaml up -d gateway
```

## Data & backups

All durable state is in Postgres (control plane, MLflow, Langfuse, checkpoints) and
MinIO (artifacts). To back up locally, snapshot those two volumes:

```bash
docker run --rm -v mlaiops_postgres-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/postgres-data.tgz -C /data .
docker run --rm -v mlaiops_minio-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/minio-data.tgz -C /data .
```

## Resetting

To wipe all state and start clean (destructive):

```bash
docker compose -f deploy/compose.yaml down -v   # -v removes the named volumes
make local-up
```

Do not add `-v` during routine restarts. It permanently deletes platform databases,
artifacts, models, notebooks, and IDE workspace data.

## Building the docs site

```bash
make docs-install     # once
make docs-serve       # live preview at http://localhost:8000
make docs-build       # strict static build into site/
```
