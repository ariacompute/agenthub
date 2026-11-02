package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sink captures what the middleware records.
type sink struct {
	mu     sync.Mutex
	events []Event
}

func (s *sink) Record(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *sink) last() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return Event{}, false
	}
	return s.events[len(s.events)-1], true
}

// TestTapRecordsStatusAndIdentity is the happy path: the middleware observes the
// response, keeps the caller's annotations and never touches the body.
func TestTapRecordsStatusAndIdentity(t *testing.T) {
	s := &sink{}
	h := Tap(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tr := FromContext(r.Context()); tr != nil {
			tr.NotePrincipal(ActorUser, "u-1", "a@b.c", "u-1")
			tr.NoteAction("message/send")
			tr.NoteResource("res-1", "agent")
			tr.NoteMeta("count", 2)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader("{}"))
	req.Header.Set("User-Agent", "agent-ua")
	h.ServeHTTP(rec, req)

	ev, ok := s.last()
	if !ok {
		t.Fatal("nothing recorded")
	}
	if ev.Status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", ev.Status)
	}
	if ev.ActorID != "u-1" || ev.Action != "message/send" || ev.ResourceID != "res-1" {
		t.Fatalf("annotations lost: %+v", ev)
	}
	if ev.Source != SourceA2A {
		t.Fatalf("source = %q, want a2a", ev.Source)
	}
	if ev.Metadata["count"] != 2 {
		t.Fatalf("metadata lost: %+v", ev.Metadata)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body altered: %q", rec.Body.String())
	}
	if ev.EventID == "" || len(ev.EventID) != 32 {
		t.Fatalf("event id = %q, want 128 bit hex", ev.EventID)
	}
}

// TestTapRedactsQueryValues keeps a token out of the trail: the key survives so
// the shape of the call is still readable.
func TestTapRedactsQueryValues(t *testing.T) {
	s := &sink{}
	Tap(s, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/tasks?access_token=abc123", nil))
	ev, ok := s.last()
	if !ok {
		t.Fatal("nothing recorded")
	}
	if want := "/api/tasks?access_token=REDACTED"; ev.Path != want {
		t.Fatalf("path = %q, want %q", ev.Path, want)
	}
}

// TestTapRecordsRefusal is the failure path: a 403 leaves a row with a machine
// readable code, which is what the console filters on.
func TestTapRecordsRefusal(t *testing.T) {
	s := &sink{}
	Tap(s, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/resources/x", nil))
	ev, _ := s.last()
	if ev.ErrorCode != "forbidden" {
		t.Fatalf("error code = %q, want forbidden", ev.ErrorCode)
	}
	if ev.ReadOnly {
		t.Fatal("a DELETE is not a read")
	}
}

// TestTapWithoutSinkIsTransparent guards against a nil recorder: an instance with
// the trail switched off must behave exactly as before.
func TestTapWithoutSinkIsTransparent(t *testing.T) {
	called := false
	Tap(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !called {
		t.Fatal("the chain must still run without a sink")
	}
}

// TestTemplatePathGroupsIdentifiers keeps the action name a family rather than
// one row per id.
func TestTemplatePathGroupsIdentifiers(t *testing.T) {
	cases := map[string]string{
		"/api/resources/abc123def456ghi789jkl012": "/api/resources/{id}",
		"/api/tasks/42/spans":                     "/api/tasks/{id}/spans",
		"/api/resources":                          "/api/resources",
		"/a2a":                                    "/a2a",
	}
	for in, want := range cases {
		if got := templatePath(in); got != want {
			t.Fatalf("templatePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRecorderDropsWhenFull keeps the promise that recording never blocks: the
// queue is bounded and a full queue is counted, not waited on.
func TestRecorderDropsWhenFull(t *testing.T) {
	r := New(nil, Options{Queue: 1, Batch: 1, FlushEvery: time.Hour})
	r.Record(Event{EventID: "a"})
	r.Record(Event{EventID: "b"})
	if r.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", r.Dropped())
	}
}

// TestRecorderWritesOnClose covers the shutdown path: events still in the queue
// are stamped with a linear chain and counted as written.
func TestRecorderWritesOnClose(t *testing.T) {
	r := New(nil, Options{Queue: 8, Batch: 4, FlushEvery: time.Hour})
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	r.Record(Event{EventID: "a"})
	r.Record(Event{EventID: "b"})
	r.Close()
	<-done
	if r.Written() != 2 {
		t.Fatalf("written = %d, want 2", r.Written())
	}
}

// TestRecorderWithoutStoreIsSafe keeps /healthz and unit tests working without a
// database: the chain still advances in memory.
func TestRecorderWithoutStoreIsSafe(t *testing.T) {
	r := New(nil, Options{})
	r.Record(Event{EventID: "a"})
	r.Close()
	if _, err := r.List(context.Background(), Query{}); err != nil {
		t.Fatalf("list without a store: %v", err)
	}
	if v, err := r.Verify(context.Background(), time.Time{}, time.Time{}, 0); err != nil || !v.OK {
		t.Fatalf("verify without a store = %+v, %v", v, err)
	}
}
