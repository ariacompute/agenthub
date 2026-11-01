-- Router: the scheduler's two layer state. A trigger is one inbound fact, a ticket
-- one unit of work, an assignment one run of that work, a decision the verdict in
-- between. Every hop is a row so "why did this happen" stays answerable.
-- Idempotent: safe to replay on every startup.
CREATE TABLE IF NOT EXISTS router_triggers (
    id           TEXT PRIMARY KEY,
    source       TEXT NOT NULL,
    source_id    TEXT NOT NULL DEFAULT '',
    thread_id    TEXT NOT NULL DEFAULT '',
    actor_id     TEXT NOT NULL DEFAULT '',
    actor_name   TEXT NOT NULL DEFAULT '',
    actor_kind   TEXT NOT NULL DEFAULT 'system',
    kind         TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    explicit     BOOLEAN NOT NULL DEFAULT FALSE,
    meta         JSONB,
    dedup_key    TEXT NOT NULL DEFAULT '',
    disposition  TEXT NOT NULL DEFAULT 'pending',
    decision_id  TEXT NOT NULL DEFAULT '',
    ticket_id    TEXT NOT NULL DEFAULT '',
    handbacks    INTEGER NOT NULL DEFAULT 0,
    due_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Partial unique index: an empty key means "do not deduplicate", and many rows
-- may carry one.
CREATE UNIQUE INDEX IF NOT EXISTS idx_router_triggers_dedup ON router_triggers (dedup_key) WHERE dedup_key <> '';

CREATE INDEX IF NOT EXISTS idx_router_triggers_due ON router_triggers (disposition, due_at);

CREATE INDEX IF NOT EXISTS idx_router_triggers_thread ON router_triggers (thread_id);

CREATE INDEX IF NOT EXISTS idx_router_triggers_ticket ON router_triggers (ticket_id);

CREATE INDEX IF NOT EXISTS idx_router_triggers_at ON router_triggers (at);

CREATE TABLE IF NOT EXISTS router_threads (
    id            TEXT PRIMARY KEY,
    source        TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    opened_by     TEXT NOT NULL DEFAULT '',
    closed        BOOLEAN NOT NULL DEFAULT FALSE,
    close_outcome TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_router_threads_opened_by ON router_threads (opened_by);

CREATE TABLE IF NOT EXISTS router_subscriptions (
    thread_id  TEXT NOT NULL,
    ticket_id  TEXT NOT NULL,
    own        BOOLEAN NOT NULL DEFAULT FALSE,
    context    BOOLEAN NOT NULL DEFAULT FALSE,
    closed     BOOLEAN NOT NULL DEFAULT FALSE,
    at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (thread_id, ticket_id)
);

CREATE INDEX IF NOT EXISTS idx_router_subscriptions_ticket ON router_subscriptions (ticket_id);

CREATE TABLE IF NOT EXISTS router_tickets (
    id                TEXT PRIMARY KEY,
    title             TEXT NOT NULL DEFAULT '',
    goal              TEXT NOT NULL DEFAULT '',
    owner_id          TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL,
    resource          TEXT NOT NULL DEFAULT '',
    task_id           TEXT NOT NULL DEFAULT '',
    origin_trigger_id TEXT NOT NULL DEFAULT '',
    batch_id          TEXT NOT NULL DEFAULT '',
    focus             TEXT NOT NULL DEFAULT '',
    attempts          INTEGER NOT NULL DEFAULT 0,
    next_try_at       TIMESTAMPTZ,
    last_error        TEXT NOT NULL DEFAULT '',
    waits_for_person  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_router_tickets_state ON router_tickets (state);

CREATE INDEX IF NOT EXISTS idx_router_tickets_created_at ON router_tickets (created_at);

CREATE INDEX IF NOT EXISTS idx_router_tickets_origin ON router_tickets (origin_trigger_id);

CREATE INDEX IF NOT EXISTS idx_router_tickets_owner ON router_tickets (owner_id);

CREATE TABLE IF NOT EXISTS router_assignments (
    id         TEXT PRIMARY KEY,
    ticket_id  TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'started',
    task_id    TEXT NOT NULL DEFAULT '',
    note       TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    trigger_id TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_router_assignments_ticket ON router_assignments (ticket_id, started_at);

CREATE TABLE IF NOT EXISTS router_decisions (
    id         TEXT PRIMARY KEY,
    trigger_id TEXT NOT NULL,
    batch_id   TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL,
    ticket_id  TEXT NOT NULL DEFAULT '',
    resource   TEXT NOT NULL DEFAULT '',
    by         TEXT NOT NULL DEFAULT 'rule',
    reason     TEXT NOT NULL DEFAULT '',
    at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_router_decisions_trigger ON router_decisions (trigger_id);

CREATE INDEX IF NOT EXISTS idx_router_decisions_ticket ON router_decisions (ticket_id);

CREATE INDEX IF NOT EXISTS idx_router_decisions_batch ON router_decisions (batch_id);

CREATE TABLE IF NOT EXISTS router_policies (
    source      TEXT PRIMARY KEY,
    mode        TEXT NOT NULL DEFAULT 'candidate',
    allow       JSONB,
    keywords    JSONB,
    quiet_start INTEGER NOT NULL DEFAULT -1,
    quiet_end   INTEGER NOT NULL DEFAULT -1,
    resource    TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS router_events (
    seq     BIGSERIAL PRIMARY KEY,
    kind    TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload JSONB
);

CREATE INDEX IF NOT EXISTS idx_router_events_subject ON router_events (subject, seq);

CREATE INDEX IF NOT EXISTS idx_router_events_at ON router_events (at);
