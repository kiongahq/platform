# Production operations and acceptance

These requirements apply to both hosting paths. Packaging and local tests are not
proof of successful cloud deployment. Record your installation's owners, cloud,
cluster, identity mappings, release digests, dependency versions and acceptance results.

## Security boundaries

Mounted-secret loading is implemented in the Go gateway, operator, worker, feature
gateway, storage proxy, trace proxy and serving manager. It is not a universal
loader for Python services or third-party images. Supported names are defined in
`go/internal/runtimeconfig/config.go`. Files are read at startup; restart/roll out
consumers after rotation. Do not place secret data in ConfigMaps, Helm values,
deployment JSON, traces or CI logs. Secret values enter process memory/environment
after loading; mounted files are not protection against a compromised process.

The internal service token is a shared credential, not per-workload mTLS identity.
Rotate it across all consumers deliberately. Protect the stable personal-credential
encryption key; automated multi-key rotation/KMS envelope encryption is not supplied.
Losing or replacing it requires recovery from backup or reconnecting accounts.

Operators and gateway service accounts have sensitive namespace permissions.
Namespace RBAC and restricted pods are not isolation from cluster administrators
or a complete sandbox for hostile arbitrary code. Apply tenant-aware network
policies, admission controls, image policies and resource limits. Generated-code
frameworks such as NOOA require an independently reviewed sandbox and egress policy.

### Notebook and IDE browser isolation

Production mode disables the same-origin workspace proxy by default. The
Kubernetes chart provides wildcard workspace ingress and per-workspace subdomains.
The gateway issues a 45-second, single-use PostgreSQL handoff ticket and a
30-minute host-only session on redemption. It does not send the control-plane
browser cookie to a workspace host. Every request checks the live grant and owner.
Console logout revokes workspace sessions for that subject. Test DNS, wildcard
TLS, redirects, iframe cookies and websockets with your actual ingress controller.
The VM package has no workload plane.

`KIONGA_TRUSTED_WORKSPACE_PROXY=true` opts into the existing same-origin proxy only
for a deliberately trusted environment. It is incompatible with isolated ingress.
Restrict wildcard DNS/TLS control and test hostname routing and grant revocation.
If a user signs out at the identity provider without using Kionga logout, an
existing workspace session lasts at most 30 minutes unless the platform grant
is revoked first.

## Availability and observability

Monitor public gateway readiness, HTTP errors/latency, worker polling readiness,
Kafka consumer lag, outbox backlog, operator reconciliation failures, pod restarts,
PVC capacity, scheduling failures, resource quotas, certificates and secret sync.
Health probes for service proxies do not verify every upstream dependency.
Install your organization's metrics/log collection and alerts; this chart does
not supply a complete monitoring stack or on-call service. It does include
optional ServiceMonitors and two availability alerts when a Prometheus Operator
is installed. Verify that alerts are actually selected, routed and fired in staging.

Test node drain, replica loss and dependency interruptions in staging. Disruption
budgets govern voluntary disruptions, not all failures. Ensure spare scheduling
capacity and redundant storage/databases/engines. The VM has no gateway failover.
Use private connectivity and TLS to external services; verify each engine's auth
and tenancy behavior rather than assuming the platform adds it automatically.

## Backups and recovery

Define measurable RPO/RTO. Use PostgreSQL point-in-time recovery, object versioning
and tested workspace PVC snapshots. Securely back up credential encryption keys
and configuration alongside compatible data versions. Kafka retention/replay is
not a database backup. Include MLflow/Langfuse/Prefect and other external engine
state in the recovery plan. See [PostgreSQL backup guidance](https://www.postgresql.org/docs/current/backup.html).

Restore into a separate environment regularly. Verify identity configuration,
encrypted account credentials, artifacts, workspace files and job reconciliation.
Measure recovery time and retain evidence. Never assume Helm rollback restores
schema, object data, external secret versions or custom-resource definitions.

## Release controls

Build once and promote immutable digests. Scan images/dependencies and verify
provenance/SBOMs through your chosen tooling; build attestations alone do not
enforce admission. Keep cloud deployment permissions outside normal application
credentials. Use reviewed GitOps or short-lived CI federation with protected
environments. Test migrations against backups before rollout; protect production
from incompatible downgrade. Gateway replicas serialize embedded migrations
with a PostgreSQL advisory lock; schema compatibility across versions still
needs a staged rollout check.

## Required acceptance checklist

1. Confirm HTTPS, callback/origin matching and login/logout with the real IdP.
   Anonymous console/API access must fail while public health remains available.
2. Test an admin, a provisioned normal user, an unprovisioned user and a suspended
   user. Attempt cross-project reads/writes and direct API access, not just UI clicks.
3. Provision CPU/memory/storage/services; verify limits and rejection of requests
   beyond grants. Create, stop and resume a workspace without losing files.
4. Run a job and multi-step pipeline, consume events, register/download a model,
   access approved object storage and invoke an agent. Verify real engine results.
5. Verify denied network paths and allowed destinations, including workspace
   object access, model downloads, Kafka REST and the Kubernetes API.
6. Drain a node or stop a replica, interrupt a dependency and confirm recovery,
   alerts, bounded retries and no unauthorized privilege fallback.
7. Rotate credentials with coordinated consumer restart; restore a backup into
   staging and validate both encrypted credentials and user artifacts.
8. Test separate-origin workspace authentication, ownership checks, revoked
   access and cookie isolation before enabling untrusted notebook/IDE users.

Track failures as release blockers. Full workload acceptance does not apply to a
gateway-only VM unless an external workload plane has been separately integrated.

`scripts/verify-production.py` automates the read-only control-plane checks and
optionally tests workspace handoff with `--test-workspace-handoff`. Record the
result, then run the remaining stateful, disruptive and recovery cases in staging.
The PostgreSQL integration test verifies ticket redemption across connections
and session revocation; it does not replace an ingress/browser test.
