-- Single-use signed tokens: the id of a token is recorded when it is
-- redeemed, so a second redemption on any gateway replica is refused. Rows
-- expire shortly after the token itself and are purged.
CREATE TABLE IF NOT EXISTS redeemed_tokens (
    tenant_id TEXT NOT NULL,
    purpose TEXT NOT NULL,
    token_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, purpose, token_id)
);
CREATE INDEX IF NOT EXISTS redeemed_tokens_expiry_idx ON redeemed_tokens (expires_at);
ALTER TABLE redeemed_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE redeemed_tokens FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS redeemed_tokens_tenant ON redeemed_tokens;
CREATE POLICY redeemed_tokens_tenant ON redeemed_tokens
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
