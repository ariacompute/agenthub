-- Least-privilege database roles for agenthub.
--
-- Run this once, as a superuser, in the agenthub database (or in each tenant
-- database). It creates three roles:
--
--   app       – the runtime identity used by DATABASE_URL. It owns no schema
--               objects and can only perform DML (SELECT/INSERT/UPDATE/DELETE,
--               plus sequence nextval) on the application tables. It cannot
--               CREATE/DROP/ALTER, so a compromised app connection cannot change
--               the schema.
--   migrator  – used only by AGENTHUB_MIGRATOR_DATABASE_URL at startup. It owns
--               the schema and may run DDL, but does not serve traffic.
--   readonly  – for operators / analytics / backup verification. SELECT only.
--
-- Adjust the database name and passwords to your environment. Prefer letting the
-- application derive credentials from a secrets manager rather than inlining.

\set db agenthub

-- 1. Migrator: schema owner. Created first so it can own the objects below.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'migrator') THEN
    CREATE ROLE migrator LOGIN PASSWORD 'REPLACE_WITH_STRONG_MIGRATOR_PASSWORD';
  END IF;
END $$;

-- 2. App: least-privilege runtime role (no login password here is optional; the
--    app authenticates with its own credential).
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app') THEN
    CREATE ROLE app LOGIN PASSWORD 'REPLACE_WITH_STRONG_APP_PASSWORD';
  END IF;
END $$;

-- 3. Readonly operator role.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'readonly') THEN
    CREATE ROLE readonly LOGIN PASSWORD 'REPLACE_WITH_STRONG_READONLY_PASSWORD';
  END IF;
END $$;

GRANT CONNECT ON DATABASE :"db" TO app, migrator, readonly;

-- The migrator owns the schema and all current + future objects.
ALTER SCHEMA public OWNER TO migrator;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO readonly;

-- Apply the same grants to objects that already exist.
GRANT USAGE ON SCHEMA public TO app, readonly;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO readonly;

-- The app role receives no DDL privileges at all; only migrator may CREATE,
-- ALTER or DROP. Nothing here grants those to app, so the grant set above is the
-- complete surface of what a runtime connection can do.
