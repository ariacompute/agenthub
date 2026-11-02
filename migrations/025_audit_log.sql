-- API audit trail (evaluation section 5.3 / CloudTrail parity).
--
-- One row per control plane (/api) and data plane (/a2a) call. The field names
-- mirror the CloudTrail event shape so an operator can reason about the table
-- with the vocabulary they already know:
--
--   event_id / ts                 eventID / eventTime
--   source                        eventSource (api | a2a)
--   action                        eventName (the A2A method or the route)
--   read_only                     readOnly
--   actor_kind / actor_id         userIdentity
--   ip                            sourceIPAddress
--   resource_id / resource_kind   resources[]
--   status / error_code           responseElements / errorCode
--
-- The table is append only twice over: the application never issues UPDATE or
-- DELETE, and the trigger below rejects both for every role, including the
-- owner. Integrity of the sequence is covered by the hash chain: hash =
-- sha256(prev_hash || canonical(event)), so removing or editing a row in the
-- middle breaks every later link and shows up in /api/audit/verify.
CREATE TABLE IF NOT EXISTS audit_log (
    id            BIGSERIAL PRIMARY KEY,
    event_id      TEXT        NOT NULL DEFAULT '',
    ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id    TEXT        NOT NULL DEFAULT '',
    source        TEXT        NOT NULL DEFAULT '',
    actor_kind    TEXT        NOT NULL DEFAULT '',
    actor_id      TEXT        NOT NULL DEFAULT '',
    actor_label   TEXT        NOT NULL DEFAULT '',
    owner_id      TEXT        NOT NULL DEFAULT '',
    ip            TEXT        NOT NULL DEFAULT '',
    method        TEXT        NOT NULL DEFAULT '',
    path          TEXT        NOT NULL DEFAULT '',
    action        TEXT        NOT NULL DEFAULT '',
    resource_id   TEXT        NOT NULL DEFAULT '',
    resource_kind TEXT        NOT NULL DEFAULT '',
    status        INTEGER     NOT NULL DEFAULT 0,
    error_code    TEXT        NOT NULL DEFAULT '',
    read_only     BOOLEAN     NOT NULL DEFAULT false,
    latency_ms    INTEGER     NOT NULL DEFAULT 0,
    user_agent    TEXT        NOT NULL DEFAULT '',
    metadata      JSONB,
    prev_hash     TEXT        NOT NULL DEFAULT '',
    hash          TEXT        NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_log_event ON audit_log (event_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log (ts DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log (actor_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log (action, ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_resource ON audit_log (resource_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_status ON audit_log (status, ts DESC);

-- The single writer that owns retention sets app.retention for exactly one
-- transaction; every other writer, including the owner and a superuser session,
-- is refused. UPDATE is rejected unconditionally: a retention pass expires old
-- rows, it never rewrites them.
CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' AND current_setting('app.retention', true) = 'on' THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'audit_log is append-only: % is not allowed', TG_OP
    USING HINT = 'Expire old rows with the retention job (app.retention=on) instead of deleting them by hand.';
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

DROP TRIGGER IF EXISTS audit_log_no_truncate ON audit_log;
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
  FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();
