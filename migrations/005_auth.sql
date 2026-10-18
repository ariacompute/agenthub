-- Identity layer: users, server-side sessions and third-party account bindings.
--
-- Sessions are stored server side so an administrator can revoke one (kick a
-- user) without waiting for a token to expire. Only derived values are kept:
-- password_hash carries the salt and the iteration count, and sessions only
-- store the hash of the opaque cookie value, never the cookie itself.
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    -- Empty for accounts that only ever log in through OAuth.
    password_hash TEXT NOT NULL DEFAULT '',
    -- active | disabled; a disabled user keeps its rows (tasks, grants) but can
    -- no longer authenticate.
    status        TEXT NOT NULL DEFAULT 'active',
    -- The bootstrap administrator: always keeps every permission.
    is_owner      BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_users_status ON users (status);
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users (created_at);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_agent   TEXT NOT NULL DEFAULT '',
    ip           TEXT NOT NULL DEFAULT '',
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_revoked ON sessions (revoked_at) WHERE revoked_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS oauth_accounts (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL,
    email      TEXT NOT NULL DEFAULT '',
    profile    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);

CREATE INDEX IF NOT EXISTS idx_oauth_accounts_user ON oauth_accounts (user_id);
