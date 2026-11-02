// Package computers defines the domain types and persistence port for the agent
// sandboxes (browser / shell / screen) the console may attach to.
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store — the same dependency-inversion pattern as channels.Store.
package computers

import "context"

// Kind enumerates the sandbox an agent may drive.
type Kind string

const (
	KindBrowser Kind = "browser"
	KindShell   Kind = "shell"
	KindScreen  Kind = "screen"
)

// Kinds lists every supported computer kind.
var Kinds = []Kind{KindBrowser, KindShell, KindScreen}

// NormalizeKind returns a valid kind, falling back to the empty string (unknown)
// for anything unrecognised so a malformed request is rejected rather than coerced.
func NormalizeKind(s string) Kind {
	for _, k := range Kinds {
		if string(k) == s {
			return k
		}
	}
	return Kind("")
}

// Computer is one agent sandbox the console may attach to.
//
// It used to carry a session_ref nobody could attach to. It now names the lease
// that actually backs it, which is the difference between a card in a console
// and an execution environment: the lease is what expires, what can be
// terminated, and what the audit trail points at.
type Computer struct {
	ID         string `json:"id"`
	OwnerID    string `json:"ownerId"`
	AgentID    string `json:"agentId"`
	Kind       Kind   `json:"kind"`
	SessionRef string `json:"sessionRef"`
	Provider   string `json:"provider,omitempty"`
	LeaseID    string `json:"leaseId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	Status     string `json:"status"`
	LastAction string `json:"lastAction"`
	CreatedAt  string `json:"createdAt"`
}

// Store persists computers. Implemented in internal/store.
//
// Ownership is part of the key (see channels.Store). The delete path already
// compared owner ids in the handler while the get path did not, which is the
// asymmetry that exposed every card to every signed in user.
type Store interface {
	List(ctx context.Context, ownerID string) ([]*Computer, error)
	Get(ctx context.Context, id, ownerID string) (*Computer, error)
	Create(ctx context.Context, c *Computer) (*Computer, error)
	// Update writes back the fields a lease changes (status, last action, expiry).
	// ownerID scopes the row it is allowed to touch.
	Update(ctx context.Context, c *Computer, ownerID string) (*Computer, error)
	// Delete removes the row; the caller terminates the lease first.
	Delete(ctx context.Context, id, ownerID string) error
}
