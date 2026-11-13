-- 033_audit_chain_anchor.sql
--
-- Retention expires the oldest audit rows, but the row that survives as the new
-- head still carries the removed tail's hash in prev_hash. A verifier starting
-- from an empty seed reports that healthy, pruned chain as broken at its very
-- first row. Keep the removed tail here so /api/audit/verify has a trusted seed.
--
-- Single row by construction: id is pinned to 1 and the retention pass upserts
-- it in the same transaction as the delete. There is no HTTP surface; the only
-- writer is internal/store's Prune, running under the retention identity.
CREATE TABLE IF NOT EXISTS audit_chain_anchor (
  id        SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  hash      TEXT        NOT NULL,
  event_id  TEXT        NOT NULL DEFAULT '',
  pruned_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE audit_chain_anchor ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_chain_anchor FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS audit_chain_anchor_read ON audit_chain_anchor;
CREATE POLICY audit_chain_anchor_read ON audit_chain_anchor FOR SELECT
  USING (
    current_setting('app.system', true) = 'on'
    OR current_setting('app.bypass', true) = 'on'
    OR current_setting('app.audit_read', true) = 'on'
  );

-- Only the retention job may move the anchor: it is the one path allowed to
-- delete audit rows, and the anchor must move with that delete.
DROP POLICY IF EXISTS audit_chain_anchor_retention ON audit_chain_anchor;
CREATE POLICY audit_chain_anchor_retention ON audit_chain_anchor FOR ALL
  USING (current_setting('app.retention', true) = 'on')
  WITH CHECK (current_setting('app.retention', true) = 'on');
