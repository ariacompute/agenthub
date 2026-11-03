// Package channels defines the domain types and persistence port for the external
// messaging surfaces (Slack / Teams / Telegram / SMS) the console binds to a thread.
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store — the same dependency-inversion pattern as registry.ResourceStore.
package channels

import (
	"context"

	"agenthub/internal/httputil"
)

// ErrChannelNotFound reports that the parent channel could not be read in the
// caller's scope. A receipt cannot be written without it: the owner it has to
// carry comes from that row.
var ErrChannelNotFound = &httputil.Error{Code: httputil.CodeChannelNotFound}

// Kind enumerates the supported external surfaces.
type Kind string

const (
	KindSlack    Kind = "slack"
	KindTeams    Kind = "teams"
	KindTelegram Kind = "telegram"
	KindSMS      Kind = "sms"
)

// Kinds lists every supported channel kind.
var Kinds = []Kind{KindSlack, KindTeams, KindTelegram, KindSMS}

// NormalizeKind returns a valid kind, falling back to the empty string (unknown)
// for anything unrecognised so a malformed inbound request is rejected rather
// than silently coerced.
func NormalizeKind(s string) Kind {
	for _, k := range Kinds {
		if string(k) == s {
			return k
		}
	}
	return Kind("")
}

// Channel is one external messaging surface bound to a thread. The signing secret
// is never stored: only the name of the environment variable that holds it at call
// time (SigningKeyEnv).
type Channel struct {
	ID            string         `json:"id"`
	Kind          Kind           `json:"kind"`
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	Config        map[string]any `json:"config"`
	SigningKeyEnv string         `json:"signingKeyEnv"`
	ThreadID      string         `json:"threadId"`
	OwnerID       string         `json:"ownerId"`
}

// ChannelMessage is one inbound (or outbound) message routed through a channel.
// ExternalRef is the platform's idempotency key: a unique constraint dedupes
// redeliveries.
type ChannelMessage struct {
	ID          string         `json:"id"`
	ChannelID   string         `json:"channelId"`
	ExternalRef string         `json:"externalRef"`
	ThreadID    string         `json:"threadId"`
	TaskID      string         `json:"taskId"`
	Direction   string         `json:"direction"`
	Payload     map[string]any `json:"payload"`
	Status      string         `json:"status"`
	Error       string         `json:"error"`
	// OwnerID is the owner of the channel this receipt travelled through. The
	// store fills it from the parent row on insert; callers never set it by
	// hand, and every read scopes on it.
	OwnerID   string `json:"ownerId"`
	CreatedAt string `json:"createdAt"`
}

// Status values for a channel message.
const (
	StatusReceived  = "received"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// Store persists channels and their messages. Implemented in internal/store.
//
// Ownership is part of the key, not a filter: Get, Update and Delete take the
// caller's owner id and report somebody else's row as absent. The list endpoint
// was always scoped while the by-id endpoints were not, which let any signed in
// user read, rewrite, re-point or delete another account's channel; a lookup
// that does not carry the owner cannot answer "does this id exist" either
// (requirements-isolation.md: an unauthorised read is "not found").
type Store interface {
	List(ctx context.Context, ownerID string) ([]*Channel, error)
	// Get returns the channel when ownerID owns it, and nil otherwise.
	Get(ctx context.Context, id, ownerID string) (*Channel, error)
	// GetUnscoped reads a channel with no ownership predicate. It exists for
	// one caller only — the public inbound webhook, which has to read the row to
	// verify the platform signature before any session can exist — and is always
	// called with a system identity, never with a request identity.
	GetUnscoped(ctx context.Context, id string) (*Channel, error)
	Create(ctx context.Context, c *Channel) (*Channel, error)
	Update(ctx context.Context, c *Channel, ownerID string) (*Channel, error)
	Delete(ctx context.Context, id, ownerID string) error
	// RecordDelivery inserts a channel message. It returns (false, nil) when
	// ExternalRef is a duplicate, letting the caller treat redelivery as a no-op.
	// The owner is read from the parent channel inside the same transaction, so a
	// receipt cannot be written under an owner the channel does not carry.
	RecordDelivery(ctx context.Context, m *ChannelMessage) (bool, error)
	// ListDeliveries reads one channel's receipts, scoped to the owner that
	// channel carries. Ownership is part of the key here for the same reason it
	// is on Get: a lookup that does not carry the owner cannot answer "does this
	// receipt exist" either.
	ListDeliveries(ctx context.Context, channelID, ownerID string) ([]*ChannelMessage, error)
	// GetDelivery returns the receipt identified by ID (its ExternalRef) on a
	// channel the given owner holds, or nil when there is none.
	GetDelivery(ctx context.Context, channelID, id, ownerID string) (*ChannelMessage, error)
	// UpdateDelivery rewrites the outcome of an existing receipt in place, so a
	// retry keeps one line per message instead of appending a second one. It
	// reports false when the row is gone — including when it belongs to somebody
	// else.
	UpdateDelivery(ctx context.Context, m *ChannelMessage, ownerID string) (bool, error)
}
