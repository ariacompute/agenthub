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

// Computer is one agent sandbox the console may attach to. In the first release the
// console renders a mock view; session_ref is where a real driver would attach.
type Computer struct {
	ID         string `json:"id"`
	OwnerID    string `json:"ownerId"`
	AgentID    string `json:"agentId"`
	Kind       Kind   `json:"kind"`
	SessionRef string `json:"sessionRef"`
	Status     string `json:"status"`
	LastAction string `json:"lastAction"`
	CreatedAt  string `json:"createdAt"`
}

// Store persists computers. Implemented in internal/store.
type Store interface {
	List(ctx context.Context, ownerID string) ([]*Computer, error)
	Get(ctx context.Context, id string) (*Computer, error)
	Create(ctx context.Context, c *Computer) (*Computer, error)
}
