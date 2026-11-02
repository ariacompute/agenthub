-- Row level security for the three per-owner surfaces that had none.
--
-- 026 covered the task pipeline (tasks, artifacts, spans, pending, push, events)
-- plus knowledge and the audit log. channels, schedules and computers were left
-- out, and they were exactly the three tables whose by-id endpoints looked rows
-- up by id alone while the list endpoints filtered by owner. A forgotten WHERE
-- clause therefore had nothing behind it: the application layer was the only
-- guard, and on these three endpoints it was not there.
--
-- The policy is the same predicate 026 introduced, so there is one definition of
-- "may this row be seen": app.system (the process acting as itself), app.bypass
-- (AGENTHUB_RLS=off), or an exact, non-empty owner match.
--
-- channel_messages is deliberately not covered here. A receipt is only reached
-- through its channel, and handleListDeliveries now fetches the channel through
-- the owner scoped lookup first, so the parent row is the gate.
--
-- The inbound webhook is the one caller that reads a channel without an owner:
-- it must read the row to learn which secret verifies the body, before any
-- session can exist. It goes through channels.Store.GetUnscoped with the process
-- identity (app.system), which is the same shape the audit writer uses. Every
-- other statement against these tables carries an owner.

ALTER TABLE channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE channels FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS channels_owner_access ON channels;
CREATE POLICY channels_owner_access ON channels
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedules FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS schedules_owner_access ON schedules;
CREATE POLICY schedules_owner_access ON schedules
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE computers ENABLE ROW LEVEL SECURITY;
ALTER TABLE computers FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS computers_owner_access ON computers;
CREATE POLICY computers_owner_access ON computers
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));
