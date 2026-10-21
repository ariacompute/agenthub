package hub

import (
	"testing"

	"agenthub/internal/a2a"
)

func TestNotifyRefusesInsecureURL(t *testing.T) {
	n := NewHTTPNotifier(nil, "")
	// An http (not https) destination is refused before any network call and the
	// error is swallowed so it never affects the task's terminal state.
	if err := n.Notify(nil, a2a.PushNotificationConfig{URL: "http://evil.test/cb", Token: "t"}, "task1", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("insecure url should be refused without error: %v", err)
	}
}

func TestNotifierSignIsDeterministicAndVerifiable(t *testing.T) {
	secret := "strong-shared-secret"
	ts := int64(1700000000)
	nonce := "abc123"
	body := []byte(`{"taskId":"t1","state":"completed"}`)
	sig1 := sign(secret, ts, nonce, body)
	sig2 := sign(secret, ts, nonce, body)
	if sig1 != sig2 {
		t.Fatal("signature must be deterministic for identical inputs")
	}
	if sign(secret, ts+1, nonce, body) == sig1 {
		t.Fatal("different timestamp must change signature")
	}
	if sign("other", ts, nonce, body) == sig1 {
		t.Fatal("different secret must change signature")
	}
}

func TestNotifierMarkSeenDeduplicates(t *testing.T) {
	n := NewHTTPNotifier(nil, "")
	if !n.markSeen("task1/completed") {
		t.Fatal("first sight must return true")
	}
	if n.markSeen("task1/completed") {
		t.Fatal("repeat within ttl must return false")
	}
	if !n.markSeen("task2/completed") {
		t.Fatal("different key must return true")
	}
}
