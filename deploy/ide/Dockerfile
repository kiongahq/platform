FROM codercom/code-server:4.99.4

USER root
ARG TARGETARCH
ARG NODE_VERSION=22.17.0
# Node.js publishes amd64 as "x64"; Docker's TARGETARCH is "amd64". Map it, or
# the tarball URL 404s on amd64 (the usual deployment architecture).
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl python3 xz-utils \
 && node_arch="$(case "${TARGETARCH}" in amd64) echo x64;; arm64) echo arm64;; *) echo "${TARGETARCH}";; esac)" \
 && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz" \
    | tar -xJ --strip-components=1 -C /usr/local \
 && npm install -g @openai/codex@0.142.5 @anthropic-ai/claude-code@2.1.200 \
 && rm -rf /var/lib/apt/lists/* /root/.npm

COPY deploy/workspace/kionga.py /usr/local/bin/kionga
COPY deploy/workspace/directories.py /usr/local/lib/kionga_workspace_directories.py
COPY deploy/ide/entrypoint.sh /usr/local/bin/ide-workspace
RUN chmod 0755 /usr/local/bin/kionga /usr/local/bin/ide-workspace \
 && mkdir -p /workspace \
 && chown -R coder:coder /workspace

WORKDIR /workspace
ENTRYPOINT ["dumb-init", "--", "ide-workspace"]
