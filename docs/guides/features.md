# Feature store

Kionga provides an integrated feature store for both **real-time** (online) and
**batch** (offline) use, centralized behind the feature gateway.

Gateway examples assume `export MLAIOPS_URL=http://localhost:8080`.

## Online vs offline

- **Online** — features served from **Redis** through the feature-gateway for
  low-latency reads (real-time scoring, agent tool lookups).
- **Offline** — features materialized as **Parquet snapshots** in MinIO for batch
  training/analysis.

## Feature views

Defined in `python/features/definitions.py`. Each view names an **entity**, a
**schema** (typed fields), a TTL, and its source rows. The bundled views:

| View | Entity | Example fields |
| --- | --- | --- |
| `customer_profile` | `entity_id` | `plan`, `region`, `open_tickets`, `csat_90d` |
| `transaction_stats_5m` | `entity_id` | `txn_count_5m`, `txn_amount_5m`, `distinct_merchants_5m`, `home_region` |

## Materialization

The **feature-materializer** applies the definitions and populates the online store,
reporting entity counts back to the control plane, then exits. Re-run it:

```bash
docker compose -f deploy/compose.yaml up feature-materializer
```

It: applies each view (`POST /api/v1/features`), writes online values to Redis via
the feature gateway (`PUT /internal/v1/features/{view}/{entity}`), snapshots offline
Parquet to `s3://mlaiops-features/...`, and reports counts
(`POST /api/v1/features/{name}/materialized`).

## Browsing

=== "Console"

    The **Features** tab lists views with their schema, TTL, and online entity
    counts. Search filters by name/tag.

=== "API"

    ```bash
    curl -s "$MLAIOPS_URL/api/v1/features" | python -m json.tool
    ```

## Online lookups

Read online features directly from the feature gateway (Feast-compatible shape):

```bash
curl -s -X POST http://localhost:8083/get-online-features \
  -H 'Content-Type: application/json' \
  -d '{"feature_service":"customer_profile","entities":[{"entity_id":"u123"}]}'
```

Response:

```json
{"results": [{"values": {"plan":"pro","region":"eu-west","open_tickets":1,"csat_90d":4.6},
              "statuses": {"plan":"PRESENT", "...": "PRESENT"}}]}
```

Agents reach the same data through the `feature_store_lookup` tool and
`AgentMemoryClient.get_entity_features`; the real-time processor uses it to enrich
streaming events.

## Adding a feature view

Add a view to `python/features/definitions.py` with its entity, schema, TTL, and a
`source_rows` provider (deterministic rows for the demo; in production, read from the
offline store or a warehouse connection). Re-run the materializer to publish it.
