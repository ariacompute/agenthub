-- Shared files: the metadata row for a blob a person uploaded.
--
-- The bytes themselves are not here. They live behind a backend (the instance's
-- disk by default, an S3-compatible store when configured) and this row holds the
-- address plus everything the console needs to list and serve them. Keeping the
-- bytes out of the database is what lets the same API work against RustFS, MinIO
-- or a plain directory without a schema change.
--
-- Every row belongs to exactly one owner: a file is context for that account's
-- tasks and nobody else's. The policy below is the database half of that rule,
-- and internal/store keeps the application half.
CREATE TABLE IF NOT EXISTS shared_files (
  id         TEXT PRIMARY KEY,
  owner_id   TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL,
  mime       TEXT NOT NULL DEFAULT '',
  -- BIGINT: a recording or a deck is measured in megabytes, and an int column
  -- would cap a file at 2 GiB without ever saying so.
  size       BIGINT NOT NULL DEFAULT 0,
  sha256     TEXT NOT NULL DEFAULT '',
  backend    TEXT NOT NULL DEFAULT '',
  object_key TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_shared_files_owner_id ON shared_files (owner_id);
CREATE INDEX IF NOT EXISTS idx_shared_files_created_at ON shared_files (created_at DESC);

ALTER TABLE shared_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE shared_files FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS shared_files_owner_access ON shared_files;
CREATE POLICY shared_files_owner_access ON shared_files
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));
