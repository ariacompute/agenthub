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

func TestDeliverRejectsAURLWithoutAHost(t *testing.T) {
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": "https://"}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("a target without a host must be rejected before any dial")
	}
}

func TestDeliverNeverReachesAPrivateAddress(t *testing.T) {
	// The dial-time guard, not the URL check, is what stops an operator from
	// pointing a channel at the host's own loopback: the send fails and the
	// caller records the failed receipt.
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": "https://127.0.0.1:9/hook"}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("a loopback target must be refused at dial time")
	}
}

func TestDeliverReportsADeadHostAsAnError(t *testing.T) {
	// A public name that cannot resolve is still an error, never an empty result.
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": "https://no-such-host.invalid/hook"}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("an unreachable target must be reported")
	}
}

func TestDeliverRefusesAnAlreadyCancelledRequest(t *testing.T) {
	// Cancellation is threaded through the request context: the send gives up
	// instead of waiting out the client timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": "https://no-such-host.invalid/hook"}}
	if _, err := Deliver(ctx, ch, "body"); err == nil {
		t.Fatal("a cancelled context must fail the send")
	}
}

func TestDeliverTreatsANonStringTargetAsMissing(t *testing.T) {
	// config is untyped jsonb: a number where a URL belongs must read as absent,
	// not as a panic.
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": 42}}
	if _, err := Deliver(context.Background(), ch, "body"); err == nil {
		t.Fatal("a non-string target_url must be rejected")
	}
}
