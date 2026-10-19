-- Upstream registry synchronisation: the sources agenthub pulls MCP server and
-- skill records from, plus the outcome of the last run.
--
-- A source is metadata only. The token itself lives in the environment named by
-- auth_token_env and is never written to the database, the API or the log.
CREATE TABLE IF NOT EXISTS registry_sources (
    id               TEXT PRIMARY KEY,
    namespace        TEXT NOT NULL DEFAULT 'default',
    -- mcp | skill
    kind             TEXT NOT NULL,
    name             TEXT NOT NULL,
    url              TEXT NOT NULL,
    -- none | bearer
    auth_scheme      TEXT NOT NULL DEFAULT 'none',
    auth_token_env   TEXT NOT NULL DEFAULT '',
    -- true keeps the source on the offline, deterministic client: no network.
    fixture          BOOLEAN NOT NULL DEFAULT false,
    enabled          BOOLEAN NOT NULL DEFAULT true,
    -- Optional 5 field cron expression for scheduled runs.
    schedule         TEXT NOT NULL DEFAULT '',
    last_sync_at     TIMESTAMPTZ,
    -- ok | failed
    last_sync_status TEXT NOT NULL DEFAULT '',
    last_sync_count  INTEGER NOT NULL DEFAULT 0,
    last_sync_error  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_registry_sources_namespace ON registry_sources (namespace);
CREATE INDEX IF NOT EXISTS idx_registry_sources_kind ON registry_sources (kind);
CREATE INDEX IF NOT EXISTS idx_registry_sources_enabled ON registry_sources (enabled);
