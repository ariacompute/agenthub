package store

import (
	"context"
	"testing"
	"time"

	"agenthub/internal/audit"
	"agenthub/internal/identity"
)

// The audit ledger is append only by trigger: a row cannot be updated or
// deleted, so a test cannot tidy up the ordinary way. These cases therefore
// write rows with artificial timestamps (2001 / 2003) and remove them through
// Prune, which is the only delete the policies accept and the only path an
// operator has. Nothing outside those windows is touched.

var (
	// Long past, so a prune cutoff in 2002 (or 2004) can never reach real rows.
	auditOldTime = time.Date(2001, 1, 1, 0, 0, 3, 0, time.UTC)
	// After the cutoff: proves a prune leaves newer rows alone.
	auditNewTime = time.Date(2003, 1, 1, 0, 0, 0, 0, time.UTC)
)

func auditWriterCtx() context.Context { return identity.System() }

func auditReaderCtx() context.Context {
	return identity.SystemWith(identity.Identity{AuditRead: true})
}

func auditRetentionCtx() context.Context {
	return identity.SystemWith(identity.Identity{Retention: true})
}

// auditTestEvent builds one event with a unique id and the fields the filters
// and counters read.
func auditTestEvent(t *testing.T, id, actor, action string, at time.Time, status int, readOnly bool) audit.Event {
	t.Helper()
	return audit.Event{
		EventID:   id,
		Time:      at,
		RequestID: "req-" + id,
		Source:    "api",
		ActorKind: "user",
		ActorID:   actor,
		IP:        "127.0.0.1",
		Method:    "POST",
		Path:      "/api/" + action,
		Action:    action,
		Status:    status,
		ReadOnly:  readOnly,
		Metadata:  map[string]any{"note": "kept"},
	}
}

// auditCleanup removes every row this file wrote, through the one legal delete.
func auditCleanup(t *testing.T, s audit.Store) {
	t.Helper()
	cutoff := time.Date(2004, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Prune(auditRetentionCtx(), cutoff); err != nil {
		t.Errorf("cleanup prune: %v", err)
	}
}

func TestAuditStoreRoundTripsOneEvent(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	id := uniqueName("audit-one")
	ev := auditTestEvent(t, id, "alice", "resources.create", auditOldTime, 201, false)
	ev.PrevHash = "prev"
	ev.Hash = "hash"

	if err := s.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Get(auditReaderCtx(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nothing for the event that was just written")
	}
	if got.ActorID != "alice" || got.Action != "resources.create" || got.Status != 201 {
		t.Fatalf("read back = %+v", got)
	}
	// The chain material survives a round trip: a verifier recomputes it from the
	// row, so losing either field would make every later link unverifiable.
	if got.PrevHash != "prev" || got.Hash != "hash" {
		t.Fatalf("chain fields = %q / %q", got.PrevHash, got.Hash)
	}
	if got.Metadata["note"] != "kept" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
	if got.Seq == 0 {
		t.Fatal("Seq is the storage id and must be set")
	}

	// An unknown id is an absence, not an error: the handler maps it to 404.
	if missing, err := s.Get(auditReaderCtx(), id+"-nope"); err != nil || missing != nil {
		t.Fatalf("Get(missing) = %v, %v", missing, err)
	}

	// LastHash is what lets a restart continue the chain instead of starting a
	// second one, so it must be the row that was just written.
	hash, tailID, err := s.LastHash(auditReaderCtx())
	if err != nil {
		t.Fatalf("LastHash: %v", err)
	}
	if hash != "hash" || tailID != got.Seq {
		t.Fatalf("LastHash = %q,%d want %q,%d", hash, tailID, "hash", got.Seq)
	}
}

func TestAuditStoreIsIdempotentOnEventID(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-idem")
	ev := auditTestEvent(t, uniqueName("audit-idem-event"), actor, "tasks.get", auditOldTime, 200, true)

	// An empty batch is a no-op rather than an error: the recorder flushes on a
	// timer and a quiet period must not be a failure.
	if err := s.Append(auditWriterCtx(), nil); err != nil {
		t.Fatalf("Append(nil) = %v", err)
	}
	if err := s.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A retried batch re-sends the same events; ON CONFLICT DO NOTHING is what
	// keeps a duplicate delivery from breaking the chain.
	if err := s.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
		t.Fatalf("Append(duplicate) = %v", err)
	}

	rows, err := s.List(auditReaderCtx(), audit.Query{ActorID: actor})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d after writing the same event twice, want 1", len(rows))
	}
}

// Metadata is decorative. An unserialisable value must cost the metadata, never
// the event: a lost audit row is a hole in the trail.
func TestAuditStoreKeepsTheEventWhenMetadataCannotBeEncoded(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-meta")
	ev := auditTestEvent(t, uniqueName("audit-meta-event"), actor, "tasks.get", auditOldTime, 200, true)
	ev.Metadata = map[string]any{"bad": make(chan int)}

	if err := s.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Get(auditReaderCtx(), ev.EventID)
	if err != nil || got == nil {
		t.Fatalf("the event was lost with its metadata: %v, %v", got, err)
	}
	if len(got.Metadata) != 0 {
		t.Fatalf("metadata = %v, want it dropped", got.Metadata)
	}
}

