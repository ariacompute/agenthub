-- Knowledge: chunks an agent may retrieve as context (mock RAG). Real embedding and
-- vector search are out of scope for the first release; this table stores the text
-- and its provenance so a future retrieval can rank it (F14).
CREATE TABLE IF NOT EXISTS knowledge (
    id          TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL DEFAULT '',
    chunk_text  TEXT NOT NULL DEFAULT '',
    source_ref  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_knowledge_agent_id ON knowledge (agent_id);
