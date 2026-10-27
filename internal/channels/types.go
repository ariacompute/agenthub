// Package channels defines the domain types and persistence port for the external
// messaging surfaces (Slack / Teams / Telegram / SMS) the console binds to a thread.
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store — the same dependency-inversion pattern as registry.ResourceStore.
package channels

import "context"

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
	CreatedAt   string         `json:"createdAt"`
}

// Status values for a channel message.
const (
	StatusReceived  = "received"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// Store persists channels and their messages. Implemented in internal/store.
type Store interface {
	List(ctx context.Context, ownerID string) ([]*Channel, error)
	Get(ctx context.Context, id string) (*Channel, error)
	Create(ctx context.Context, c *Channel) (*Channel, error)
	Update(ctx context.Context, c *Channel) (*Channel, error)
	Delete(ctx context.Context, id string) error
	// RecordDelivery inserts a channel message. It returns (false, nil) when
	// ExternalRef is a duplicate, letting the caller treat redelivery as a no-op.
	RecordDelivery(ctx context.Context, m *ChannelMessage) (bool, error)
	ListDeliveries(ctx context.Context, channelID string) ([]*ChannelMessage, error)
}
