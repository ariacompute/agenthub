-- agentbuddy collaboration threads: the conversation space and its message log.
-- A thread is owned by one principal; messages are either human turns or agent
-- replies, each agent reply linked back to the A2A task that produced it.
-- Idempotent: safe to replay on every startup.
CREATE TABLE IF NOT EXISTS threads (
    id            TEXT PRIMARY KEY,
    title         TEXT NOT NULL DEFAULT 'New thread',
    owner_id      TEXT NOT NULL,
    participants  JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_threads_owner ON threads (owner_id);
CREATE INDEX IF NOT EXISTS idx_threads_updated_at ON threads (updated_at);

CREATE TABLE IF NOT EXISTS thread_messages (
    id           BIGSERIAL PRIMARY KEY,
    thread_id    TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    author_type  TEXT NOT NULL,
    author_id    TEXT NOT NULL,
    author_name  TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    task_id      TEXT,
    resource_id  TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_thread_messages_thread ON thread_messages (thread_id, id);
CREATE INDEX IF NOT EXISTS idx_thread_messages_task ON thread_messages (task_id);
