-- Task lifecycle: the A2A task plus its artifacts, span timeline, parked
-- question and push notification configuration.
-- Child rows cascade with the task so a deleted task leaves nothing behind.
CREATE TABLE IF NOT EXISTS tasks (
    id               TEXT PRIMARY KEY,
    kind             TEXT NOT NULL DEFAULT '',
    context_id       TEXT NOT NULL DEFAULT '',
    session_id       TEXT NOT NULL DEFAULT '',
    resource_id      TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL,
    status_message   JSONB,
    status_timestamp TEXT NOT NULL DEFAULT '',
    history          JSONB,
    metadata         JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks (state);
CREATE INDEX IF NOT EXISTS idx_tasks_resource ON tasks (resource_id);
CREATE INDEX IF NOT EXISTS idx_tasks_context ON tasks (context_id);
CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks (created_at);

CREATE TABLE IF NOT EXISTS task_artifacts (
    id          BIGSERIAL PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    artifact_id TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    parts       JSONB,
    idx         INTEGER NOT NULL DEFAULT 0,
    append      BOOLEAN,
    last_chunk  BOOLEAN,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_task_artifacts_task ON task_artifacts (task_id, id);

CREATE TABLE IF NOT EXISTS task_spans (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL DEFAULT '',
    label       TEXT NOT NULL DEFAULT '',
    start_at    TEXT NOT NULL DEFAULT '',
    end_at      TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT '',
    code        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_task_spans_task ON task_spans (task_id, created_at);
CREATE INDEX IF NOT EXISTS idx_task_spans_status ON task_spans (status);

CREATE TABLE IF NOT EXISTS task_pending (
    task_id    TEXT PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    prompt     TEXT NOT NULL DEFAULT '',
    asked_at   TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS task_push (
    task_id    TEXT PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    config     JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
