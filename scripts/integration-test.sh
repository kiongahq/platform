#!/usr/bin/env bash
# PostgreSQL integration tests against a throwaway pgvector container.
set -euo pipefail

PORT="${TEST_POSTGRES_PORT:-55432}"
NAME="kionga-integration-postgres-$$"
docker run -d --rm --name "$NAME" -p "$PORT:5432" \
  -e POSTGRES_USER=mlaiops -e POSTGRES_PASSWORD=mlaiops-local -e POSTGRES_DB=mlaiops \
  pgvector/pgvector:pg16 >/dev/null
cleanup() { docker stop "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
for _ in $(seq 1 30); do
  docker exec "$NAME" pg_isready -U mlaiops >/dev/null 2>&1 && break
  sleep 1
done

export TEST_DATABASE_URL="postgres://mlaiops:mlaiops-local@localhost:${PORT}/mlaiops?sslmode=disable"
(cd "$(dirname "$0")/../go" && go test -buildvcs=false -tags=integration -v ./integration)
