-- Thread message origin: marks where a message entered the conversation (web,
-- voice, a channel). Lets the console render provenance and lets the channel and
-- speech features distinguish inbound traffic.
ALTER TABLE thread_messages ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT '';
