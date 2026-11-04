#!/usr/bin/env bash
# End-to-end proof of Agent Teams across real processes: the gateway binary with a real
# store and the team scheduler, the real designer runner on the mock model, and the real
# sandbox manager under real runsc on Docker. Only a local HTTPS git server and a
# counting HTTP backend stand in for things outside Kionga. See
# .github/workflows/agent-teams-smoke.yml.
#
#   AGENT_RUNTIME_DIR=../agent-runtime scripts/agent-teams-smoke.sh
#
# AGENT_RUNTIME_DIR must hold a .venv with requirements-dev.txt installed (see
# agent-runtime/CONTRIBUTING.md). KIONGA_SMOKE_SCENARIOS=1,5 runs a subset, and any
# scenario not run makes the result INCOMPLETE, with exit status 3.
#
# Environment for the sandbox scenarios: KIONGA_SMOKE_IMAGE (a local sha256: image ID,
# built here when unset) and KIONGA_SMOKE_GIT_ADDRESS (an address git helper containers
# reach this host on, by default Docker's bridge gateway). KIONGA_SMOKE_NO_ENGINE=1
# runs without any engine: every scenario that needs a sandbox is then reported NOT RUN.
# DATABASE_URL selects PostgreSQL; without it the gateway uses its file store.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AGENT_RUNTIME_DIR="$(cd "${AGENT_RUNTIME_DIR:-$ROOT/../agent-runtime}" && pwd)"
export AGENT_RUNTIME_DIR
PY="${KIONGA_SMOKE_PYTHON:-$AGENT_RUNTIME_DIR/.venv/bin/python}"
[ -x "$PY" ] || { echo "no virtualenv at $AGENT_RUNTIME_DIR/.venv" >&2; exit 2; }
command -v go >/dev/null || { echo "go is required" >&2; exit 2; }
WORK="${KIONGA_SMOKE_WORK:-$(mktemp -d "${TMPDIR:-/tmp}/agent-teams-smoke.XXXXXX")}"
mkdir -p "$WORK"
export KIONGA_SMOKE_WORK="$WORK"
export KIONGA_SMOKE_CONTRACTS="$ROOT/contracts/agent-team"
[ -f "$KIONGA_SMOKE_CONTRACTS/team.v1.schema.json" ] || { echo "contracts submodule is missing: git submodule update --init" >&2; exit 2; }

echo "== build gateway and sandbox manager"
# The console is not under test. The gateway refuses to start without an index page.
DIST="$ROOT/go/cmd/gateway/frontend-dist"
if [ ! -f "$DIST/index.html" ]; then
  placeholder=1
  printf '<!doctype html><title>smoke</title>\n' > "$DIST/index.html"
fi
trap '[ -z "${placeholder:-}" ] || rm -f "$DIST/index.html"' EXIT
(cd "$ROOT/go" && go build -o "$WORK/gateway" ./cmd/gateway && go build -o "$WORK/sandbox-manager" ./cmd/sandbox-manager)
[ -z "${placeholder:-}" ] || { rm -f "$DIST/index.html"; placeholder=; }

if [ "${KIONGA_SMOKE_NO_ENGINE:-}" != "1" ]; then
  if [ -z "${KIONGA_SMOKE_IMAGE:-}" ]; then
    echo "== build the sandbox image"
    docker build --quiet --tag kionga-agent-teams-smoke:dev "$ROOT/deploy/sandbox" >/dev/null
    KIONGA_SMOKE_IMAGE="$(docker image inspect kionga-agent-teams-smoke:dev --format '{{.Id}}')"
    export KIONGA_SMOKE_IMAGE
  fi
  if [ -z "${KIONGA_SMOKE_GIT_ADDRESS:-}" ]; then
    KIONGA_SMOKE_GIT_ADDRESS="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')"
    export KIONGA_SMOKE_GIT_ADDRESS
  fi
fi

exec "$PY" -u "$ROOT/scripts/agent_teams_smoke.py"
