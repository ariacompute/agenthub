-- Row level security: the database is the last line of defence (evaluation
-- section 2 / 5.3 #1 and #4).
--
-- The application layer already filters by owner. RLS makes the guarantee
-- independent of that code path: a query that forgets a WHERE clause, a future
-- handler, or a psql session using the runtime role all see only rows the
-- session identity is allowed to see.
--
-- Identity is injected per transaction by internal/store (set_config(..., true)):
--
--   app.system     'on'  a process owned path: task state machine, event
--                        broker, seed, migration, backup, audit writer.
--   app.bypass     'on'  the platform owner / an administrator acting on any row.
--   app.owner_id   <id>  the signed in user; the row owner must match exactly.
--   app.audit_read 'on'  a principal holding audit:read, for audit_log only.
--   app.retention  'on'  the audit retention job, the only DELETE allowed.
--
-- Unset settings read as NULL, every comparison yields NULL and NULL counts as
-- false in a policy, so a connection with no identity sees nothing. That is the
-- fail-closed default: forgetting to scope a request denies it instead of
-- opening it.
CREATE OR REPLACE FUNCTION agenthub_row_visible(row_owner TEXT) RETURNS boolean AS $$
  SELECT current_setting('app.system', true) = 'on'
      OR current_setting('app.bypass', true) = 'on'
      OR (row_owner <> '' AND row_owner = current_setting('app.owner_id', true))
$$ LANGUAGE sql STABLE;

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tasks_owner_access ON tasks;
CREATE POLICY tasks_owner_access ON tasks
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE task_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_artifacts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS task_artifacts_owner_access ON task_artifacts;
CREATE POLICY task_artifacts_owner_access ON task_artifacts
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE task_spans ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_spans FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS task_spans_owner_access ON task_spans;
CREATE POLICY task_spans_owner_access ON task_spans
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE task_pending ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_pending FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS task_pending_owner_access ON task_pending;
CREATE POLICY task_pending_owner_access ON task_pending
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE task_push ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_push FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS task_push_owner_access ON task_push;
CREATE POLICY task_push_owner_access ON task_push
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE events ENABLE ROW LEVEL SECURITY;
ALTER TABLE events FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS events_owner_access ON events;
CREATE POLICY events_owner_access ON events
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

ALTER TABLE knowledge ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS knowledge_owner_access ON knowledge;
CREATE POLICY knowledge_owner_access ON knowledge
  USING (agenthub_row_visible(owner_id))
  WITH CHECK (agenthub_row_visible(owner_id));

-- The audit trail is readable only with a dedicated setting, so a normal
-- request scope never sees it; only the writer (system) may append.
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS audit_log_read ON audit_log;
CREATE POLICY audit_log_read ON audit_log FOR SELECT
  USING (
    current_setting('app.system', true) = 'on'
    OR current_setting('app.bypass', true) = 'on'
    OR current_setting('app.audit_read', true) = 'on'
  );
DROP POLICY IF EXISTS audit_log_append ON audit_log;
CREATE POLICY audit_log_append ON audit_log FOR INSERT
  WITH CHECK (
    current_setting('app.system', true) = 'on'
    OR current_setting('app.bypass', true) = 'on'
  );
DROP POLICY IF EXISTS audit_log_retention ON audit_log;
CREATE POLICY audit_log_retention ON audit_log FOR DELETE
  USING (current_setting('app.retention', true) = 'on');
