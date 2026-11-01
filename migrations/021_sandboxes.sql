-- 021_sandboxes.sql — execution environments as first-class leases.
--
-- The control plane stops executing things itself and starts leasing somewhere
-- to execute them. Two shapes drive the schema:
--
--   * a lease is keyed by (owner, resource, scope, provider) so a router ticket's
--     steps share one environment instead of opening one container each;
--   * the audit trail stores digests, never output, so a command that printed a
--     credential does not leave it in the database.

CREATE TABLE IF NOT EXISTS sandbox_leases (
    id            TEXT PRIMARY KEY,
    owner_id      TEXT        NOT NULL DEFAULT '',
    resource_id   TEXT        NOT NULL DEFAULT '',
    scope         TEXT        NOT NULL DEFAULT '',
    provider      TEXT        NOT NULL DEFAULT '',
    runtime       TEXT        NOT NULL DEFAULT '',
    node_id       TEXT        NOT NULL DEFAULT '',
    state         TEXT        NOT NULL DEFAULT 'starting',
    ref           TEXT        NOT NULL DEFAULT '',
    profile       JSONB,
    execs         INTEGER     NOT NULL DEFAULT 0,
    last_exit     INTEGER     NOT NULL DEFAULT 0,
    last_command  TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    renewed_at    TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL
);

-- The reuse lookup: a live lease for a key, most recently renewed first.
CREATE INDEX IF NOT EXISTS idx_sandbox_leases_key
    ON sandbox_leases (owner_id, resource_id, scope, provider, state);
-- The sweep: expired leases, oldest deadline first.
CREATE INDEX IF NOT EXISTS idx_sandbox_leases_expiry
    ON sandbox_leases (state, expires_at);
CREATE INDEX IF NOT EXISTS idx_sandbox_leases_owner
    ON sandbox_leases (owner_id, created_at DESC);

CREATE TABLE IF NOT EXISTS sandbox_execs (
    id             TEXT PRIMARY KEY,
    lease_id       TEXT        NOT NULL,
    owner_id       TEXT        NOT NULL DEFAULT '',
    provider       TEXT        NOT NULL DEFAULT '',
    command        TEXT        NOT NULL DEFAULT '',
    exit_code      INTEGER     NOT NULL DEFAULT 0,
    duration_ms    BIGINT      NOT NULL DEFAULT 0,
    truncated      BOOLEAN     NOT NULL DEFAULT FALSE,
    stdout_sha256  TEXT        NOT NULL DEFAULT '',
    stderr_sha256  TEXT        NOT NULL DEFAULT '',
    dry_run        BOOLEAN     NOT NULL DEFAULT FALSE,
    at             TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sandbox_execs_lease ON sandbox_execs (lease_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_sandbox_execs_at ON sandbox_execs (at DESC);

CREATE TABLE IF NOT EXISTS sandbox_nodes (
    id            TEXT PRIMARY KEY,
    owner_id      TEXT        NOT NULL DEFAULT '',
    name          TEXT        NOT NULL DEFAULT '',
    api_key_id    TEXT        NOT NULL DEFAULT '',
    capabilities  JSONB,
    status        TEXT        NOT NULL DEFAULT 'offline',
    last_seen_at  TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sandbox_nodes_owner ON sandbox_nodes (owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sandbox_nodes_seen ON sandbox_nodes (last_seen_at DESC);

CREATE TABLE IF NOT EXISTS sandbox_jobs (
    id           TEXT PRIMARY KEY,
    node_id      TEXT        NOT NULL DEFAULT '',
    lease_id     TEXT        NOT NULL DEFAULT '',
    provider     TEXT        NOT NULL DEFAULT '',
    op           TEXT        NOT NULL DEFAULT '',
    state        TEXT        NOT NULL DEFAULT '',
    command      TEXT        NOT NULL DEFAULT '',
    exit_code    INTEGER     NOT NULL DEFAULT 0,
    error        TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sandbox_jobs_node ON sandbox_jobs (node_id, created_at DESC);

CREATE TABLE IF NOT EXISTS sandbox_events (
    seq       BIGSERIAL PRIMARY KEY,
    subject   TEXT        NOT NULL,
    kind      TEXT        NOT NULL,
    lease_id  TEXT        NOT NULL DEFAULT '',
    payload   JSONB,
    at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sandbox_events_subject ON sandbox_events (subject, seq);

-- computers gain the lease they are attached to. A computer used to be a card
-- with a session_ref nobody could attach to; now it points at a real environment
-- and knows when that environment expires.
ALTER TABLE computers ADD COLUMN IF NOT EXISTS provider   TEXT        NOT NULL DEFAULT '';
ALTER TABLE computers ADD COLUMN IF NOT EXISTS lease_id   TEXT        NOT NULL DEFAULT '';
ALTER TABLE computers ADD COLUMN IF NOT EXISTS node_id    TEXT        NOT NULL DEFAULT '';
ALTER TABLE computers ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
