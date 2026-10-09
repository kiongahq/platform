# External object storage

Kionga ships MinIO for local use. Production and team deployments usually
keep artifacts, feature snapshots and datasets in an existing S3-compatible
store (AWS S3, GCS interoperability, Azure via an S3 gateway, Ceph RGW, MinIO,
Wasabi, Cloudflare R2). A **storage connection** records such a bucket and
checks it.

## Registering a bucket

Platform → **Feature stores and object storage** → **＋ Object storage**, or:

```bash
export LAKE_CREDENTIALS='AKIA...:...'          # on the gateway, never in the request
curl -X POST $KIONGA/api/v1/admin/storage-connections -H 'Content-Type: application/json' -d '{
  "name": "team-lake", "endpoint": "https://s3.eu-west-1.amazonaws.com",
  "region": "eu-west-1", "bucket": "team-lake", "path_style": false,
  "secret_ref": "env:LAKE_CREDENTIALS", "allowed_projects": []
}'
curl -X POST $KIONGA/api/v1/admin/storage-connections/<id>/test
```

| Field | Rules |
| --- | --- |
| `endpoint` | `http(s)://host[:port]`, no path, query or embedded credentials |
| `region` | lowercase letters, digits and hyphens; defaults to `us-east-1` for signing |
| `bucket` | valid S3 name; buckets with dots require `path_style` over HTTPS (wildcard certificates do not cover them) |
| `path_style` | `true` for MinIO, Ceph and dotted buckets; `false` for virtual-hosted AWS |
| `ca_bundle` | PEM CA certificates added to the system roots; private keys are rejected |
| `secret_ref` | `env:VAR` holding `ACCESS_KEY_ID:SECRET_ACCESS_KEY` |

**Test** sends a SigV4-signed `HEAD` on the bucket (the same signer as the
Storage Explorer, `go/internal/storage`) and records:

| Result | Meaning |
| --- | --- |
| `healthy` | 200 within 3 s |
| `degraded` | slow (>3 s), 5xx, or a region redirect (the reason names the bucket's real region) |
| `unavailable` | unreachable, TLS failure (add `ca_bundle`), 403 (bad or expired credentials, missing `s3:ListBucket`), 404 (no such bucket) |
| `configured` | saved, but `secret_ref` is not set on the gateway |

Credentials never appear in responses, stored documents or check reasons.

## Consistency

- AWS S3 has strong read-after-write consistency for all operations. Most
  S3-compatible stores match it for single objects; listings on some (older
  Ceph, some gateways) are eventually consistent.
- Kionga writes immutable, versioned keys for feature snapshots and model
  artifacts and records the URI in lineage, so readers never depend on a
  listing or on overwriting a key in place.
- Cross-region replication is asynchronous. Point training jobs at the
  primary region, or accept replication lag explicitly.

## Security

- Use one IAM principal per deployment (or per tenant) scoped to the
  bucket and prefix: `s3:ListBucket` on the bucket, `s3:GetObject`/`PutObject`
  on the prefix. Never use root or account-wide keys.
- Prefer short-lived credentials (IRSA on EKS, Workload Identity on GKE,
  Workload Identity on AKS) over static keys in `secret_ref`; the env
  variable can be populated by your secret manager's injector.
- Require TLS. Use `ca_bundle` for private CAs instead of disabling
  verification (there is no option to disable it).
- Enable bucket encryption (SSE-S3 or SSE-KMS) and block public access.
- `allowed_projects` records which projects may use a connection; grant
  bucket access to users through the existing bucket grants.

## Performance

- Keep compute and the bucket in the same region; cross-region reads add
  latency and egress cost.
- Large Parquet snapshots benefit from multipart uploads (pyarrow and boto3 do
  this automatically) and from partitioning by date or entity hash.
- The online path never reads object storage: online lookups stay in Redis.
- S3FS mounts in JupyterLab are convenient for browsing but slow for
  random I/O; read datasets with pyarrow/fsspec directly in jobs.

## Failure and recovery

| Failure | Effect | Recovery |
| --- | --- | --- |
| Endpoint down | Test reports `unavailable`; materializer records a failed run in lineage and the view goes stale after its TTL | Online serving continues from Redis; re-run materialization when the store returns |
| Credentials rotated | Test reports `403` as `unavailable` | Update the gateway env var and re-test; nothing else changes |
| Bucket deleted or emptied | `404` | Restore from versioning/replica; lineage records which `offline_uri` each view version used |
| Region moved | `degraded` with the real region | Update `region` |

Enable bucket versioning so a bad materialization can be rolled back to the
`offline_uri` recorded for an earlier run.

## Kubernetes: tenant storage

Tenant workloads (per-user workspaces, pipeline steps, agents) must never
choose where on a node their data lands.

- **Never `hostPath`.** The operator's admission check
  (`operator.ValidateTenantStorage`, `go/internal/operator/storage_admission.go`)
  rejects `hostPath` and any volume source other than PVCs, ConfigMaps,
  Secrets, `emptyDir`, projected and downward-API volumes. A workspace that
  fails the check is marked `Failed` with reason `StorageNotAllowed` and no
  claim is created.
- **Allowlisted StorageClasses.** Set
  `WORKSPACE_ALLOWED_STORAGE_CLASSES=gp3,s3-csi` on the operator. A workspace
  claim whose class (`WORKSPACE_STORAGE_CLASS`, or the cluster default) is not
  on the list is refused.
- **Object storage as volumes** goes through a CSI driver (Mountpoint for
  Amazon S3 CSI, GCS FUSE CSI, Azure Blob CSI) exposed as an allowlisted
  StorageClass, so access is governed by the driver's identity binding rather
  than node paths.
- **Per tenant profile (planned).** Today the allowlist is operator-wide.
  Mapping resource profiles (`starter`, `team`, `power`, `gpu`) to different
  allowlisted classes per tenant is planned; until then use one allowlist per
  operator installation (one per cluster or namespace group).
