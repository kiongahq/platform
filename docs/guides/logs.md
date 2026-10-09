# Logs and events

Kionga stores structured logs and events for pipeline runs and correlates them by
tenant, project, flow, run, node, attempt and workload.

## What is recorded

| Source | Produced by | Examples |
| --- | --- | --- |
| `runner` | the pipeline runner, streamed while a container runs | container stdout and stderr, one entry per line |
| `platform` | the gateway | every node transition (`train → failed: ValueError: …`) with attempt, exit code and workload |
| `engine` | reserved for orchestrator messages | |
| `k8s` | the Kubernetes executor (opt-in, see [Pipelines](pipelines.md#kubernetes)) | Pod and Job events such as `FailedScheduling`, `OOMKilled` or a deadline; container stdout is not streamed yet |

Each entry has a timestamp, source, severity (`debug`, `info`, `warn`, `error`),
message and structured context. Container lines from stderr are `warn` unless the
runner says otherwise.

**Secrets are redacted before storage**: cloud and provider keys, bearer and JSON
web tokens, private key blocks, credentials in URLs, `password=` style pairs, and
the exact values of the gateway's own secret environment variables.

Per node, streamed output is capped (`MLAIOPS_PIPELINE_LOG_STREAM_LIMIT_BYTES`,
default 5 MiB) with an explicit truncation notice; the final output tail is still
kept on the node. Entries older than `KIONGA_LOG_RETENTION_DAYS` (default 30) are
deleted hourly.

## Reading logs

- **In a run**: select a node in the graph to see its logs and events.
- **Logs page** (Operate → Logs): filter by project (the page's project selector),
  run, node, time range, source, severity and text. Results are newest first with
  **Load earlier lines**; **Live tail** streams new lines.

You only ever see projects you can read; asking for another project's run returns
"not found".

```bash
curl -s "$MLAIOPS_URL/api/v1/logs?run_id=<run>&node=train&severity=warn,error&q=timeout"
curl -s "$MLAIOPS_URL/api/v1/logs?project_id=<prj>&order=desc&limit=100&before=<sequence>"
curl -N "$MLAIOPS_URL/api/v1/logs/stream?run_id=<run>"   # Server-Sent Events
```

The stream sends `event: logs` with a JSON array and `id: <sequence>`. It sends at
most 200 entries per event and continues from the last one, so a slow client never
loses lines; reconnect with `Last-Event-ID` to resume.

## Exporting to Elasticsearch or OpenSearch

See [Log export](../operations/log-export.md). Kionga keeps working whether or not
the exporter is running; the Platform page shows its state.
