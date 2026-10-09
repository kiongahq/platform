package httpapi

import (
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func ensureSeedBlog(repository store.Repository) {
	for _, post := range repository.BlogPosts() {
		if post.Slug == "mounting-s3-as-a-filesystem-in-jupyter" {
			return
		}
	}
	_, _ = repository.UpsertBlogPost("", api.UpsertBlogPostRequest{
		Slug:    "mounting-s3-as-a-filesystem-in-jupyter",
		Title:   "Mounting S3 as a filesystem inside Jupyter",
		Summary: "A production-minded guide to making object storage feel local in notebooks with S3FS, FUSE, containers, permissions, health checks, and honest operational trade-offs.",
		Author:  "Kionga Engineering",
		Tags:    []string{"Jupyter", "S3", "Infrastructure", "MLOps"},
		Status:  "published",
		Content: `# Mounting S3 as a filesystem inside Jupyter

Data scientists usually want files. Infrastructure teams usually want object storage. Those two preferences are not in conflict, but pretending that S3 is a normal POSIX disk can create subtle reliability problems.

This guide explains a practical middle ground: mount selected S3-compatible buckets into a Jupyter workspace with **S3FS and FUSE**, keep the underlying object-store semantics visible, and design the container so failures are obvious rather than silently falling back to an empty local directory.

The pattern works with AWS S3, RustFS, Ceph RGW, and many S3-compatible services.

## What we are building

The notebook user sees a predictable tree:

~~~text
/workspace/
├── notebooks/
└── object-store/
    ├── models/
    ├── artifacts/
    ├── features/
    └── pipeline-logs/
~~~

Opening **/workspace/object-store/artifacts/experiment-42/metrics.json** causes S3FS to translate filesystem operations into S3 API requests.

The important architectural boundaries are:

1. Jupyter owns the user-facing workspace.
2. S3 remains the durable system of record for mounted buckets.
3. FUSE provides the kernel-to-userspace bridge.
4. S3FS translates filesystem operations into object operations.
5. Credentials are injected at runtime and never baked into the image.

## Why use a mount at all?

A native SDK such as boto3 is usually the best interface for production code. It exposes object-store behavior honestly, supports explicit retries, and avoids POSIX assumptions.

A mount is useful when:

- existing libraries require filesystem paths;
- users explore data interactively with familiar tools;
- notebook code needs to open artifacts produced by another service;
- model libraries expect directories rather than URIs;
- migration speed matters more than perfect object-store purity.

Use both interfaces deliberately. Keep application and pipeline code on native S3 APIs when possible; use the mount as an ergonomic workbench adapter.

## Understand the semantic mismatch

S3 stores immutable objects addressed by keys. A filesystem exposes files, directories, random writes, renames, locks, ownership, and atomic operations.

S3FS emulates missing filesystem behavior. That has consequences:

- a rename may become copy-then-delete;
- appending can rewrite an entire object;
- directory entries are key prefixes rather than real directories;
- file locking is not equivalent to a shared POSIX filesystem;
- metadata operations can generate many API requests;
- concurrent writers can overwrite one another;
- performance depends on object size, request latency, and cache policy.

Do not place databases, Git repositories, package environments, Kafka state, or lock-heavy workloads on the mount. Keep those on a real local or network filesystem.

## Build the Jupyter image

Install FUSE and S3FS in the image. On Debian-based images:

~~~dockerfile
FROM python:3.11-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends fuse3 s3fs \
 && printf 'user_allow_other\n' >> /etc/fuse.conf \
 && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --uid 1000 notebook \
 && mkdir -p /workspace/object-store \
 && chown -R notebook:notebook /workspace
~~~

**user_allow_other** permits a mount created during container startup to be read by the non-root notebook process when S3FS uses **allow_other**.

Pin the base image and packages in production. Scan the final image, produce an SBOM, and rebuild on a regular security cadence.

## Mount buckets during startup

The entrypoint has to run before Jupyter and fail if required mounts cannot be established.

~~~sh
#!/bin/sh
set -eu

mount_root="${S3_MOUNT_ROOT:-/workspace/object-store}"
buckets="${S3_MOUNT_BUCKETS:-models artifacts features}"
credentials=/run/s3fs-passwd

test -n "${S3_ENDPOINT:-}"
test -n "${AWS_ACCESS_KEY_ID:-}"
test -n "${AWS_SECRET_ACCESS_KEY:-}"
test -e /dev/fuse

printf '%s:%s\n' "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" > "$credentials"
chmod 600 "$credentials"
mkdir -p "$mount_root"

for bucket in $buckets; do
  target="$mount_root/$bucket"
  mkdir -p "$target"

  if ! mountpoint -q "$target"; then
    s3fs "$bucket" "$target" \
      -o "url=$S3_ENDPOINT" \
      -o use_path_request_style \
      -o "passwd_file=$credentials" \
      -o allow_other \
      -o uid=1000 \
      -o gid=1000 \
      -o umask=0022
  fi

  mountpoint -q "$target"
done

chown notebook:notebook "$mount_root"
exec runuser -u notebook -- jupyter lab \
  --ip=0.0.0.0 --port=8888 --no-browser \
  --ServerApp.root_dir=/workspace
~~~

There are several intentional choices here.

### Runtime credentials

The credentials file lives under **/run**, receives mode **0600**, and is created from runtime environment variables. It is not copied into an image layer or persisted in the workspace volume.

For a production Kubernetes deployment, prefer workload identity, projected credentials, or a secret broker that issues short-lived credentials. Static access keys should be the compatibility fallback.

### Path-style addressing

**use_path_request_style** is commonly needed for RustFS, Ceph RGW and development endpoints where wildcard bucket DNS is unavailable. AWS S3 often works without it.

### UID and GID mapping

The mount is created by the container entrypoint, often as root, while Jupyter runs as an unprivileged user. Explicit **uid**, **gid**, **allow_other**, and **umask** options make ownership predictable.

### Fail closed

An empty directory looks like a valid mount to notebook code. If mounting fails and Jupyter still starts, users may write important artifacts to ephemeral container storage.

For required storage, exit before Jupyter starts. Kubernetes or Compose will mark the service unhealthy and restart it. For optional storage, expose a loud degraded state in the UI and metrics.

## Container privileges

FUSE needs access to **/dev/fuse** plus mount capability. A Docker Compose service commonly needs:

~~~yaml
services:
  jupyter:
    devices:
      - /dev/fuse:/dev/fuse
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
~~~

**SYS_ADMIN** is broad. Do not copy this configuration to unrelated services.

For Kubernetes, investigate a CSI driver before granting mount privileges to notebook pods. A CSI-based object-store driver moves mount responsibility into a dedicated, controlled component and works better with pod security standards.

## Credentials and least privilege

Create a policy per workspace, user, or project. Avoid one platform-wide key with access to every bucket.

A useful policy boundary is:

- list only approved buckets or prefixes;
- read training datasets;
- write only the user's artifact prefix;
- read approved model artifacts;
- deny bucket policy and lifecycle administration;
- use short credential lifetimes;
- record access in object-store audit logs.

Never reveal object-store credentials to notebook JavaScript or place them in notebook cells. Users will save, export, and share notebooks.

## Health checks that test the mount

Process health is not storage health. A useful readiness check should verify:

1. **/dev/fuse** exists;
2. every required target is a mount point;
3. a bounded directory listing succeeds;
4. an optional write-read-delete probe succeeds in a dedicated health prefix;
5. credential expiry is not imminent.

Avoid reading a large object during every probe. Keep checks cheap and apply timeouts.

Example:

~~~sh
for bucket in $S3_MOUNT_BUCKETS; do
  timeout 3 mountpoint -q "$S3_MOUNT_ROOT/$bucket"
  timeout 3 ls "$S3_MOUNT_ROOT/$bucket" >/dev/null
done
~~~

Export separate metrics for mount presence, operation latency, errors, credential renewal, and cache usage.

## Performance tuning

Start without aggressive caching and measure real notebook workloads. Useful options depend on workload:

- **use_cache=/path** can improve repeated reads but consumes local disk;
- **max_stat_cache_size** controls metadata cache memory;
- **stat_cache_expire** balances freshness and request volume;
- multipart upload settings affect large writes;
- parallel request settings affect throughput and object-store pressure.

Prefer fewer, larger objects over millions of tiny files. Parquet datasets with sensible partitioning behave better than deeply nested trees of tiny JSON files.

Do not tune around one laptop. Measure inside the same network and container environment used in production.

## Safe notebook usage

Reading an object is straightforward:

~~~python
from pathlib import Path
import pandas as pd

features = Path("/workspace/object-store/features")
frame = pd.read_parquet(features / "customer_churn/date=2026-07-01")
~~~

For writes, create a complete local file and move or upload it once:

~~~python
from pathlib import Path
import tempfile

target = Path("/workspace/object-store/artifacts/run-42/metrics.json")
target.parent.mkdir(parents=True, exist_ok=True)

with tempfile.NamedTemporaryFile("w", delete=False) as handle:
    handle.write('{"accuracy": 0.91}')
    local_path = Path(handle.name)

target.write_bytes(local_path.read_bytes())
local_path.unlink()
~~~

For large or critical artifacts, use the SDK directly so retries, multipart behavior, checksums, and conditional writes are explicit.

## Multi-user architecture

A single shared Jupyter container and shared credentials are acceptable only for a trusted local environment.

A multi-user system should isolate:

- the notebook pod or workspace;
- persistent local storage;
- S3 prefixes or buckets;
- workload identity;
- network policy;
- compute and storage quota;
- audit identity;
- mount lifecycle.

The administrator should be able to answer: who can see this bucket, which workspace mounted it, what credential was issued, when it expires, and which objects were accessed?

## Alternatives

Choose the simplest interface that meets the workload:

### Native S3 SDK

Best for pipelines, services, reliable uploads, explicit errors, and object-native applications.

### Presigned URLs

Best for bounded browser uploads/downloads without distributing credentials.

### Object-store CSI driver

Best for Kubernetes-managed mounts and stronger separation of mount privileges.

### Managed network filesystem

Best when applications genuinely require POSIX locking, frequent random writes, low-latency metadata, or shared mutable files.

### Data lake table formats

Best when the problem is analytical tables, schema evolution, transactions, and large-scale query—not generic files.

## Production checklist

- [ ] Required buckets and prefixes are explicit.
- [ ] Credentials are short-lived and least privilege.
- [ ] Credentials never enter images, notebooks, logs, or Git.
- [ ] Mount failure prevents a false-ready Jupyter service.
- [ ] UID, GID, umask, and multi-user behavior are tested.
- [ ] **/dev/fuse** and capabilities are limited to the mount-owning workload.
- [ ] Read, write, rename, large-object, and concurrent-writer behavior is tested.
- [ ] Mount latency and errors are observable.
- [ ] Cache capacity and eviction are bounded.
- [ ] Backups and lifecycle policies exist independently of the mount.
- [ ] Users know when to prefer the native SDK.
- [ ] Multi-user deployments use isolated identities and storage boundaries.

## Closing perspective

Mounting S3 into Jupyter is an interface adapter, not a conversion of object storage into a real disk.

Used with clear boundaries, it removes friction from exploration and makes existing filesystem-oriented tools useful. Used carelessly, it hides failure modes and encourages workloads that object storage was never designed to support.

The production-minded version of this pattern is simple: mount only what the user is allowed to see, issue short-lived credentials, fail loudly, observe the mount, keep critical writes object-native, and preserve the distinction between a convenient path and the durable storage system behind it.`,
	}, "system")
}
