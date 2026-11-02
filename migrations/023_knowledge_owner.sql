-- Knowledge ownership: a chunk belongs to the account that added it, not only to
-- the agent it was indexed for (evaluation 5.3 #4). Filtering by agent_id alone
-- let any resources:read holder read every account's retrieval corpus.
ALTER TABLE knowledge ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_knowledge_owner_created ON knowledge (owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_knowledge_owner_agent ON knowledge (owner_id, agent_id);
