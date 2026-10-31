-- Schedules: goals the hub works on proactively, on a cron or an external trigger.
-- The first release runs the scheduler in-process as a mock; the cron expression is
-- stored for a future real scheduler (F13).
CREATE TABLE IF NOT EXISTS schedules (
    id           TEXT PRIMARY KEY,
    goal         TEXT NOT NULL DEFAULT '',
    trigger      TEXT NOT NULL DEFAULT 'manual',
    cron_expr    TEXT NOT NULL DEFAULT '',
    enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    next_run     TIMESTAMPTZ,
    owner_id     TEXT NOT NULL DEFAULT '',
    last_run_at  TIMESTAMPTZ,
    last_status  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_schedules_owner_id ON schedules (owner_id);
