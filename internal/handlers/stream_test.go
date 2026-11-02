package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

// The console stream has three shapes: a snapshot for a task that never reported
// an event, a replay for one that did, and a live tail that ends on the terminal
// frame. All three are exercised here because a stuck SSE handler is invisible in
// a unit test that only checks the status code.

func streamRequest(t *testing.T, h http.Handler, target string, cancelAfter time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if cancelAfter > 0 {
		go func() {
			time.Sleep(cancelAfter)
			cancel()
		}()
	} else {
		defer cancel()
	}
	r := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestConcatenatedPathSegmentIsNotATask(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	// No task id at all: there is nothing to attach the stream to.
	w := do(t, h, http.MethodGet, "/a2a/stream", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if code := errorCode(t, w); code != "not_found" {
		t.Fatalf("code = %q", code)
	}
	if w := do(t, h, http.MethodGet, "/a2a/stream?task=nope", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown task: status = %d", w.Code)
	}
}

func TestConsoleStreamReplaysASnapshotForAQuietTask(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)

	// Created directly in the store: submitted, never advanced, no events.
	live := d.Hub.Store().Create(context.Background(), entry.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hi")))

	rec := streamRequest(t, h, "/a2a/stream?task="+live.ID, 100*time.Millisecond)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	out := rec.Body.String()
	// A task with no events still renders its current status, otherwise the
	// console would show an empty drawer for a live task.
	if !strings.Contains(out, "event: "+a2a.EventTaskStatus) {
		t.Fatalf("snapshot = %s", out)
	}
	if !strings.Contains(out, `"state":"submitted"`) {
		t.Fatalf("snapshot = %s", out)
	}
}

func TestConsoleStreamEndsOnTheTerminalFrame(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)

	live := d.Hub.Store().Create(context.Background(), entry.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hi")))
	go func() {
		// Wait for the handler to subscribe, then report the end of the run.
		time.Sleep(80 * time.Millisecond)
		d.Hub.Broker().Publish(hub.Event{
			TaskID: live.ID,
			Kind:   hub.EventKindStatus,
			Status: &a2a.TaskStatusUpdateEvent{
				Kind:   a2a.KindStatusUpdate,
				TaskID: live.ID,
				Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
				Final:  true,
			},
		})
	}()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- streamRequest(t, h, "/a2a/stream?task="+live.ID, 0) }()

	select {
	case rec := <-done:
		out := rec.Body.String()
		if !strings.Contains(out, `"state":"completed"`) {
			t.Fatalf("live frames = %s", out)
		}
		if strings.Contains(out, "[DONE]") {
			t.Fatal("A2A streaming has no sentinel frame")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the terminal frame must end the stream instead of parking the console")
	}
}

func TestConsoleStreamStopsWhenTheClientHangsUp(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)

	live := d.Hub.Store().Create(context.Background(), entry.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hi")))
	started := time.Now()
	streamRequest(t, h, "/a2a/stream?id="+live.ID, 80*time.Millisecond)
	// The handler must notice the disconnect rather than hold a broker
	// subscription for the life of the process.
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the handler ignored the cancelled request for %v", elapsed)
	}
	if got := d.Hub.Broker().SubscriberCount(live.ID); got != 0 {
		t.Fatalf("subscribers = %d, want the stream to release its subscription", got)
	}
}

func TestTaskEventStreamReplaysThenClosesForAFinishedTask(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)
	id := runOnce(t, h, entry.ID)

	// A finished task has nothing live to add, so the stream replays and closes
	// without a cancel: if this hangs, the test times out instead of passing.
	rec := streamRequest(t, h, "/api/tasks/"+id+"/events?since=0", 0)
	out := rec.Body.String()
	if !strings.Contains(out, a2a.EventTaskStatus) {
		t.Fatalf("replay = %s", out)
	}
	if !strings.Contains(out, `"state":"completed"`) {
		t.Fatalf("replay = %s", out)
	}

	// An unparsable cursor replays from the beginning rather than erroring: a
	// reconnecting console must never be stuck because of one bad query string.
	rec = streamRequest(t, h, "/api/tasks/"+id+"/events?since=soon", 0)
	if out := rec.Body.String(); !strings.Contains(out, a2a.EventTaskStatus) {
		t.Fatalf("bad cursor replay = %s", out)
	}
}
