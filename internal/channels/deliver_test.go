package channels

import (
	"context"
	"testing"
)

func TestDeliverRequiresTargetURL(t *testing.T) {
	ch := &Channel{ID: "c1", Config: map[string]any{}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("a channel without target_url must be rejected")
	}
}

func TestDeliverRejectsUnsupportedSchemes(t *testing.T) {
	// A non-http(s) scheme is refused outright.
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": "ftp://example.test/hook"}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("unsupported scheme must be rejected")
	}
	// Provider webhooks carry secrets, so plain http is never allowed.
	ch = &Channel{ID: "c2", Config: map[string]any{"target_url": "http://example.test/hook"}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("http target must be rejected in favour of https")
	}
}

func TestDeliverTruncationLimitIsBounded(t *testing.T) {
	// The constant is a guardrail: the client must never buffer more than the cap.
	if maxOutboundBody <= 0 || maxOutboundBody > 1024 {
		t.Fatalf("maxOutboundBody = %d, expected a small positive cap", maxOutboundBody)
	}
}
