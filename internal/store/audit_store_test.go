package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/audit"
	"agenthub/internal/identity"
	"agenthub/internal/model"
	"agenthub/internal/testdb"
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

// requireAuditLedger opens the shared handle and then asks the one question
// these cases depend on: may this role expire rows? The runtime role may append
// and read audit_log and nothing else — provision-db-roles.sql revokes UPDATE,
// DELETE and TRUNCATE from it — so on a DML-only deployment every case below
// skips with the grant named, rather than reporting "permission denied" from a
// cleanup that was never the subject of the test.
func requireAuditLedger(t *testing.T) *gorm.DB {
	t.Helper()
	db := openGovernanceDB(t)
	testdb.RequireTablePrivilege(t, db, "audit_log", "DELETE")
	// Every case below prunes, and Prune always moves the global anchor.
	testdb.RequireTablePrivilege(t, db, "audit_chain_anchor", "UPDATE")
	preserveChainAnchor(t, db)
	return db
}

func auditRetentionCtx() context.Context {
	return identity.SystemWith(identity.Identity{Retention: true})
}

// preservedChainAnchor is the single-row anchor table as the case found it.
type preservedChainAnchor struct {
	found    bool
	hash     string
	eventID  string
	prunedAt time.Time
}

// preserveChainAnchor captures the anchor before a case can move it and puts it
// back afterwards.
//
// The table is global and Prune rewrites it on every call, including the cleanup
// prune. Leaving a case's own seed behind is not cosmetic: /api/audit/verify
// seeds the walk from that hash, so the next caller - the console, or an e2e run
// sharing this database - is told a healthy chain is broken at its first row,
// because no row is preceded by a hash the pruned test row invented.
func preserveChainAnchor(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []model.AuditChainAnchor
	if err := db.Where("id = ?", 1).Limit(1).Find(&rows).Error; err != nil {
		t.Fatalf("read the chain anchor: %v", err)
	}
	before := preservedChainAnchor{}
	if len(rows) > 0 {
		before = preservedChainAnchor{found: true, hash: rows[0].Hash, eventID: rows[0].EventID, prunedAt: rows[0].PrunedAt}
	}
	// Registered before the case's own cleanup, so it runs after it: cleanups are
	// LIFO, and the prune has to happen before the anchor is put back.
	t.Cleanup(func() {
		// An empty hash is how Anchor() reports "never pruned", so an anchor that
		// did not exist comes back as an empty one: the table keeps its single-row
		// shape without needing DELETE on it.
		anchor := model.AuditChainAnchor{ID: 1, Hash: before.hash, EventID: before.eventID, PrunedAt: before.prunedAt}
		if anchor.PrunedAt.IsZero() {
			anchor.PrunedAt = time.Now().UTC()
		}
		if err := withIdentity(auditRetentionCtx(), db, func(tx *gorm.DB) error {
			return tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"hash", "event_id", "pruned_at"}),
			}).Create(&anchor).Error
		}); err != nil {
			t.Errorf("restore the chain anchor after the test: %v", err)
		}
	})
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
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	id := uniqueName("audit-one")
	ev := auditTestEvent(t, id, "alice", "resources.create", auditOldTime, 201, false)

	// The store owns the chain: the batch arrives unstamped and it is stamped
	// under the chain lock, so the tail it returns is the last row's hash.
	events := []audit.Event{ev}
	tail, err := s.Append(auditWriterCtx(), events)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if tail != events[0].Hash {
		t.Fatalf("Append tail = %q, want the hash it stamped %q", tail, events[0].Hash)
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
	if got.Hash != audit.Hash(got.PrevHash, *got) {
		t.Fatalf("chain fields do not recompute: prev=%q hash=%q", got.PrevHash, got.Hash)
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
	if hash != got.Hash || tailID != got.Seq {
		t.Fatalf("LastHash = %q,%d want %q,%d", hash, tailID, got.Hash, got.Seq)
	}
}

func TestAuditStoreIsIdempotentOnEventID(t *testing.T) {
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-idem")
	ev := auditTestEvent(t, uniqueName("audit-idem-event"), actor, "tasks.get", auditOldTime, 200, true)

	// An empty batch is a no-op rather than an error: the recorder flushes on a
	// timer and a quiet period must not be a failure.
	if _, err := s.Append(auditWriterCtx(), nil); err != nil {
		t.Fatalf("Append(nil) = %v", err)
	}
	if _, err := s.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A retried batch re-sends the same events. The duplicate is dropped before
	// stamping, so it neither adds a row nor invents a hash no row carries, and
	// the tail stays where it was. The batch repeats the event inside itself too:
	// deduplication cannot depend on the copies arriving in separate calls.
	beforeReplay, _, err := s.LastHash(auditReaderCtx())
	if err != nil {
		t.Fatalf("LastHash: %v", err)
	}
	if _, err := s.Append(auditWriterCtx(), []audit.Event{ev, ev}); err != nil {
		t.Fatalf("Append(duplicate) = %v", err)
	}
	afterReplay, _, err := s.LastHash(auditReaderCtx())
	if err != nil {
		t.Fatalf("LastHash: %v", err)
	}
	if afterReplay != beforeReplay {
		t.Fatalf("a replayed batch moved the tail: %q -> %q", beforeReplay, afterReplay)
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
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-meta")
	ev := auditTestEvent(t, uniqueName("audit-meta-event"), actor, "tasks.get", auditOldTime, 200, true)
	ev.Metadata = map[string]any{"bad": make(chan int)}

	if _, err := s.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
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
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-filter")
	events := []audit.Event{
		auditTestEvent(t, uniqueName("audit-f-ok"), actor, "resources.list", auditOldTime, 200, true),
		auditTestEvent(t, uniqueName("audit-f-denied"), actor, "resources.delete", auditOldTime.Add(time.Second), 403, false),
		auditTestEvent(t, uniqueName("audit-f-failed"), actor, "resources.create", auditOldTime.Add(2*time.Second), 500, false),
	}
	if _, err := s.Append(auditWriterCtx(), events); err != nil {
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
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	actor := uniqueName("audit-segment")
	events := make([]audit.Event, 0, 3)
	base := time.Date(2001, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		events = append(events, auditTestEvent(t, uniqueName("audit-seg"), actor, "tasks.get", base.Add(time.Duration(i)*time.Second), 200, true))
	}
	// The ledger already holds rows this file did not write, so the cursor starts
	// at the newest id before the append: everything past it belongs to this case.
	_, before, err := s.LastHash(auditReaderCtx())
	if err != nil {
		t.Fatalf("LastHash: %v", err)
	}
	if _, err := s.Append(auditWriterCtx(), events); err != nil {
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
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	old := auditTestEvent(t, uniqueName("audit-prune-old"), uniqueName("audit-prune"), "tasks.get", auditOldTime, 200, true)
	newer := auditTestEvent(t, uniqueName("audit-prune-new"), old.ActorID, "tasks.get", auditNewTime, 200, true)
	if _, err := s.Append(auditWriterCtx(), []audit.Event{old, newer}); err != nil {
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

// A prune leaves a head whose prev_hash points at a row that is gone. It must
// record that removed tail in the same transaction, or /api/audit/verify calls
// a healthy trail broken at its first row.
func TestAuditStorePruneRecordsTheChainAnchor(t *testing.T) {
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	tag := uniqueName("audit-anchor")
	// The store stamps the two rows, so the assertion below uses the hash it
	// actually wrote for the expired one - which is exactly what the anchor has
	// to remember.
	events := []audit.Event{
		auditTestEvent(t, tag+"-old", tag, "tasks.get", auditOldTime, 200, true),
		auditTestEvent(t, tag+"-new", tag, "tasks.get", auditNewTime, 200, true),
	}
	if _, err := s.Append(auditWriterCtx(), events); err != nil {
		t.Fatalf("Append: %v", err)
	}
	old := events[0]

	cutoff := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
	removed, err := s.Prune(auditRetentionCtx(), cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed < 1 {
		t.Fatalf("Prune removed %d rows, want at least the expired one", removed)
	}
	anchor, err := s.Anchor(auditReaderCtx())
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if anchor.Hash != old.Hash || anchor.EventID != old.EventID {
		t.Fatalf("anchor = %+v, want the removed tail %q / %q", anchor, old.Hash, old.EventID)
	}

	// A prune that expires nothing must leave the anchor where it is.
	if _, err := s.Prune(auditRetentionCtx(), cutoff); err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	again, err := s.Anchor(auditReaderCtx())
	if err != nil || again.Hash != old.Hash {
		t.Fatalf("anchor after a no-op prune = %+v, %v", again, err)
	}
}

// secondAuditHandle opens an independent pool. A PostgreSQL advisory lock belongs
// to a database session, not to a Go value, so a second pool behaves exactly like
// a second agenthub instance - which is the deployment shape the chain lock is
// for.
func secondAuditHandle(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := OpenWithOptions(testdb.DSN(t), OpenOptions{})
	if err != nil {
		t.Fatalf("open a second handle: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(db); err != nil {
			t.Errorf("close the second handle: %v", err)
		}
	})
	return db
}

// TestAuditAppendWaitsForTheChainLock is the cross-instance guarantee without a
// second process: one transaction on another pool holds the chain lock, and
// Append must not return until that transaction ends. Before the lock existed
// this case would pass instantly and two instances would fork the trail.
func TestAuditAppendWaitsForTheChainLock(t *testing.T) {
	db := requireAuditLedger(t)
	s := NewAuditStore(db)
	t.Cleanup(func() { auditCleanup(t, s) })

	held := make(chan struct{})
	release := make(chan struct{})
	blocker := make(chan error, 1)
	go func() {
		blocker <- secondAuditHandle(t).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, auditChainLockKey).Error; err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ev := auditTestEvent(t, uniqueName("audit-lock"), uniqueName("audit-lock"), "tasks.get", auditOldTime, 200, true)
	appended := make(chan error, 1)
	go func() {
		_, err := s.Append(auditWriterCtx(), []audit.Event{ev})
		appended <- err
	}()

	select {
	case err := <-appended:
		t.Fatalf("Append returned %v while another session held the chain lock", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	if err := <-blocker; err != nil {
		t.Fatalf("release the chain lock: %v", err)
	}
	if err := <-appended; err != nil {
		t.Fatalf("Append after the lock was released: %v", err)
	}
	if got, err := s.Get(auditReaderCtx(), ev.EventID); err != nil || got == nil {
		t.Fatalf("the event was not written after the wait: %v, %v", got, err)
	}
}

// TestAuditConcurrentWritersKeepOneChain is the failure the round-11 report
// found: two instances sharing a database each read their own tail and commit,
// and /api/audit/verify reports the fork. With the lock the rows written by two
// pools must still form one verifiable chain.
func TestAuditConcurrentWritersKeepOneChain(t *testing.T) {
	db := requireAuditLedger(t)
	first := NewAuditStore(db)
	second := NewAuditStore(secondAuditHandle(t))
	t.Cleanup(func() { auditCleanup(t, first) })

	tail, before, err := first.LastHash(auditReaderCtx())
	if err != nil {
		t.Fatalf("LastHash: %v", err)
	}

	tag := uniqueName("audit-race")
	const writers, perWriter = 4, 4
	var wg sync.WaitGroup
	failures := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			store := first
			if w%2 == 1 {
				store = second
			}
			for i := 0; i < perWriter; i++ {
				ev := auditTestEvent(t, fmt.Sprintf("%s-%d-%d", tag, w, i), tag, "tasks.get", auditOldTime, 200, true)
				if _, err := store.Append(auditWriterCtx(), []audit.Event{ev}); err != nil {
					failures <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent Append: %v", err)
	}

	rows, err := first.Segment(auditReaderCtx(), time.Time{}, before, 0)
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	ours := make([]audit.Event, 0, writers*perWriter)
	for _, row := range rows {
		if !strings.HasPrefix(row.EventID, tag) {
			// The developer machine may run a live instance against the same
			// database; a foreign row inside the window makes the continuity
			// assertion meaningless rather than failing. CI has no such writer,
			// so the check still runs where it matters.
			t.Skipf("the ledger received a foreign row (%s) during the window", row.EventID)
		}
		ours = append(ours, row)
	}
	if len(ours) != writers*perWriter {
		t.Fatalf("rows in the window = %d, want %d", len(ours), writers*perWriter)
	}
	if ours[0].PrevHash != tail {
		t.Fatalf("the window starts at %q, want the tail it inherited %q", ours[0].PrevHash, tail)
	}
	if _, broken := audit.VerifySegment(tail, ours); broken != nil {
		t.Fatalf("two writers forked the chain: %+v", broken)
	}
}
