-- Event stream: every status / artifact / message / span frame broadcast for a
-- task, persisted so a client that reconnects with since=<seq> can replay what
-- it missed. seq is assigned by the broker before fan-out, so the pair
-- (task_id, seq) is unique and monotonic within a task.
CREATE TABLE IF NOT EXISTS events (
    seq        BIGINT NOT NULL,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL DEFAULT '',
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_events_task_seq ON events (task_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events (kind);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events (created_at);
