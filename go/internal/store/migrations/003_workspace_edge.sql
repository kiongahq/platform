CREATE TABLE IF NOT EXISTS workspace_edge_tickets (
    tenant_id TEXT NOT NULL,
    token_hash BYTEA NOT NULL,
    subject TEXT NOT NULL,
    workspace_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, token_hash)
);
CREATE TABLE IF NOT EXISTS workspace_edge_sessions (
    tenant_id TEXT NOT NULL,
    token_hash BYTEA NOT NULL,
    subject TEXT NOT NULL,
    workspace_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, token_hash)
);
CREATE INDEX IF NOT EXISTS workspace_edge_sessions_expiry_idx ON workspace_edge_sessions (expires_at);
ALTER TABLE workspace_edge_tickets ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_edge_tickets FORCE ROW LEVEL SECURITY;
ALTER TABLE workspace_edge_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_edge_sessions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS workspace_edge_ticket_tenant ON workspace_edge_tickets;
CREATE POLICY workspace_edge_ticket_tenant ON workspace_edge_tickets
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
DROP POLICY IF EXISTS workspace_edge_session_tenant ON workspace_edge_sessions;
CREATE POLICY workspace_edge_session_tenant ON workspace_edge_sessions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
