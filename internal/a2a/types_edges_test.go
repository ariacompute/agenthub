package a2a

import "testing"

// Context() is the compatibility shim between the v1.0 contextId and the v0.1
// sessionId: a peer that sends the old field must still be correlatable.
func TestMessageContextPrefersContextID(t *testing.T) {
	if got := (Message{ContextID: "ctx-1", SessionID: "sess-1"}).Context(); got != "ctx-1" {
		t.Fatalf("Context() = %q, want the v1.0 context id", got)
	}
	if got := (Message{SessionID: "sess-1"}).Context(); got != "sess-1" {
		t.Fatalf("Context() = %q, want the v0.1 session id", got)
	}
	if got := (Message{}).Context(); got != "" {
		t.Fatalf("Context() = %q, want empty", got)
	}
}
