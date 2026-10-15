-- Resource catalogue: the model / agent / mcp / skill / cli entries agenthub interconnects.
-- Idempotent: safe to re-run, and guarded by schema_migrations.
CREATE TABLE IF NOT EXISTS resources (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    endpoint     TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    repository   TEXT NOT NULL DEFAULT '',
    tags         JSONB,
    capabilities JSONB,
    auth_schemes JSONB,
    env          JSONB,
    metadata     JSONB,
    transport    TEXT NOT NULL DEFAULT '',
    health       TEXT NOT NULL DEFAULT 'unknown',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_resources_kind ON resources (kind);
CREATE INDEX IF NOT EXISTS idx_resources_health ON resources (health);
CREATE INDEX IF NOT EXISTS idx_resources_created_at ON resources (created_at);
