# Log export (Elasticsearch / OpenSearch)

`log-exporter` is an optional service that ships one tenant's Kionga log entries
(see [Logs and events](../guides/logs.md)) to Elasticsearch or OpenSearch. The
gateway never waits on it: if it is not deployed, misconfigured or the cluster is
down, pipelines and the console keep working and the **Platform** page shows the
exporter's state.

## States

| State | Meaning |
| --- | --- |
| `not_deployed` | The exporter is not running (the default). |
| `not_configured` | Running, but `KIONGA_LOG_EXPORT_*` is incomplete; the detail says what is missing. |
| `configured` | Configured, nothing exported yet. |
| `healthy` | The last batch was accepted. |
| `degraded` | The last request failed; retrying with backoff (1s doubling to 60s). |
| `unavailable` | Five or more consecutive failures. |

## Targets

The two targets are separate adapters with separate tests; OpenSearch is not
assumed to support Elasticsearch-only features.

| | Elasticsearch | OpenSearch |
| --- | --- | --- |
| Destination | data stream `logs-kionga.<tenant>-default` (matches the built-in `logs-*-*` template) | daily index `kionga-logs-<tenant>-YYYY.MM.DD` |
| Bulk action | `create` | `index` with a stable `_id` (`<tenant>-<sequence>`) |
| Authentication | API key (`Authorization: ApiKey …`) or basic auth | basic auth |

Documents use ECS-style fields: `@timestamp`, `message`, `log.level`,
`event.dataset` (`kionga.<source>`), `event.id`, and `kionga.{tenant, project_id,
pipeline_id, run_id, node, attempt, workload_kind, workload_id, source, sequence,
context}`.

## Configuration

Set in `.env` (Compose) or the exporter's environment:

| Variable | Purpose |
| --- | --- |
| `KIONGA_LOG_EXPORT_TARGET` | `elasticsearch` or `opensearch` |
| `KIONGA_LOG_EXPORT_URL` | `https://cluster:9200` (credentials in the URL are rejected) |
| `KIONGA_LOG_EXPORT_API_KEY_FILE` | file with an Elasticsearch API key |
| `KIONGA_LOG_EXPORT_USERNAME` / `KIONGA_LOG_EXPORT_PASSWORD_FILE` | basic auth; the password is read from a file |
| `KIONGA_LOG_EXPORT_CA_FILE` | PEM bundle for a private CA |
| `KIONGA_LOG_EXPORT_INSECURE_TLS` | `true` disables certificate checks (testing only) |
| `KIONGA_LOG_EXPORT_ANONYMOUS` | `true` allows an unauthenticated test cluster |
| `KIONGA_LOG_EXPORT_BATCH` | documents per bulk request (default 500) |
| `MLAIOPS_TENANT` | tenant to export; one exporter per tenant |

Put secret files in `config/secrets/` (ignored by Git, mounted read-only at
`/run/kionga-secrets`), for example
`KIONGA_LOG_EXPORT_API_KEY_FILE=/run/kionga-secrets/es-api-key`.
Credentials are sent only over HTTPS to remote hosts and never appear in errors,
logs or the status endpoint.

```bash
docker compose -f deploy/compose.yaml --profile log-export up -d log-exporter
curl -s http://localhost:8086/status
```

For a local test cluster: `--profile log-export-es` starts single-node
Elasticsearch on `localhost:9200`, `--profile log-export-os` starts OpenSearch on
`localhost:9201`, both without security; set `KIONGA_LOG_EXPORT_ANONYMOUS=true` and
`KIONGA_LOG_EXPORT_URL=http://elasticsearch:9200` or `http://opensearch:9200`.

## Delivery guarantees

- **At least once.** The exporter's cursor (the log sequence) advances only after
  a batch is fully accepted or dead-lettered, and is stored in the database.
- **Partial failures.** Only rejected documents are retried. `429` and `5xx` items
  retry with backoff up to five attempts; mapping and validation errors (`4xx`) are
  counted as dead-lettered and reported on the status endpoint.
- **Tenant separation.** Each exporter reads one tenant's rows (PostgreSQL
  row-level security) and writes to that tenant's data stream or indices.
- **Retention** in the target is your cluster's ILM/ISM policy. Kionga's own
  retention is `KIONGA_LOG_RETENTION_DAYS`; export lag beyond it loses entries.

## Verification status

Both adapters are tested against servers that reproduce the bulk API's request and
response formats, including partial failures, outages and authentication errors.
They have **not** been run against a live Elasticsearch or OpenSearch cluster in
this repository's CI; the Compose test profiles exist for doing so.

Audit events are not exported by this service; they are already published to the
Kafka topic `mlaiops.audit.operations`, which Elastic's or OpenSearch's Kafka
integrations can consume.
