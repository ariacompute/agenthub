package hub

import (
	"context"

	"agenthub/internal/registry"
)

// distributionKey carries the ability package of the agent a call is about to
// reach.
//
// It travels on the context rather than on MessageSendParams because params is
// decoded from the caller's JSON: anything a client can put there can be
// forged, and a forged package would let one caller feed arbitrary mcp servers
// to somebody else's agent. The handler resolves the package after authorizing
// the call and attaches it here, which is the same channel identity and the
// audit trace already use.
type distributionKey struct{}

// WithDistribution attaches the package the target agent should receive.
// A nil or empty package leaves the context untouched: the connector then sends
// exactly what it sent before this feature existed.
func WithDistribution(ctx context.Context, dist *registry.Distribution) context.Context {
	if dist == nil || dist.Empty() {
		return ctx
	}
	return context.WithValue(ctx, distributionKey{}, dist)
}

// distributionFrom reads the package attached by the HTTP boundary.
func distributionFrom(ctx context.Context) *registry.Distribution {
	dist, _ := ctx.Value(distributionKey{}).(*registry.Distribution)
	return dist
}
