-- Persona: the optional human-facing presentation of a resource (display label,
-- tagline, avatar). Descriptive only; nothing here changes routing or auth.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS persona JSONB;
