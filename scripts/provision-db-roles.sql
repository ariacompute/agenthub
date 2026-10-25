-- Least-privilege database roles for agenthub.
--
-- Run this once, as a superuser, in the agenthub database (or in each tenant
-- database). It creates two roles:
--
--   app       – the runtime identity used by DATABASE_URL. It owns the schema and
--               may run DDL, because migrations run on this connection at startup.
--               Outside of migrations it performs the usual DML (SELECT/INSERT/
--               UPDATE/DELETE, plus sequence nextval) on the application tables.
--   readonly  – for operators / analytics / backup verification. SELECT only.
--
-- If you would rather keep the runtime role DML-only, run the migrations once
-- with a separate superuser (or the postgres role) before starting agenthub, then
-- grant `app` only the DML privileges below.
--
-- Adjust the database name and passwords to your environment. Prefer letting the
-- application derive credentials from a secrets manager rather than inlining.

\set db agenthub

-- 1. App: runtime role; owns the schema so it can run migrations at startup.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app') THEN
    CREATE ROLE app LOGIN PASSWORD 'REPLACE_WITH_STRONG_APP_PASSWORD';
  END IF;
END $$;

-- 2. Readonly operator role.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'readonly') THEN
    CREATE ROLE readonly LOGIN PASSWORD 'REPLACE_WITH_STRONG_READONLY_PASSWORD';
  END IF;
END $$;

GRANT CONNECT ON DATABASE :"db" TO app, readonly;

-- The app owns the schema and all current + future objects, so migrations can
-- create/alter/drop tables on the runtime connection.
ALTER SCHEMA public OWNER TO app;
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
