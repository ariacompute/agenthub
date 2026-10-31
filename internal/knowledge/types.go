// Package knowledge defines the domain types and persistence port for the chunks an
// agent may retrieve as context (mock RAG).
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store.
package knowledge

import "context"

// Knowledge is one retrieved-or-stored chunk an agent may use as context.
type Knowledge struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	ChunkText string `json:"chunkText"`
	SourceRef string `json:"sourceRef"`
	CreatedAt string `json:"createdAt"`
}

// Store persists knowledge chunks. Implemented in internal/store.
type Store interface {
	List(ctx context.Context, agentID string) ([]*Knowledge, error)
	Add(ctx context.Context, k *Knowledge) (*Knowledge, error)
}
