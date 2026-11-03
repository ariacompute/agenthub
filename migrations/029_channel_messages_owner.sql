-- A delivery receipt belongs to whoever owns the channel it travelled through.
--
-- channel_messages was the last table of user data without an ownership column:
-- the only guard was the application layer, which works out the parent channel
-- first and never queries a receipt on its own. That is a convention, not a
-- guarantee. With owner_id here, 028's sentence ("the parent row is the gate")
-- stops being load-bearing: the database now enforces the same rule.
--
-- The backfill has to run under the process identity. channels is already under
-- FORCE row level security (028), and a migration connection is the table owner
-- but not exempt from a forced policy — without app.system the UPDATE would see
-- zero parent rows and silently backfill nothing.
SELECT set_config('app.system', 'on', true);

ALTER TABLE channel_messages ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';

-- Existing receipts inherit their channel's owner. Receipts whose channel is gone
-- keep the empty owner: they are already unreachable through the API, and the
-- policy treats an empty owner as "nobody but the process", which is correct.
UPDATE channel_messages m
   SET owner_id = c.owner_id
  FROM channels c
 WHERE c.id = m.channel_id
   AND m.owner_id = '';

CREATE INDEX IF NOT EXISTS idx_channel_messages_owner_id ON channel_messages (owner_id);

ALTER TABLE channel_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_messages FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS channel_messages_owner_access ON channel_messages;
CREATE POLICY channel_messages_owner_access ON channel_messages
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));
