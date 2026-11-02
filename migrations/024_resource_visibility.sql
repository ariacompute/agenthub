-- Explicit visibility for a catalogue record (evaluation 5.3 #3).
--
-- Before this migration "no grants" meant "everyone with resources:read", i.e.
-- the default was open and a private resource stayed public until somebody
-- remembered to add a grant. Visibility replaces that implicit rule with an
-- explicit one:
--
--   restricted  the default. Only the owner, a granted principal, or the
--               platform owner may read or invoke the record.
--   public      deliberately published: any caller, including an anonymous A2A
--               peer, may read and invoke it. Grants still apply for write.
--
-- No backfill on purpose. An operator that upgrades a live instance decides per
-- record whether it was meant to be public; see requirements-isolation.md for
-- the upgrade checklist.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'restricted';

CREATE INDEX IF NOT EXISTS idx_resources_visibility ON resources (visibility);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'resources_visibility_check'
  ) THEN
    ALTER TABLE resources
      ADD CONSTRAINT resources_visibility_check
      CHECK (visibility IN ('public', 'restricted'));
  END IF;
END $$;
