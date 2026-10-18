package handlers

// Streaming and sessions: the two places where a caller holds a long lived
// connection or a long lived credential. Both are easy to get subtly wrong — a
// cursor that replays the wrong window, or a session list that tells a user
// which of their own sessions is the current one — and neither shows up as an
// error code, so they are asserted here rather than left to the console.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/a2a"
	"agenthub/internal/hub"
)

func TestSinceSeqReplaysFromTheStartWhenTheCursorIsUnusable(t *testing.T) {
	cases := []struct {
		raw  string
		want uint64
	}{
		{"", 0},
		{"   ", 0},
		{"42", 42},
		{" 42 ", 42},
		{"0", 0},
		{"18446744073709551615", 18446744073709551615},
		// A cursor that is not a number is a cold start, not an error: a client
		// that lost its place must be able to reconnect.
		{"not-a-number", 0},
		{"-1", 0},
		{"12.5", 0},
	}
	for _, c := range cases {
		if got := sinceSeq(c.raw); got != c.want {
			t.Errorf("sinceSeq(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestIsTerminalEventEndsTheStreamOnTheRightFrames(t *testing.T) {
	status := func(state a2a.TaskState) *hub.Event {
		return &hub.Event{Kind: hub.EventKindStatus, Status: &a2a.TaskStatusUpdateEvent{Status: a2a.TaskStatus{State: state}}}
	}
	terminal := []a2a.TaskState{
		a2a.TaskStateCompleted,
		a2a.TaskStateFailed,
		a2a.TaskStateCanceled,
		// input-required ends the stream too: the task is parked waiting for a
		// human, so there is nothing left to stream until it is resumed.
		a2a.TaskStateInputRequired,
	}
	for _, state := range terminal {
		if !isTerminalEvent(status(state)) {
			t.Errorf("state %q must end the stream", state)
		}
	}
	for _, state := range []a2a.TaskState{a2a.TaskStateSubmitted, a2a.TaskStateWorking} {
		if isTerminalEvent(status(state)) {
			t.Errorf("state %q must not end the stream", state)
		}
	}
	// Anything that is not a status update carries no terminal information.
	if isTerminalEvent(&hub.Event{Kind: hub.EventKindArtifact}) {
		t.Error("an artifact frame must not end the stream")
	}
	if isTerminalEvent(&hub.Event{Kind: hub.EventKindStatus}) {
		t.Error("a status frame with no status must not end the stream")
	}
}

// Every event kind has to leave as a frame the A2A client recognises; an
// unknown kind still has to leave, because dropping it would silently truncate
// the trace.
func TestWriteEventFrameNamesEveryKind(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{hub.EventKindStatus, string(a2a.EventTaskStatus)},
		{hub.EventKindArtifact, string(a2a.EventTaskArtifact)},
		{hub.EventKindMessage, string(a2a.EventTaskMessage)},
		{hub.EventKindSpan, string(a2a.EventTaskSpan)},
		{"something.new", "something.new"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		flusher := &noopFlusher{rec}
		ev := &hub.Event{Kind: c.kind}
		switch c.kind {
		case hub.EventKindStatus:
			ev.Status = &a2a.TaskStatusUpdateEvent{}
		case hub.EventKindArtifact:
			ev.Artifact = &a2a.TaskArtifactUpdateEvent{}
		case hub.EventKindMessage:
			ev.Message = &a2a.Message{}
		case hub.EventKindSpan:
			ev.Span = &hub.Span{}
		}
		if err := writeEventFrame(rec, flusher, ev); err != nil {
			t.Fatalf("%s: writeEventFrame: %v", c.kind, err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "event: "+c.want) {
			t.Errorf("%s: frame = %q, want it named %q", c.kind, body, c.want)
		}
		if !strings.Contains(body, "data:") {
			t.Errorf("%s: frame = %q, want a data line", c.kind, body)
		}
	}
}

// noopFlusher is the http.Flusher an httptest recorder does not provide.
type noopFlusher struct {
	rec *httptest.ResponseRecorder
}

func (f *noopFlusher) Flush() { f.rec.Flush() }
