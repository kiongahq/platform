FROM codercom/code-server:4.99.4

USER root
ARG TARGETARCH
ARG NODE_VERSION=22.17.0
ARG INSTALL_CODING_AGENTS=false
# Node.js publishes amd64 as "x64"; Docker's TARGETARCH is "amd64". Map it, or
# the tarball URL 404s on amd64 (the usual deployment architecture).
RUN apt-get update \
 && apt-get install -y --no-install-recommends git python3 \
 && rm -rf /var/lib/apt/lists/*

# The editor must start without a second large registry/toolchain download.
# Opt in to agent CLIs with `make ide-agents-up` when those registries are reachable.
RUN if [ "$INSTALL_CODING_AGENTS" = true ]; then \
      apt-get update \
      && apt-get install -y --no-install-recommends curl xz-utils \
      && node_arch="$(case "${TARGETARCH}" in amd64) echo x64;; arm64) echo arm64;; *) echo "${TARGETARCH}";; esac)" \
      && curl -fsSL --retry 3 "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz" \
        | tar -xJ --strip-components=1 -C /usr/local \
      && npm install -g @openai/codex@0.142.5 @anthropic-ai/claude-code@2.1.200 \
      && rm -rf /var/lib/apt/lists/* /root/.npm; \
    fi

COPY workspace/kionga.py /usr/local/bin/kionga
COPY workspace/directories.py /usr/local/lib/kionga_workspace_directories.py
COPY ide/entrypoint.sh /usr/local/bin/ide-workspace
RUN chmod 0755 /usr/local/bin/kionga /usr/local/bin/ide-workspace \
 && mkdir -p /workspace \
 && chown -R coder:coder /workspace

WORKDIR /workspace
ENTRYPOINT ["dumb-init", "--", "ide-workspace"]
