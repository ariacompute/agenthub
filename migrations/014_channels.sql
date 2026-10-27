-- Channels: external messaging surfaces (Slack / Teams / Telegram / SMS) the
-- console binds to a thread. The signing secret is never stored; only the name of
-- the environment variable that holds it at call time.
CREATE TABLE IF NOT EXISTS channels (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    config          JSONB NOT NULL DEFAULT '{}'::jsonb,
    signing_key_env TEXT NOT NULL DEFAULT '',
    thread_id       TEXT NOT NULL DEFAULT '',
    owner_id        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS channel_messages (
    id           BIGSERIAL PRIMARY KEY,
    channel_id   TEXT NOT NULL,
    external_ref TEXT NOT NULL DEFAULT '',
    thread_id    TEXT NOT NULL DEFAULT '',
    task_id      TEXT NOT NULL DEFAULT '',
    direction    TEXT NOT NULL,
    payload      JSONB NOT NULL DEFAULT '{}'::jsonb,
    status       TEXT NOT NULL DEFAULT 'received',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS channel_messages_external_ref_key ON channel_messages (channel_id, external_ref);
CREATE INDEX IF NOT EXISTS idx_channels_thread_id ON channels (thread_id);
CREATE INDEX IF NOT EXISTS idx_channel_messages_channel_id ON channel_messages (channel_id);
