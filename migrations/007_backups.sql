-- Database checkpoints: scheduled and manual dumps of the whole agenthub
-- schema, plus the schedule the console edits.
--
-- The archive itself lives on disk (AGENTHUB_BACKUP_DIR); these tables are the
-- catalogue and the audit trail of it. A row is written before the dump runs
-- and updated when it finishes, so an interrupted run is visible as
-- status='running' with no finished_at instead of disappearing.
CREATE TABLE IF NOT EXISTS backup_checkpoints (
    id             TEXT PRIMARY KEY,
    -- manual | schedule | pre-restore
    kind           TEXT NOT NULL,
    -- pg_dump | gonative: which engine produced the archive
    engine         TEXT NOT NULL DEFAULT '',
    -- running | ok | failed | interrupted
    status         TEXT NOT NULL,
    label          TEXT NOT NULL DEFAULT '',
    path           TEXT NOT NULL DEFAULT '',
    size_bytes     BIGINT NOT NULL DEFAULT 0,
    checksum       TEXT NOT NULL DEFAULT '',
    row_count      BIGINT NOT NULL DEFAULT 0,
    table_count    INTEGER NOT NULL DEFAULT 0,
    -- The schema_migrations files the archive was taken at, comma separated:
    -- restoring an archive that knows migrations this binary does not would
    -- write a structure the running code cannot read.
    schema_version TEXT NOT NULL DEFAULT '',
    duration_ms    BIGINT NOT NULL DEFAULT 0,
    error_code     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    -- Filled in when this checkpoint was used to restore: the audit trail of
    -- "who rolled the database back, and to what".
    restored_at    TIMESTAMPTZ,
    restored_by    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_backup_checkpoints_created ON backup_checkpoints (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_backup_checkpoints_status ON backup_checkpoints (status);

-- Single row settings table: the schedule is edited from the console, so it
-- has to survive a restart without a redeploy. id='default' is the only row.
CREATE TABLE IF NOT EXISTS backup_schedules (
    id            TEXT PRIMARY KEY,
    -- off | interval | daily | cron
    mode          TEXT NOT NULL DEFAULT 'off',
    enabled       BOOLEAN NOT NULL DEFAULT false,
    interval_sec  BIGINT NOT NULL DEFAULT 86400,
    -- "HH:MM" interpreted in timezone
    daily_time    TEXT NOT NULL DEFAULT '03:00',
    timezone      TEXT NOT NULL DEFAULT 'UTC',
    cron_expr     TEXT NOT NULL DEFAULT '',
    keep_last     INTEGER NOT NULL DEFAULT 7,
    max_age_days  INTEGER NOT NULL DEFAULT 30,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by    TEXT NOT NULL DEFAULT ''
);

INSERT INTO backup_schedules (id, mode, enabled) VALUES ('default', 'off', false)
ON CONFLICT (id) DO NOTHING;

-- Backups are destructive to restore, so they are their own permission rather
-- than an implicit side effect of roles:manage. The admin role gets it here so
-- an existing instance keeps working after the upgrade; operator and viewer
-- deliberately do not.
INSERT INTO permissions (code, description) VALUES
    ('backups:manage', 'Create, download, delete and restore database checkpoints')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission) VALUES
    ('role-admin', 'backups:manage')
ON CONFLICT (role_id, permission) DO NOTHING;
