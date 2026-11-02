package audit

import (
	"testing"
	"time"
)

func sampleEvent(id string) Event {
	return Event{
		EventID:   id,
		Time:      time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
		RequestID: "req-" + id,
		Source:    SourceAPI,
		ActorKind: ActorUser,
		ActorID:   "u-1",
		Method:    "POST",
		Path:      "/api/tasks",
		Action:    "POST /api/tasks",
		Status:    200,
		ReadOnly:  false,
		LatencyMS: 12,
		UserAgent: "test",
		Metadata:  map[string]any{"count": 1, "note": "x"},
	}
}

// TestHashIsStableAcrossCalls pins the property the verifier depends on: the
// same content always hashes the same way, regardless of how the map was built.
func TestHashIsStableAcrossCalls(t *testing.T) {
	e := sampleEvent("e1")
	first := Hash("", e)
	second := Hash("", e)
	if first != second {
		t.Fatalf("hash is not stable: %q vs %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("hash length = %d, want a hex sha256", len(first))
	}
	// The previous link is part of the input, so a different prefix must give a
	// different digest; otherwise reordering rows would go unnoticed.
	if Hash("other", e) == first {
		t.Fatal("prev_hash must change the digest")
	}
}

// TestHashCoversEveryField walks each field and asserts a change breaks the
// digest. A field left out of Canonical is a field an attacker can edit without
// breaking the chain.
func TestHashCoversEveryField(t *testing.T) {
	base := sampleEvent("e1")
	seed := Hash("", base)
	mutations := map[string]func(*Event){
		"eventId":  func(e *Event) { e.EventID = "e2" },
		"time":     func(e *Event) { e.Time = e.Time.Add(time.Second) },
		"request":  func(e *Event) { e.RequestID = "other" },
		"source":   func(e *Event) { e.Source = SourceA2A },
		"actor":    func(e *Event) { e.ActorID = "u-2" },
		"label":    func(e *Event) { e.ActorLabel = "x" },
		"owner":    func(e *Event) { e.OwnerID = "o" },
		"ip":       func(e *Event) { e.IP = "10.0.0.9" },
		"method":   func(e *Event) { e.Method = "DELETE" },
		"path":     func(e *Event) { e.Path = "/api/other" },
		"action":   func(e *Event) { e.Action = "DELETE /api/tasks" },
		"resource": func(e *Event) { e.ResourceID = "res-1" },
		"kind":     func(e *Event) { e.ResourceKind = "agent" },
		"status":   func(e *Event) { e.Status = 500 },
		"code":     func(e *Event) { e.ErrorCode = "forbidden" },
		"readOnly": func(e *Event) { e.ReadOnly = true },
		"latency":  func(e *Event) { e.LatencyMS = 99 },
		"ua":       func(e *Event) { e.UserAgent = "other" },
		"metadata": func(e *Event) { e.Metadata = map[string]any{"count": 2} },
	}
	for name, mutate := range mutations {
		ev := sampleEvent("e1")
		mutate(&ev)
		if got := Hash("", ev); got == seed {
			t.Fatalf("%s is not covered by the chain digest", name)
		}
	}
}

// TestVerifySegmentAcceptsAnIntactChain is the happy path.
func TestVerifySegmentAcceptsAnIntactChain(t *testing.T) {
	rows := chainOf(t, 3)
	prev, broken := VerifySegment("", rows)
	if broken != nil {
		t.Fatalf("intact chain reported broken: %+v", broken)
	}
	if prev != rows[len(rows)-1].Hash {
		t.Fatal("the returned seed must be the last link")
	}
}

// TestVerifySegmentDetectsAnEdit is the tamper case: one field changed in the
// middle breaks that row and every later link.
func TestVerifySegmentDetectsAnEdit(t *testing.T) {
	rows := chainOf(t, 3)
	rows[1].Status = 500
	if _, broken := VerifySegment("", rows); broken == nil {
		t.Fatal("an edited row must break the chain")
	}
}

// TestVerifySegmentDetectsARemovedRow is the deletion case: the successor's
// prev_hash no longer matches, even though each remaining row is internally
// consistent.
func TestVerifySegmentDetectsARemovedRow(t *testing.T) {
	rows := chainOf(t, 4)
	rows = append(rows[:1], rows[2:]...)
	if _, broken := VerifySegment("", rows); broken == nil {
		t.Fatal("a removed row must break the chain")
	}
}

func chainOf(t *testing.T, n int) []Event {
	t.Helper()
	out := make([]Event, 0, n)
	prev := ""
	for i := 0; i < n; i++ {
		ev := sampleEvent(string(rune('a' + i)))
		ev.PrevHash = prev
		ev.Hash = Hash(prev, ev)
		prev = ev.Hash
		out = append(out, ev)
	}
	return out
}
