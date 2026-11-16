package httputil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// failingWriter refuses every write and reports the error it was given, so the
// SSE helpers can be shown to pass a broken connection back to their caller
// instead of swallowing it.
type failingWriter struct {
	header http.Header
	err    error
}

func (w *failingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingWriter) WriteHeader(int) {}

func (w *failingWriter) Write([]byte) (int, error) { return 0, w.err }

// A payload the JSON encoder cannot represent is a programming error: the status
// is already on the wire, so the only honest answer is a logged failure.
func TestWriteJSONSurvivesAnUnencodablePayload(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, func() {}) // must not panic
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the header that was already written", rec.Code)
	}
}

// Same for the frame builder: an unencodable payload must surface as an error
// the stream loop can stop on.
func TestWriteSSEReportsAnUnencodablePayload(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteSSE(rec, nil, "message", func() {}); err == nil {
		t.Fatal("an unencodable payload must be reported")
	}
}

// A connection that breaks mid-frame must abort the write, not leave the caller
// believing the frame went out.
func TestSSEFrameWritesReportABrokenConnection(t *testing.T) {
	for _, err := range []error{http.ErrBodyNotAllowed, errors.New("broken pipe")} {
		w := &failingWriter{err: err}
		if got := WriteRawSSE(w, nil, "event", "data"); !errors.Is(got, err) {
			t.Fatalf("WriteRawSSE = %v, want %v", got, err)
		}
		if got := Comment(w, nil, "keep-alive"); !errors.Is(got, err) {
			t.Fatalf("Comment = %v, want %v", got, err)
		}
	}
}

// KeepAlive treats a zero interval as "the shipped default" rather than a hot
// loop, and stops as soon as the context is done.
func TestKeepAliveDefaultsTheInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		KeepAlive(ctx, httptest.NewRecorder(), nil, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepAlive must honour the cancelled context")
	}
}

// A keep-alive that cannot be written ends the loop. ErrBodyNotAllowed is the
// normal way a client hangs up on a stream, so it must not be logged as a fault;
// every other error is worth a debug line.
func TestKeepAliveStopsOnABrokenConnection(t *testing.T) {
	for _, err := range []error{http.ErrBodyNotAllowed, errors.New("broken pipe")} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			KeepAlive(ctx, &failingWriter{err: err}, nil, time.Millisecond)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("a failing keep-alive must end the loop")
		}
	}
}
