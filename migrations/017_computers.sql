-- Agent computers: each is a dedicated sandbox (browser / shell / screen) an agent
-- may drive. Real control is out of scope for the first release; this table records
-- the session the console may attach to (F11).
CREATE TABLE IF NOT EXISTS computers (
    id           TEXT PRIMARY KEY,
    owner_id     TEXT NOT NULL DEFAULT '',
    agent_id     TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL,
    session_ref  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'idle',
    last_action  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_computers_owner_id ON computers (owner_id);
CREATE INDEX IF NOT EXISTS idx_computers_agent_id ON computers (agent_id);
