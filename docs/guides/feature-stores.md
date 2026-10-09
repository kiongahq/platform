# Feature stores: internal and external

Kionga serves features from its **internal store** (Redis online, Parquet
offline in `s3://mlaiops-features`) and can connect **external stores**
through adapters. The Features page shows, for every store, whether it is
internal or external, where it lives, its last health check with the reason,
and which projects may use it. Each feature view card shows its version,
store location, freshness badge and recent failures.

## Adapter contract

Every provider implements `feature.Adapter` (`go/internal/feature/adapter.go`):

| Method | Meaning |
| --- | --- |
| `Name()` | Registry name |
| `Capabilities()` | `online`, `offline`, `point_in_time`, `materialize`, `ttl`, `push`: what Kionga can drive through this adapter |
| `Health(ctx)` | `configured`, `healthy`, `degraded` or `unavailable`, with a reason |
| `ListFeatureViews(ctx)` | Views the store reports, or `ErrNotSupported` |
| `GetOnline(ctx, request)` | One row per entity, `NOT_FOUND` statuses for misses |

| Provider | Adapter | Capabilities | Health check |
| --- | --- | --- | --- |
| `internal` | yes | all six | Redis `PING` or feature-gateway `/healthz` (which pings Redis); degraded without an offline location |
| `feast` | yes | online, ttl | Feast `GET /health`; 401/403 means the credential was rejected; slow (>2 s) is degraded |
| `tecton`, `hopsworks`, `vertex-ai`, `sagemaker`, `databricks` | no | none | listed as **contract available — no adapter**; connections cannot be created |

Both adapters pass one conformance suite (`TestInternalAdapterConformance`,
`TestInternalAdapterThroughFeatureGatewayConformance`,
`TestFeastAdapterConformance`); the Feast fake reproduces the feature server's
real `/health` and columnar `/get-online-features` contract.

## Connecting an external store

Platform → **Feature stores and object storage** → **＋ Feature store**, or:

```bash
curl -X POST $KIONGA/api/v1/admin/feature-stores -H 'Content-Type: application/json' -d '{
  "provider": "feast", "name": "team-feast",
  "config": {"url": "http://feast-feature-server:6566", "feature_services": "customer_profile"},
  "secret_ref": "env:FEAST_TOKEN",
  "allowed_projects": ["prj-123"]
}'
curl -X POST $KIONGA/api/v1/admin/feature-stores/<id>/test
```

- `config` takes only the provider's documented keys. Keys that look like
  secrets and URLs with embedded credentials are rejected.
- `secret_ref` is `env:VARIABLE` on the gateway. The value is read only for a
  check and is never stored, logged or returned; responses report only
  `secret_present`.
- `allowed_projects` empty means every project. Members see a store only when
  it is shared with one of their projects, and only their own project ids.
- Saving or editing resets health to `configured`; **Test** records the result.

| Endpoint | Who |
| --- | --- |
| `GET/POST /api/v1/admin/feature-stores`, `GET/PUT/DELETE /{id}`, `POST /{id}/test` | admin, operator |
| `GET /api/v1/admin/feature-store-providers` | admin, operator |
| `GET /api/v1/features/stores` | anyone with the features service (scoped) |

The built-in store has id `internal`; it is configured by
`MLAIOPS_FEATURE_GATEWAY_URL` and `KIONGA_FEATURE_OFFLINE_URI` and cannot be
edited or deleted.

## Versions, lineage and freshness

- **Immutable versions.** Every `POST /api/v1/features` whose definition
  (entity, fields, tags, source, TTL, store) changed writes
  `feature_definition_version` `<name>@<n>`. Re-applying the same definition
  writes nothing. `GET /api/v1/features/{name}/versions`.
- **Lineage.** The materializer reports each run to
  `POST /api/v1/features/{name}/materializations` with `run_id`,
  `source_dataset`, `offline_uri`, `entity_count`, `status` and `error`, linked
  to the current view version. `GET /api/v1/features/{name}/lineage`. Only the
  materializer (service role), engineers and operators may report.
- **Freshness** compares the last successful materialization with the view's
  TTL: `fresh`, `stale` (older than TTL) or `never`. A zero TTL never goes stale.
  `GET /api/v1/features/views` returns each view with store, freshness,
  version, last run and failures since the last success.

## Point-in-time joins

`features.point_in_time.point_in_time_join` builds training sets without
future leakage: per entity, it takes the latest feature row at or before each
label's `event_timestamp` (ties broken by `created_timestamp`), and treats rows
older than `ttl_seconds` as missing.

```python
from features.point_in_time import point_in_time_join
training = point_in_time_join(labels, feature_rows, entity_key="user_id", features=["plan", "spend"], ttl_seconds=3600)
```

The leakage fixture in `python/tests/test_feature_store_correctness.py`
places a future value after every label and checks it never appears, and
compares every output against a brute-force scan.

## TTL and retention sweep

The feature gateway writes online rows without expiry.
`python -m features.retention` (with `REDIS_URL`) arms `EXPIRE ttl_seconds` on
every key of a view that has a TTL, deletes keys of views that no longer
exist, and leaves TTL-less views alone. `--dry-run` reports without changes.
Run it after each materialization: a row that stops being refreshed then
disappears within one TTL of the next sweep.

## Metrics

The feature gateway exports `/metrics`:

| Metric | Labels |
| --- | --- |
| `kionga_feature_gateway_lookups_total` | `mode`, `outcome` (ok, invalid_request, failed) |
| `kionga_feature_gateway_lookup_entities_total` | `mode` |
| `kionga_feature_gateway_lookup_entities_missing_total` | `mode` |
| `kionga_feature_gateway_lookup_duration_seconds` | `mode` |
| `kionga_feature_gateway_writes_total` | `mode`, `outcome` (ok, unauthorized, invalid_request, unsupported) |
| `kionga_feature_gateway_health_checks_total` | `mode`, `outcome` |

`/healthz` now returns `503` when Redis or Feast is unreachable.
