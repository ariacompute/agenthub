-- Push callback audit trail: one row per callback the mock notifier captured.
-- The notifier records the URL / state / config snapshot but never performs
-- network I/O, so the history stays assertable and survives a restart.
CREATE TABLE IF NOT EXISTS push_records (
    id         BIGSERIAL PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    url        TEXT NOT NULL,
    state      TEXT NOT NULL,
    config     JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_push_records_task_created ON push_records (task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_push_records_state ON push_records (state);
CREATE INDEX IF NOT EXISTS idx_push_records_created_at ON push_records (created_at);
