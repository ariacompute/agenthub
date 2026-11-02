package audit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeStore is the persistence port in memory. The recorder is the part under
// test here: it owns the chain, the batching and the retention window, and it
// must behave the same whether the rows land in PostgreSQL or in this map.
type fakeStore struct {
	rows      []Event
	appendErr error
	lastErr   error
	segment   [][]Event // one page per call, in order
	stats     Stats
	pruned    int64
	pruneErr  error

	appendCalls int
	lastCtx     context.Context
	cutoff      time.Time
}

func (f *fakeStore) Append(ctx context.Context, events []Event) error {
	f.appendCalls++
	f.lastCtx = ctx
	if f.appendErr != nil {
		return f.appendErr
	}
	f.rows = append(f.rows, events...)
	return nil
}

func (f *fakeStore) LastHash(context.Context) (string, int64, error) {
	if f.lastErr != nil {
		return "", 0, f.lastErr
	}
	if len(f.rows) == 0 {
		return "", 0, nil
	}
	last := f.rows[len(f.rows)-1]
	return last.Hash, last.Seq, nil
}

func (f *fakeStore) List(_ context.Context, q Query) ([]Event, error) {
	if q.Search == "boom" {
		return nil, errors.New("list failed")
	}
	return f.rows, nil
}

func (f *fakeStore) Get(_ context.Context, eventID string) (*Event, error) {
	for i := range f.rows {
		if f.rows[i].EventID == eventID {
			return &f.rows[i], nil
		}
	}
	return nil, nil
}

func (f *fakeStore) Stats(context.Context, Query) (Stats, error) { return f.stats, nil }

func (f *fakeStore) Segment(_ context.Context, _ time.Time, _ int64, _ int) ([]Event, error) {
	if len(f.segment) == 0 {
		return nil, nil
	}
	page := f.segment[0]
	f.segment = f.segment[1:]
	return page, nil
}

func (f *fakeStore) Prune(_ context.Context, cutoff time.Time) (int64, error) {
	f.cutoff = cutoff
	return f.pruned, f.pruneErr
}

