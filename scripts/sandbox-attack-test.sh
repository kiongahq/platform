#!/usr/bin/env bash
# Runs the sandbox attack suite against a real engine. It only removes objects carrying
# the suite's own random name prefix.
#
#   CI, real gVisor:  see .github/workflows/sandbox-attack.yml
#   Local, NOT gVisor: KIONGA_ATTACK_RUNTIME=crun KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME=1 \
#     KIONGA_ATTACK_SOCKET=<podman socket> KIONGA_ATTACK_GIT_HOST=192.168.127.254 \
#     scripts/sandbox-attack-test.sh
set -euo pipefail
cd "$(dirname "$0")/.."

engine="${KIONGA_ATTACK_ENGINE:-$(command -v docker || command -v podman)}"
if [[ -z "${KIONGA_ATTACK_IMAGE:-}" ]]; then
  "$engine" build --quiet --tag kionga-sandbox-attack-base:dev deploy/sandbox >/dev/null
  KIONGA_ATTACK_IMAGE="$("$engine" image inspect kionga-sandbox-attack-base:dev --format '{{.Id}}')"
  [[ "$KIONGA_ATTACK_IMAGE" == sha256:* ]] || KIONGA_ATTACK_IMAGE="sha256:$KIONGA_ATTACK_IMAGE"
fi
export KIONGA_ATTACK_IMAGE
export KIONGA_ATTACK_SOCKET="${KIONGA_ATTACK_SOCKET:-/var/run/docker.sock}"
export KIONGA_ATTACK_RUNTIME="${KIONGA_ATTACK_RUNTIME:-runsc}"
cd go
exec go test -tags sandboxattack -count=1 -timeout 30m -v -run "${KIONGA_ATTACK_RUN:-^TestAttack}" ./internal/sandbox
