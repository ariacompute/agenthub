-- WeChat web login (snsapi_login) never returns an email address, so a WeChat
-- account is keyed only by its (provider, subject) binding in oauth_accounts.
-- Allow users.email to be NULL so such accounts can be stored; Postgres UNIQUE
-- indexes treat NULLs as distinct, so multiple email-less users do not collide.
-- Idempotent: re-running on an already-nullable column is a no-op.
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
