-- A namespace is the durable execution and storage boundary for a project.
-- Enforce tenant-scoped uniqueness in PostgreSQL so concurrent API requests
-- cannot create two display names that normalize to the same namespace.
CREATE UNIQUE INDEX IF NOT EXISTS platform_projects_namespace_unique_idx
    ON platform_resources (tenant_id, lower(payload->>'namespace'))
    WHERE kind = 'project' AND COALESCE(payload->>'namespace', '') <> '';
