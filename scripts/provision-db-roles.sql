-- Least-privilege database roles for agenthub, split around row level security.
--
-- Run this once, as a superuser, in the agenthub database (or in each tenant
-- database). Three roles:
--
--   agenthub_migrator  owns the schema and runs DDL. Only the startup migration
--                      connection uses it (AGENTHUB_MIGRATE_DATABASE_URL).
--   agenthub_app       the runtime identity in DATABASE_URL. DML only, no
--                      CREATE, not the owner of any table, and explicitly NOT
--                      BYPASSRLS — an owner or a BYPASSRLS role skips the
--                      policies, which is exactly what we are trying to prevent.
--   agenthub_readonly  SELECT only, for operators, analytics and restore
--                      verification. It does not bypass RLS either, so it sees
--                      whatever the session settings allow and nothing more.
--
-- Order matters: create the roles, then run agenthub once with
-- AGENTHUB_MIGRATE_DATABASE_URL pointing at agenthub_migrator so the tables and
-- the policies exist, then apply the GRANTs at the bottom (or run this file
-- again — every statement is idempotent).
--
-- Adjust the database name and passwords to your environment, and prefer letting
-- the application read credentials from a secrets manager over inlining them.

\set db agenthub

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'agenthub_migrator') THEN
    CREATE ROLE agenthub_migrator LOGIN PASSWORD 'REPLACE_WITH_STRONG_MIGRATOR_PASSWORD';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'agenthub_app') THEN
    CREATE ROLE agenthub_app LOGIN PASSWORD 'REPLACE_WITH_STRONG_APP_PASSWORD';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'agenthub_readonly') THEN
    CREATE ROLE agenthub_readonly LOGIN PASSWORD 'REPLACE_WITH_STRONG_READONLY_PASSWORD';
  END IF;
END $$;

-- A role can only be de-privileged, never re-privileged by accident: assert the
-- two properties the policies depend on.
ALTER ROLE agenthub_app NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE;
ALTER ROLE agenthub_readonly NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE;

GRANT CONNECT ON DATABASE :"db" TO agenthub_migrator, agenthub_app, agenthub_readonly;

-- 1. The migrator owns the schema, so every table it creates is owned by it and
--    the runtime role is never a table owner.
ALTER SCHEMA public OWNER TO agenthub_migrator;

-- 2. Future objects created by the migrator are readable/writable by the app.
ALTER DEFAULT PRIVILEGES FOR ROLE agenthub_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO agenthub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE agenthub_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO agenthub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE agenthub_migrator IN SCHEMA public
  GRANT SELECT ON TABLES TO agenthub_readonly;

-- 3. The same for objects that already exist.
GRANT USAGE ON SCHEMA public TO agenthub_app, agenthub_readonly;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO agenthub_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO agenthub_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO agenthub_readonly;

-- 4. The audit trail is append only for everyone, including the runtime role.
--    The trigger in 025_audit_log.sql is the belt; this is the braces. A
--    retention pass is expected to run as the migrator role with app.retention
--    set for the transaction.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM agenthub_app;
GRANT INSERT, SELECT ON audit_log TO agenthub_app;
GRANT SELECT ON audit_log TO agenthub_readonly;

-- 5. Row level security needs a policy on every guarded table. Refuse to call
--    this a finished provision while one is missing.
DO $$
DECLARE
  missing TEXT;
BEGIN
  SELECT string_agg(t, ', ')
    INTO missing
    FROM (VALUES ('tasks'), ('task_artifacts'), ('task_spans'), ('task_pending'),
                 ('task_push'), ('events'), ('knowledge'), ('audit_log')) AS v(t)
   WHERE NOT EXISTS (
     SELECT 1 FROM pg_class c
      WHERE c.relname = v.t AND c.relrowsecurity AND c.relforcerowsecurity
   );
  IF missing IS NOT NULL THEN
    RAISE EXCEPTION 'row level security is not enforced on: %', missing
      USING HINT = 'Start agenthub once with AGENTHUB_MIGRATE_DATABASE_URL so 026_rls_policies.sql runs.';
  END IF;
END $$;
