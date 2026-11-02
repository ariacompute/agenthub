// Package knowledge defines the domain types and persistence port for the chunks an
// agent may retrieve as context (mock RAG).
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store.
package knowledge

import "context"

// Knowledge is one retrieved-or-stored chunk an agent may use as context.
type Knowledge struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	// OwnerID is the account the chunk belongs to. Empty means the instance
	// wrote it; such a chunk is visible only to the platform owner.
	OwnerID   string `json:"ownerId,omitempty"`
	ChunkText string `json:"chunkText"`
	SourceRef string `json:"sourceRef"`
	CreatedAt string `json:"createdAt"`
}

// Query narrows a listing.
//
// OwnerID is the caller's account. An empty owner means "no restriction", which
// is what the platform owner and an administrator get; every other caller must
// pass its own id, because a chunk is only ever visible to the account that
// stored it. Filtering by AgentID alone was the original leak: it let any
// resources:read holder read every account's retrieval corpus.
type Query struct {
	AgentID string
	OwnerID string
}

// Store persists knowledge chunks. Implemented in internal/store.
type Store interface {
	List(ctx context.Context, q Query) ([]*Knowledge, error)
	Add(ctx context.Context, k *Knowledge) (*Knowledge, error)
}
