-- One display name per resource: Entry.display_name is the single source of
-- truth. F10 kept a second label inside persona.displayName; fold any legacy
-- value into the column before dropping the key, so an upgraded instance does
-- not lose a label an operator already chose.
--
-- Every statement is idempotent: migration files are replayed in full at each
-- startup. The backfill only fills an EMPTY column, so a label written through
-- the new contract always wins over a stale persona value.
UPDATE resources
   SET display_name = persona ->> 'displayName'
 WHERE COALESCE(display_name, '') = ''
   AND persona ? 'displayName'
   AND COALESCE(persona ->> 'displayName', '') <> '';

UPDATE resources
   SET persona = persona - 'displayName'
 WHERE persona ? 'displayName';

-- A persona that consisted only of the old label is now empty; keep the column
-- NULL, exactly like normalizePersona does for an all-empty persona.
UPDATE resources
   SET persona = NULL
 WHERE persona = '{}'::jsonb;
