#!/bin/sh
set -eu

mkdir -p /home/coder/.codex /home/coder/.claude /workspace
if [ "$(id -u)" = 0 ]; then
  chown -R coder:coder /home/coder/.codex /home/coder/.claude /workspace
  set -- runuser -u coder --
else
  set --
fi

"$@" python3 /usr/local/lib/kionga_workspace_directories.py &

exec "$@" code-server \
  --bind-addr 0.0.0.0:8080 \
  --auth "${KIONGA_IDE_AUTH_MODE:-password}" \
  --abs-proxy-base-path=/workspaces/ide \
  /workspace
