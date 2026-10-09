#!/usr/bin/env bash
set -euo pipefail

export POSTGRES_PORT="${TEST_POSTGRES_PORT:-55432}"
compose=(docker compose -p mlaiops-test -f deploy/compose.yaml)
"${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
cleanup() { "${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT
"${compose[@]}" up -d postgres
for _ in $(seq 1 30); do
  "${compose[@]}" exec -T postgres pg_isready -U mlaiops && break
  sleep 1
done

export TEST_DATABASE_URL="postgres://mlaiops:mlaiops-local@localhost:${POSTGRES_PORT}/mlaiops?sslmode=disable"
(cd go && go test -buildvcs=false -tags=integration -v ./integration)

# pgvector round-trip for agent semantic memory (skipped automatically when
# the DSN is absent; provided here because the compose postgres ships pgvector).
export TEST_MEMORY_DSN="postgresql://mlaiops:mlaiops-local@localhost:${POSTGRES_PORT}/mlaiops"
python3 -m pytest python/tests/test_agents.py -q -k pgvector
