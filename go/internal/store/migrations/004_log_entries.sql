-- Structured, tenant-isolated logs and events for pipelines, nodes,
-- workloads and the platform. Queried by run/node/time and exported to
-- Elasticsearch/OpenSearch by cmd/log-exporter (sequence is the cursor).
CREATE TABLE IF NOT EXISTS log_entries (
    tenant_id TEXT NOT NULL,
    sequence BIGSERIAL NOT NULL,
    ts TIMESTAMPTZ NOT NULL,
    project_id TEXT NOT NULL DEFAULT '',
    pipeline_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    node TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 0,
    workload_kind TEXT NOT NULL DEFAULT '',
    workload_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    severity TEXT NOT NULL,
    message TEXT NOT NULL,
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, sequence)
);
CREATE INDEX IF NOT EXISTS log_entries_run_idx ON log_entries (tenant_id, run_id, sequence);
CREATE INDEX IF NOT EXISTS log_entries_project_ts_idx ON log_entries (tenant_id, project_id, ts DESC);
CREATE INDEX IF NOT EXISTS log_entries_ts_idx ON log_entries (ts);
ALTER TABLE log_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE log_entries FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS log_entries_tenant ON log_entries;
CREATE POLICY log_entries_tenant ON log_entries
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