// List and Stats share their WHERE clauses, and the console shows both side by
// side: a counter that described different rows than the table would be a lie.
func TestAuditStoreListFiltersAndStatsAgree(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-filter")
	events := []audit.Event{
		auditTestEvent(t, uniqueName("audit-f-ok"), actor, "resources.list", auditOldTime, 200, true),
		auditTestEvent(t, uniqueName("audit-f-denied"), actor, "resources.delete", auditOldTime.Add(time.Second), 403, false),
		auditTestEvent(t, uniqueName("audit-f-failed"), actor, "resources.create", auditOldTime.Add(2*time.Second), 500, false),
	}
	if err := s.Append(auditWriterCtx(), events); err != nil {
		t.Fatalf("Append: %v", err)
	}

	count := func(q audit.Query) int {
		t.Helper()
		q.ActorID = actor
		rows, err := s.List(auditReaderCtx(), q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		return len(rows)
	}

	if got := count(audit.Query{}); got != 3 {
		t.Fatalf("unfiltered = %d, want 3", got)
	}
	// Newest first: the console reads top down and the cursor is "before this id".
	rows, err := s.List(auditReaderCtx(), audit.Query{ActorID: actor})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Seq <= rows[i].Seq {
			t.Fatalf("rows are not newest-first: %d then %d", rows[i-1].Seq, rows[i].Seq)
		}
	}

	for _, c := range []struct {
		name string
		q    audit.Query
		want int
	}{
		{"outcome ok", audit.Query{Outcome: "ok"}, 1},
		{"outcome denied", audit.Query{Outcome: "denied"}, 1},
		{"outcome failed", audit.Query{Outcome: "failed"}, 1},
		{"action", audit.Query{Action: "resources.create"}, 1},
		{"source", audit.Query{Source: "api"}, 3},
		{"unknown source", audit.Query{Source: "a2a"}, 0},
		{"search by path", audit.Query{Search: "resources."}, 3},
		{"search by request id", audit.Query{Search: "req-" + events[1].EventID}, 1},
		{"before the newest", audit.Query{Before: rows[0].Seq}, 2},
		{"limit is clamped, not rejected", audit.Query{Limit: -1}, 3},
		{"from window excludes everything earlier", audit.Query{From: auditOldTime.Add(time.Hour)}, 0},
	} {
		if got := count(c.q); got != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, got, c.want)
		}
	}

	stats, err := s.Stats(auditReaderCtx(), audit.Query{ActorID: actor})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 3 || stats.Denied != 1 || stats.Failed != 1 || stats.Written != 2 {
		t.Fatalf("stats = %+v, want total 3 / denied 1 / failed 1 / written 2", stats)
	}
}

// The verifier walks the trail with Segment, so the paging has to be exact:
// ascending by id, a from window, and a cursor that cannot repeat or skip a row.
func TestAuditStoreSegmentPagesInOrder(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-segment")
	events := make([]audit.Event, 0, 3)
	prev := ""
	base := time.Date(2001, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ev := auditTestEvent(t, uniqueName("audit-seg"), actor, "tasks.get", base.Add(time.Duration(i)*time.Second), 200, true)
		ev.PrevHash = prev
		ev.Hash = audit.Hash(prev, ev)
		prev = ev.Hash
		events = append(events, ev)
	}
	// The ledger already holds rows this file did not write, so the cursor starts
	// at the newest id before the append: everything past it belongs to this case.
	_, before, err := s.LastHash(auditReaderCtx())
	if err != nil {
		t.Fatalf("LastHash: %v", err)
	}
	if err := s.Append(auditWriterCtx(), events); err != nil {
		t.Fatalf("Append: %v", err)
	}

	page, err := s.Segment(auditReaderCtx(), time.Time{}, before, 2)
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("first page = %d rows, want 2", len(page))
	}
	if page[0].EventID != events[0].EventID || page[1].EventID != events[1].EventID {
		t.Fatalf("first page = %q,%q", page[0].EventID, page[1].EventID)
	}
	// The chain the verifier recomputes has to come back intact.
	if page[1].PrevHash != page[0].Hash {
		t.Fatalf("the page broke the chain: prev=%q hash=%q", page[1].PrevHash, page[0].Hash)
	}

	rest, err := s.Segment(auditReaderCtx(), time.Time{}, page[1].Seq, 2)
	if err != nil {
		t.Fatalf("Segment after cursor: %v", err)
	}
	if len(rest) != 1 || rest[0].EventID != events[2].EventID {
		t.Fatalf("second page = %+v, want the third event only", rest)
	}

	// The from window is inclusive, which is how a verifier resumes from a
	// timestamp after a restart.
	window, err := s.Segment(auditReaderCtx(), base.Add(time.Second), before, 0)
	if err != nil {
		t.Fatalf("Segment with a from window: %v", err)
	}
	if len(window) != 2 || window[0].EventID != events[1].EventID {
		t.Fatalf("windowed page = %+v, want the last two events", window)
	}
}

func TestAuditStorePruneStopsAtTheCutoff(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	old := auditTestEvent(t, uniqueName("audit-prune-old"), uniqueName("audit-prune"), "tasks.get", auditOldTime, 200, true)
	newer := auditTestEvent(t, uniqueName("audit-prune-new"), old.ActorID, "tasks.get", auditNewTime, 200, true)
	if err := s.Append(auditWriterCtx(), []audit.Event{old, newer}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	removed, err := s.Prune(auditRetentionCtx(), time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed < 1 {
		t.Fatalf("Prune removed %d rows, want at least the expired one", removed)
	}
	if gone, err := s.Get(auditReaderCtx(), old.EventID); err != nil || gone != nil {
		t.Fatalf("the expired row survived: %v, %v", gone, err)
	}
	if kept, err := s.Get(auditReaderCtx(), newer.EventID); err != nil || kept == nil {
		t.Fatalf("the prune took a row newer than its cutoff: %v, %v", kept, err)
	}
}
