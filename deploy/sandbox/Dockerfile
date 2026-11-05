# Base image for agent sandboxes and git helpers. Runs as a fixed non-root user. The
# manager relies on bash, GNU coreutils and findutils, and git being present here.
# Pin the built image by digest in KIONGA_SANDBOX_IMAGES.
FROM debian:trixie-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash ca-certificates coreutils curl file findutils git jq less make \
        procps ripgrep tar unzip xz-utils zip \
        build-essential pkg-config \
        python3 python3-pip python3-venv \
        nodejs npm \
    && rm -rf /var/lib/apt/lists/* \
    # Nothing in a sandbox should be able to gain privilege, even without
    # no-new-privileges: strip every setuid and setgid bit.
    && find / -xdev -perm /6000 -type f -exec chmod a-s {} + \
    && groupadd --gid 10001 kionga \
    && useradd --uid 10001 --gid 10001 --home-dir /workspace --no-create-home --shell /bin/bash kionga \
    && mkdir -p /workspace \
    && chown 10001:10001 /workspace

ENV HOME=/workspace
WORKDIR /workspace
USER 10001:10001
CMD ["/bin/sh", "-c", "while :; do sleep 3600 & wait; done"]