// start runs the writer and waits until it owns the chain, so a test cannot
// close the recorder before its goroutine has even begun.
func start(t *testing.T, rec *Recorder) {
	t.Helper()
	go rec.Run(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for !rec.started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the writer never started")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRunResumesTheStoredChain is the restart guarantee: a new process must
// link onto the last row it finds instead of starting a second chain, or
// /api/audit/verify would report every restart as tampering.
func TestRunResumesTheStoredChain(t *testing.T) {
	prev := &Event{Seq: 7, EventID: "prev", Hash: "deadbeef"}
	store := &fakeStore{rows: []Event{*prev}}
	rec := New(store, Options{FlushEvery: 5 * time.Millisecond, Batch: 2})
	start(t, rec)

	rec.Record(Event{EventID: "new", Method: http.MethodGet})
	rec.Close()

	if store.appendCalls == 0 || len(store.rows) < 2 {
		t.Fatalf("the new event never reached the store: %+v", store.rows)
	}
	written := store.rows[len(store.rows)-1]
	if written.PrevHash != prev.Hash {
		t.Fatalf("prev_hash = %q, want the stored tail %q", written.PrevHash, prev.Hash)
	}
	if written.Hash != Hash(prev.Hash, written) {
		t.Fatalf("hash does not cover the row: %+v", written)
	}
	if rec.Written() != 1 {
		t.Fatalf("written = %d, want 1", rec.Written())
	}
}

// TestRunContinuesAfterAReadFailure: an unreadable tail must not stop the
// writer. The chain restarts and verify reports the break, which is visible;
// silently dropping the whole trail would not be.
func TestRunContinuesAfterAReadFailure(t *testing.T) {
	store := &fakeStore{lastErr: errors.New("database is down")}
	rec := New(store, Options{FlushEvery: 5 * time.Millisecond})
	start(t, rec)

	rec.Record(Event{EventID: "e1"})
	rec.Close()

	if rec.Written() != 1 {
		t.Fatalf("written = %d, want the event recorded anyway", rec.Written())
	}
	if store.rows[0].PrevHash != "" {
		t.Fatalf("prev_hash = %q, want a fresh chain", store.rows[0].PrevHash)
	}
}

// TestAppendFailureIsCountedNotFatal: the request that was being audited has
// already been answered by the time the batch is written, so a failed insert
// can only be counted and logged.
func TestAppendFailureIsCountedNotFatal(t *testing.T) {
	store := &fakeStore{appendErr: errors.New("insert failed")}
	rec := New(store, Options{FlushEvery: 5 * time.Millisecond, Queue: 8, Batch: 8})
	start(t, rec)

	rec.Record(Event{EventID: "e1"})
	rec.Record(Event{EventID: "e2"})
	rec.Close()

	if rec.Written() != 0 {
		t.Fatalf("written = %d, want 0 when the store refused", rec.Written())
	}
	if len(store.rows) != 0 {
		t.Fatalf("rows landed despite the error: %+v", store.rows)
	}
}

// TestRecordOnAClosedRecorderIsSafe covers the shutdown race: a request still
// in flight when Close runs must not panic on a closed queue.
func TestRecordOnAClosedRecorderIsSafe(t *testing.T) {
	rec := New(nil, Options{FlushEvery: time.Millisecond, Queue: 1})
	rec.Record(Event{EventID: "before"}) // queue now full; the next one is dropped
	rec.Record(Event{EventID: "dropped"})
	if rec.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", rec.Dropped())
	}
	var nilRec *Recorder
	nilRec.Record(Event{}) // must not panic
	nilRec.Close()
}

// TestVerifyWalksEverySegment: the verifier pages through the table, so a break
// beyond the first page must still be found, and `checked` must count the rows
// that were actually read.
func TestVerifyWalksEverySegment(t *testing.T) {
	first := Event{Seq: 1, EventID: "a", Hash: ""}
	first.Hash = Hash("", first)
	second := Event{Seq: 2, EventID: "b", PrevHash: first.Hash}
	second.Hash = Hash(first.Hash, second)
	third := Event{Seq: 3, EventID: "c", PrevHash: second.Hash}
	third.Hash = Hash(second.Hash, third)

	ok := &fakeStore{segment: [][]Event{{first}, {second}, {third}}}
	rec := New(ok, Options{})
	got, err := rec.Verify(context.Background(), time.Time{}, time.Time{}, 1)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.OK || got.Checked != 3 {
		t.Fatalf("verification = %+v, want ok over 3 rows", got)
	}

	// Now break the third row: the walk must report it, and say how far it got.
	broken := third
	broken.Hash = Hash(second.Hash, Event{EventID: "something else"})
	bad := &fakeStore{segment: [][]Event{{first}, {second}, {broken}}}
	got, err = New(bad, Options{}).Verify(context.Background(), time.Time{}, time.Time{}, 1)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// `checked` counts the rows the walk actually looked at, including the one
	// that failed: 2 clean pages and the broken row.
	if got.OK || got.BrokenAt != "c" || got.Checked != 3 {
		t.Fatalf("verification = %+v, want a break at the third row", got)
	}
}

// TestVerifyWithoutAStoreIsOK keeps the unconfigured server honest: with no
// audit table there is nothing to verify, and saying "broken" would be a lie.
func TestVerifyWithoutAStoreIsOK(t *testing.T) {
	got, err := New(nil, Options{}).Verify(context.Background(), time.Time{}, time.Time{}, 0)
	if err != nil || !got.OK {
		t.Fatalf("verify = %+v, %v", got, err)
	}
}

// TestVerifyIgnoresCallerCancellation: an operator closing the tab must not
// leave a half-checked chain reported as broken.
func TestVerifyIgnoresCallerCancellation(t *testing.T) {
	store := &fakeStore{segment: [][]Event{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := New(store, Options{}).Verify(ctx, time.Time{}, time.Time{}, 100)
	if err != nil || !got.OK {
		t.Fatalf("verify on a cancelled context = %+v, %v", got, err)
	}
}

// TestPruneUsesTheRetentionWindow covers both sides of the switch: with no
// retention the tick must not touch the table, and with one it must cut at
// now-retention.
func TestPruneUsesTheRetentionWindow(t *testing.T) {
	store := &fakeStore{pruned: 4}
	off := New(store, Options{})
	if n, err := off.Prune(context.Background()); n != 0 || err != nil {
		t.Fatalf("prune without retention = %d, %v", n, err)
	}
	if !store.cutoff.IsZero() {
		t.Fatalf("cutoff = %v, want no call at all", store.cutoff)
	}

	on := New(store, Options{Retention: time.Hour})
	n, err := on.Prune(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	want := time.Now().UTC().Add(-time.Hour)
	if d := store.cutoff.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("cutoff = %v, want about %v", store.cutoff, want)
	}
}

// TestQueryHelpersForwardToTheStore: List/Stats/Get are pass-throughs, and each
// must survive being called on an unconfigured recorder.
func TestQueryHelpersForwardToTheStore(t *testing.T) {
	store := &fakeStore{rows: []Event{{EventID: "e1"}}, stats: Stats{Total: 3, Denied: 1, Written: 2}}
	rec := New(store, Options{})

	rows, err := rec.List(context.Background(), Query{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %v, %v", rows, err)
	}
	if _, err := rec.List(context.Background(), Query{Search: "boom"}); err == nil {
		t.Fatal("a failing store must surface its error")
	}
	stats, err := rec.Stats(context.Background(), Query{})
	if err != nil || stats.Total != 3 || stats.Denied != 1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	one, err := rec.Get(context.Background(), "e1")
	if err != nil || one == nil || one.EventID != "e1" {
		t.Fatalf("get = %+v, %v", one, err)
	}
	missing, err := rec.Get(context.Background(), "nope")
	if err != nil || missing != nil {
		t.Fatalf("get missing = %+v, %v", missing, err)
	}

	empty := New(nil, Options{})
	if rows, err := empty.List(context.Background(), Query{}); err != nil || rows != nil {
		t.Fatalf("list without a store = %v, %v", rows, err)
	}
	if stats, err := empty.Stats(context.Background(), Query{}); err != nil || stats.Total != 0 {
		t.Fatalf("stats without a store = %+v, %v", stats, err)
	}
	if one, err := empty.Get(context.Background(), "e1"); err != nil || one != nil {
		t.Fatalf("get without a store = %+v, %v", one, err)
	}
	var nilRec *Recorder
	if stats, err := nilRec.Stats(context.Background(), Query{}); err != nil || stats.Total != 0 {
		t.Fatalf("stats on a nil recorder = %+v, %v", stats, err)
	}
	if one, err := nilRec.Get(context.Background(), "e1"); err != nil || one != nil {
		t.Fatalf("get on a nil recorder = %+v, %v", one, err)
	}
}

// TestOptionsClampToSomethingUsable: the values come from the environment, so a
// batch larger than the queue must be clamped rather than deadlocking.
func TestOptionsClampToSomethingUsable(t *testing.T) {
	opts := Options{Queue: 4, Batch: 100}.withDefaults()
	if opts.Batch != 4 {
		t.Fatalf("batch = %d, want it clamped to the queue", opts.Batch)
	}
	opts = Options{}.withDefaults()
	if opts.Queue != 8192 || opts.Batch != 64 || opts.FlushEvery != 500*time.Millisecond {
		t.Fatalf("defaults = %+v", opts)
	}
	if opts.Logger == nil {
		t.Fatal("a logger must always be set, or the writer panics on its first warning")
	}
}

// TestNoteSettersReachTheEvent pins the annotations the handlers rely on: a
// trace with no error code would send an operator hunting for the response body
// that was deliberately not stored.
func TestNoteSettersReachTheEvent(t *testing.T) {
	ctx, tr := StartTrace(context.Background(), Event{EventID: "e1"})
	if FromContext(ctx) != tr {
		t.Fatal("the trace must travel on the context")
	}
	tr.NoteError("forbidden")
	tr.NoteReadOnly(false)
	tr.NoteMeta("filter", "state=working")
	ev := tr.Snapshot()
	if ev.ErrorCode != "forbidden" || ev.ReadOnly || ev.Metadata["filter"] != "state=working" {
		t.Fatalf("annotations lost: %+v", ev)
	}

	// Nil-safe: the same helpers are called from code that also runs outside a
	// request (background sweeps, migrations).
	var nilTrace *Trace
	nilTrace.NoteError("x")
	nilTrace.NoteReadOnly(true)
	nilTrace.NoteMeta("k", "v")
	if ev := nilTrace.Snapshot(); ev.EventID != "" {
		t.Fatalf("a nil trace must snapshot empty, got %+v", ev)
	}
	if FromContext(nil) != nil {
		t.Fatal("FromContext(nil) must be nil")
	}
	if FromContext(context.Background()) != nil {
		t.Fatal("a context without a trace must return nil")
	}
}

// TestSnapshotCopiesTheMetadata: the caller keeps the map after the trace is
// flushed, and the recorder mutates its own copy.
func TestSnapshotCopiesTheMetadata(t *testing.T) {
	_, tr := StartTrace(context.Background(), Event{EventID: "e1", Metadata: map[string]any{"a": 1}})
	snap := tr.Snapshot()
	snap.Metadata["a"] = 2
	if tr.Snapshot().Metadata["a"] != 1 {
		t.Fatal("snapshot must not share the metadata map")
	}
}

// TestStatusRecorderPassesTheWriterThrough covers the SSE path: Flush and
// Unwrap exist so the stream keeps streaming through the wrapper.
func TestStatusRecorderPassesTheWriterThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	sr.Flush() // httptest.ResponseRecorder implements http.Flusher
	if _, err := sr.Write([]byte("chunk")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sr.WriteHeader(http.StatusAccepted) // already wrote: status stays 200
	if sr.status != http.StatusOK {
		t.Fatalf("status = %d, want the first one", sr.status)
	}
	if sr.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap must expose the real writer")
	}

	// A writer that is not a Flusher must not panic.
	plain := &statusRecorder{ResponseWriter: struct{ http.ResponseWriter }{ResponseWriter: rec}}
	plain.Flush()
}

// TestTapRecordsWhatTheHandlerLeftOut covers the fallbacks: a handler that never
// annotates still produces a usable row, an id comes from the header when the
// caller supplied one, and the status is mapped to a machine code.
func TestTapRecordsWhatTheHandlerLeftOut(t *testing.T) {
	s := &sink{}
	h := Tap(s, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/123456", nil)
	req.Header.Set("X-Request-Id", "req-1")
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	h.ServeHTTP(httptest.NewRecorder(), req)

	ev, ok := s.last()
	if !ok {
		t.Fatal("nothing recorded")
	}
	if ev.RequestID != "req-1" {
		t.Fatalf("request id = %q, want the caller's", ev.RequestID)
	}
	if ev.ErrorCode != "forbidden" {
		t.Fatalf("error code = %q, want it derived from the 403", ev.ErrorCode)
	}
	if ev.IP != "10.0.0.1" {
		t.Fatalf("ip = %q, want the leftmost forwarded hop", ev.IP)
	}
	if ev.Action != "DELETE /api/tasks/{id}" {
		t.Fatalf("action = %q, want the identifier templated", ev.Action)
	}
	if ev.ReadOnly {
		t.Fatal("a DELETE is not a read")
	}
}

// TestCodeFromStatus covers every branch of the status map: it is the fallback
// when a handler forgets to record why it refused.
func TestCodeFromStatus(t *testing.T) {
	cases := map[int]string{
		http.StatusOK:                  "",
		http.StatusCreated:             "",
		http.StatusUnauthorized:        "unauthenticated",
		http.StatusForbidden:           "forbidden",
		http.StatusNotFound:            "not_found",
		http.StatusTooManyRequests:     "rate_limited",
		http.StatusInternalServerError: "internal_error",
		http.StatusBadRequest:          "invalid_request",
		http.StatusTeapot:              "invalid_request",
	}
	for status, want := range cases {
		if got := codeFromStatus(status); got != want {
			t.Fatalf("codeFromStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

// TestClientIPFallsBackToTheSocket covers the paths a proxied deployment does
// not exercise: no header at all, a header without a comma, and a RemoteAddr
// with no port.
func TestClientIPFallsBackToTheSocket(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	if got := clientIP(req); got != "127.0.0.1" {
		t.Fatalf("ip = %q", got)
	}

	req.Header.Set("X-Forwarded-For", "10.0.0.9")
	if got := clientIP(req); got != "10.0.0.9" {
		t.Fatalf("ip = %q, want the single hop", got)
	}

	req.Header.Del("X-Forwarded-For")
	req.RemoteAddr = "unix-socket"
	if got := clientIP(req); got != "unix-socket" {
		t.Fatalf("ip = %q, want the raw address when it has no port", got)
	}
}

// TestRedactAndTruncate covers the two shapes that used to be unhandled: a
// request with no URL at all, and a query string longer than the column.
func TestRedactAndTruncate(t *testing.T) {
	if got := redactPath(nil); got != "" {
		t.Fatalf("redactPath(nil) = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	if got := redactPath(req.URL); got != "/api/tasks" {
		t.Fatalf("redactPath without query = %q", got)
	}

	// Distinct keys: the redactor keeps one `key=REDACTED` per key, so a long
	// string of the same key would collapse to a single pair.
	var q strings.Builder
	for i := 0; i < 400; i++ {
		q.WriteString("k")
		q.WriteString(strconv.Itoa(i))
		q.WriteString("=v&")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/tasks?"+q.String(), nil)
	if got := redactPath(req.URL); len(got) != 512 {
		t.Fatalf("redacted path length = %d, want it truncated to 512", len(got))
	}
	if got := truncate("short", 512); got != "short" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate(strings.Repeat("a", 600), 512); len(got) != 512 {
		t.Fatalf("truncate = %d bytes, want 512", len(got))
	}
}

// TestSourceOf covers the three sources the console filters by.
func TestSourceOf(t *testing.T) {
	cases := map[string]string{
		"/a2a":              SourceA2A,
		"/api/auth/login":   SourceAuth,
		"/api/tasks":        SourceAPI,
		"/healthz":          SourceAPI,
		"/api/authz/groups": SourceAPI,
	}
	for path, want := range cases {
		if got := sourceOf(path); got != want {
			t.Fatalf("sourceOf(%q) = %q, want %q", path, got, want)
		}
	}
}
