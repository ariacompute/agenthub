-- Threads join the row level security perimeter, and their message log gets the
-- same ownership column channel_messages received in 029.
--
-- threads has carried owner_id since 012 — the gap was never the column, it was
-- that no policy read it, so every by-id lookup was guarded by one WHERE clause
-- in internal/store and nothing else. thread_messages had neither.
--
-- The backfill runs under the process identity for the same reason as 029: once
-- threads is forced, a migration connection that does not announce itself as the
-- process sees no parent rows and backfills nothing.
SELECT set_config('app.system', 'on', true);

ALTER TABLE threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE threads FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS threads_owner_access ON threads;
CREATE POLICY threads_owner_access ON threads
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE thread_messages ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';

UPDATE thread_messages m
   SET owner_id = t.owner_id
  FROM threads t
 WHERE t.id = m.thread_id
   AND m.owner_id = '';

CREATE INDEX IF NOT EXISTS idx_thread_messages_owner_id ON thread_messages (owner_id);

ALTER TABLE thread_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE thread_messages FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS thread_messages_owner_access ON thread_messages;
CREATE POLICY thread_messages_owner_access ON thread_messages
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));
