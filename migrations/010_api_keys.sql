-- API keys: long-lived bearer credentials for programmatic access.
--
-- A key stands in for its owner but carries only the permission subset chosen at
-- creation, so it can never exceed what the owner could do themselves. The raw
-- value is shown exactly once and never stored: only its sha256 (token_hash) and
-- a short display prefix (ahk_xxxxxxxx) land in the database, mirroring how
-- sessions keep only the hash of the cookie value.
CREATE TABLE IF NOT EXISTS api_keys (
    id           TEXT PRIMARY KEY,
    -- Owning user. Cascade so deleting a user takes its keys with it, keeping
    -- the audit trail consistent without orphaned credentials.
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Human label; not unique, several keys may share a purpose.
    name         TEXT NOT NULL DEFAULT '',
    -- sha256 of the opaque raw value. Unique so a collision is impossible and a
    -- lookup by hash is a single index hit.
    token_hash   TEXT NOT NULL UNIQUE,
    -- "ahk_" + first 8 chars of the raw value, for display so a user can tell
    -- keys apart without ever exposing the secret.
    prefix       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    -- NULL means "never expires"; a set time turns the key inert.
    expires_at   TIMESTAMPTZ,
    -- Soft delete: a revoked key keeps its row (audit trail) but is unusable.
    revoked_at   TIMESTAMPTZ,
    -- Who issued the key; an admin acting on another user's behalf is recorded.
    created_by   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys (user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_created ON api_keys (created_at DESC);

-- The permission subset of a key, mirroring role_permissions so the model stays
-- consistent: a key is a small, fixed bundle of capabilities.
CREATE TABLE IF NOT EXISTS api_key_scopes (
    key_id      TEXT NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
    permission  TEXT NOT NULL,
    PRIMARY KEY (key_id, permission)
);

-- API keys are their own permission rather than an implicit side effect of
-- roles:manage, because a key can read and act but must not silently grant the
-- power to issue more keys. The admin role gets it here so an existing instance
-- keeps working after the upgrade; operator and viewer deliberately do not.
INSERT INTO permissions (code, description) VALUES
    ('apikeys:manage', 'List, create, rotate and revoke API keys for any user')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission) VALUES
    ('role-admin', 'apikeys:manage')
ON CONFLICT (role_id, permission) DO NOTHING;
