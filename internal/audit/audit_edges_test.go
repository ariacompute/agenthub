package audit

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// noEntropy stands in for a broken random source. crypto/rand.Reader is a
// package variable precisely so a caller can do this.
type noEntropy struct{}

func (noEntropy) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// A writer that was never started, or one that was started twice, must return
// instead of writing the same chain from two goroutines.
func TestRunStartsAtMostOnce(t *testing.T) {
	var nilRec *Recorder
	nilRec.Run(context.Background()) // must not panic

	rec := New(nil, Options{FlushEvery: time.Hour})
	start(t, rec)
	rec.Run(context.Background()) // second start returns immediately
	rec.Close()
}

// A quiet instance still has to flush: the ticker is what bounds how long an
// event may sit in memory.
func TestTickerFlushesAPartialBatch(t *testing.T) {
	rec := New(nil, Options{FlushEvery: 5 * time.Millisecond, Batch: 8})
	start(t, rec)
	rec.Record(Event{EventID: "e1"})
	rec.Record(Event{EventID: "e2"})

	deadline := time.Now().Add(2 * time.Second)
	for rec.Written() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("written = %d, want the ticker to flush both rows", rec.Written())
		}
		time.Sleep(time.Millisecond)
	}
	rec.Close()
}

// A cancelled context is a shutdown too: whatever is in the batch is written
// before the loop returns.
func TestCancelledContextFlushesTheBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := New(nil, Options{FlushEvery: time.Hour, Batch: 8})
	go rec.Run(ctx)
	for !rec.started.Load() {
		time.Sleep(time.Millisecond)
	}
	rec.Record(Event{EventID: "e1"})
	rec.Record(Event{EventID: "e2"})
	time.Sleep(20 * time.Millisecond) // let the loop pick both rows up

	cancel()
	select {
	case <-rec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer did not stop on cancellation")
	}
	if rec.Written() != 2 {
		t.Fatalf("written = %d, want the batch flushed before the return", rec.Written())
	}
}

// A full batch is written as soon as it fills, without waiting for the ticker.
func TestAFullBatchIsFlushedImmediately(t *testing.T) {
	store := &fakeStore{}
	rec := New(store, Options{FlushEvery: time.Hour, Batch: 2})
	start(t, rec)
	rec.Record(Event{EventID: "e1"})
	rec.Record(Event{EventID: "e2"})

	deadline := time.Now().Add(2 * time.Second)
	for rec.Written() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("written = %d, want the batch flushed as soon as it filled", rec.Written())
		}
		time.Sleep(time.Millisecond)
	}
	rec.Close()
}

// gatedStore holds the writer inside the chain-tail read, which is the only way
// to have rows waiting in the queue when stop closes.
type gatedStore struct {
	*fakeStore
	entered chan struct{}
	gate    chan struct{}
}

func (s *gatedStore) LastHash(ctx context.Context) (string, int64, error) {
	close(s.entered)
	<-s.gate
	return s.fakeStore.LastHash(ctx)
}

// TestStopDrainsWhateverIsQueued pins the shutdown guarantee: rows already
// accepted are written, not dropped. The select in Run chooses at random between
// "stop" and "an event arrived", so the loop below walks both legal orders and
// asserts the same outcome for each.
func TestStopDrainsWhateverIsQueued(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
		store := &gatedStore{
			fakeStore: &fakeStore{},
			entered:   make(chan struct{}),
			gate:      make(chan struct{}),
		}
		rec := New(store, Options{Queue: 8, Batch: 2, FlushEvery: time.Hour})
		go rec.Run(context.Background())
		<-store.entered

		for _, id := range []string{"e1", "e2", "e3", "e4"} {
			rec.Record(Event{EventID: id})
		}
		close(rec.stop)
		close(store.gate)

		select {
		case <-rec.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the writer did not drain on stop")
		}
		if got := rec.Written(); got != 4 {
			t.Fatalf("attempt %d: written = %d, want every queued row drained", attempt, got)
		}
		if len(store.rows) != 4 {
			t.Fatalf("attempt %d: rows = %d, want 4", attempt, len(store.rows))
		}
	}
}

// segmentErrStore fails the paging read: a verifier that cannot read the table
// must report that, never "the chain is fine".
type segmentErrStore struct {
	*fakeStore
	err error
}

func (s *segmentErrStore) Segment(context.Context, time.Time, int64, int) ([]Event, error) {
	return nil, s.err
}

// Verify defaults its page size and reports a read failure; a nil context is the
// background sweep shape and must not dereference.
func TestVerifyDefaultsItsPageAndReportsReadFailures(t *testing.T) {
	rows := prunedChain("", "first")
	got, err := New(&fakeStore{segment: [][]Event{rows}}, Options{}).Verify(nil, time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Verify with a nil context: %v", err)
	}
	if !got.OK || got.Checked != 1 {
		t.Fatalf("verification = %+v, want the whole chain checked", got)
	}

	down := errors.New("table unavailable")
	if _, err := New(&segmentErrStore{fakeStore: &fakeStore{}, err: down}, Options{}).
		Verify(context.Background(), time.Time{}, time.Time{}, 10); !errors.Is(err, down) {
		t.Fatalf("err = %v, want the read failure", err)
	}
}

// Metadata that cannot be marshalled contributes nothing to the digest instead
// of panicking: the row still has to be written.
func TestCanonicalSurvivesUnencodableMetadata(t *testing.T) {
	ev := Event{EventID: "e1", Metadata: map[string]any{"callback": func() {}}}
	first := Hash("", ev)
	// A second event without metadata hashes the same canonical tail, so the
	// unencodable map contributes an empty segment rather than garbage.
	plain := Event{EventID: "e1"}
	if want := Hash("", plain); first != want {
		t.Fatalf("hash = %q, want %q", first, want)
	}
}

// A handler that clears the action still produces a usable row: the middleware
// falls back to the method and the templated path.
func TestTapFallsBackToTheDefaultAction(t *testing.T) {
	s := &sink{}
	h := Tap(s, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if tr := FromContext(r.Context()); tr != nil {
			tr.NoteAction("")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/api/tasks/42", nil))

	ev, ok := s.last()
	if !ok {
		t.Fatal("nothing recorded")
	}
	if ev.Action != "PATCH /api/tasks/{id}" {
		t.Fatalf("action = %q, want the default action", ev.Action)
	}
}

// With no entropy the event id falls back to a timestamp: the trail keeps
// flowing and stays unique enough per row, which beats refusing to record.
func TestNewIDFallsBackWithoutEntropy(t *testing.T) {
	orig := rand.Reader
	rand.Reader = noEntropy{}
	t.Cleanup(func() { rand.Reader = orig })

	id := newID()
	if id == "" {
		t.Fatal("the fallback id must not be empty")
	}
	// Hex of a UnixNano timestamp: 12..16 digits, no padding or prose.
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("id = %q, want hex digits", id)
		}
	}
}
