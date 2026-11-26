package hub

import (
	"context"
	"testing"

	"agenthub/internal/registry"
)

// The package travels on the context because message metadata is decoded from
// the caller's JSON. These cases pin the two halves of that contract: what gets
// attached, and what deliberately does not.
func TestDistributionTravelsOnTheContext(t *testing.T) {
	ctx := context.Background()
	if got := distributionFrom(ctx); got != nil {
		t.Fatalf("a bare context carries no package, got %+v", got)
	}
	if got := distributionFrom(WithDistribution(ctx, nil)); got != nil {
		t.Fatalf("nil must be a no-op, got %+v", got)
	}
	// An agent with nothing bound sends the same message it sent before the
	// feature existed, so an empty package must not become a value at all.
	if got := distributionFrom(WithDistribution(ctx, &registry.Distribution{})); got != nil {
		t.Fatalf("an empty package must be a no-op, got %+v", got)
	}

	dist := &registry.Distribution{
		AgentID: "agent-1",
		MCP:     []registry.MCPBinding{{ID: "mcp-1"}},
	}
	if got := distributionFrom(WithDistribution(ctx, dist)); got != dist {
		t.Fatalf("package = %+v", got)
	}

	// A package with nothing delivered but a stale reference is still worth
	// sending: the agent has to learn that its ability disappeared.
	stale := &registry.Distribution{AgentID: "agent-1", Unresolved: []string{"gone"}}
	if got := distributionFrom(WithDistribution(ctx, stale)); got != stale {
		t.Fatalf("stale package = %+v", got)
	}
}
