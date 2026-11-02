-- Task ownership: every task and every row that belongs to a run carries the id
-- of the user that started it, so a viewer can no longer read another account's
-- history (evaluation 5.3 #1).
--
-- Child tables duplicate owner_id on purpose: a row level security policy that
-- joined back to tasks on every row would turn each scan into a nested loop and
-- would also need SELECT on the table the policy is guarding. One denormalised
-- column keeps the policy a plain column comparison.
--
-- Rows written before this migration keep the empty owner: they belong to the
-- instance rather than to a person, are visible only to the platform owner (the
-- bypass identity) and are never handed out to a session.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_artifacts ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_spans ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_pending ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_push ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_tasks_owner_created ON tasks (owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_task_artifacts_owner ON task_artifacts (owner_id, task_id);
CREATE INDEX IF NOT EXISTS idx_task_spans_owner ON task_spans (owner_id, task_id);
CREATE INDEX IF NOT EXISTS idx_events_owner ON events (owner_id, task_id);

-- A child row inherits the owner of its task instead of asking every writer to
-- remember it. The alternative — deriving the owner in Go — would have to be
-- repeated in the artifact, span, event, pending and push paths, and one missed
-- call site would write a row nobody can read.
CREATE OR REPLACE FUNCTION task_child_owner() RETURNS trigger AS $$
BEGIN
  IF NEW.owner_id = '' THEN
    SELECT t.owner_id INTO NEW.owner_id FROM tasks t WHERE t.id = NEW.task_id;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS task_artifacts_owner ON task_artifacts;
CREATE TRIGGER task_artifacts_owner BEFORE INSERT ON task_artifacts
  FOR EACH ROW EXECUTE FUNCTION task_child_owner();

DROP TRIGGER IF EXISTS task_spans_owner ON task_spans;
CREATE TRIGGER task_spans_owner BEFORE INSERT ON task_spans
  FOR EACH ROW EXECUTE FUNCTION task_child_owner();

DROP TRIGGER IF EXISTS task_pending_owner ON task_pending;
CREATE TRIGGER task_pending_owner BEFORE INSERT ON task_pending
  FOR EACH ROW EXECUTE FUNCTION task_child_owner();

DROP TRIGGER IF EXISTS task_push_owner ON task_push;
CREATE TRIGGER task_push_owner BEFORE INSERT ON task_push
  FOR EACH ROW EXECUTE FUNCTION task_child_owner();

DROP TRIGGER IF EXISTS events_owner ON events;
CREATE TRIGGER events_owner BEFORE INSERT ON events
  FOR EACH ROW EXECUTE FUNCTION task_child_owner();
