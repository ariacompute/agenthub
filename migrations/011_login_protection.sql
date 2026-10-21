-- Brute-force defence for password authentication.
--
-- failed_login_attempts counts consecutive failures; locked_until is set when the
-- count reaches the threshold (see auth.Service) so the account is temporarily
-- unusable even with the right password. Both columns are added IF NOT EXISTS so
-- the migration is safe to re-run and to apply on a database created by an older
-- build.
ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_users_locked_until ON users (locked_until);
