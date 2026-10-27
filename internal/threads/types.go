// Package threads defines the domain types and persistence port for agentbuddy's
// multi-agent collaboration threads.
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store — the same dependency-inversion pattern as registry.ResourceStore,
// so internal/store is the only package that touches GORM and the threads package
// stays free of it.
package threads

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a thread or message does not exist (or is not
// owned by the caller, so it must not be distinguishable).
var ErrNotFound = errors.New("threads: not found")

// Thread is one collaboration space owned by a principal: a set of participating
// agents (resource ids) and the message history of the conversation with them.
type Thread struct {
	ID           string
	Title        string
	OwnerID      string
	Participants []string
	CreatedAt    string
	UpdatedAt    string
}

// Message is one turn in a thread. AuthorType is "user" or "agent"; for an agent
// message AuthorID and ResourceID are the agent's resource id, and TaskID links
// back to the A2A task that produced the reply.
type Message struct {
	ID         string
	ThreadID   string
	AuthorType string
	AuthorID   string
	AuthorName string
	Body       string
	TaskID     string
	ResourceID string
	// Origin marks where the message entered the conversation: "web" (default),
	// "voice" or a channel kind. It is descriptive only and never changes
	// routing.
	Origin    string
	CreatedAt string
}

// Store persists threads and their messages.
type Store interface {
	CreateThread(ctx context.Context, t *Thread) (*Thread, error)
	GetThread(ctx context.Context, id, ownerID string) (*Thread, error)
	ListThreads(ctx context.Context, ownerID string) ([]*Thread, error)
	SetParticipants(ctx context.Context, threadID string, resourceIDs []string) error
	AddMessage(ctx context.Context, m *Message) (*Message, error)
	ListMessages(ctx context.Context, threadID string, sinceSeq int64) ([]*Message, error)
	// UpsertAgentMessage creates (or, when body is non-empty, updates the body of)
	// the agent message that represents a delegated task's reply, keyed by task id.
	UpsertAgentMessage(ctx context.Context, threadID, taskID, resourceID, body string) (*Message, error)
}
