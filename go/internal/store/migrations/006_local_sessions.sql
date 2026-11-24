-- Local (password) sign-in sessions, by the SHA-256 of the session token, so
-- a session is valid on every gateway replica and sign-out revokes it on all
-- of them. The token itself is never stored.
CREATE TABLE IF NOT EXISTS local_sessions (
    tenant_id TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    subject TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, token_hash)
);
CREATE INDEX IF NOT EXISTS local_sessions_expiry_idx ON local_sessions (expires_at);
ALTER TABLE local_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE local_sessions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS local_sessions_tenant ON local_sessions;
CREATE POLICY local_sessions_tenant ON local_sessions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
