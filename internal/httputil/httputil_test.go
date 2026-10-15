package httputil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// plainWriter deliberately does not implement http.Flusher.
type plainWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func (w *plainWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *plainWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

func (w *plainWriter) WriteHeader(status int) { w.status = status }

func TestWriteJSONAndError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusCreated, map[string]string{"hello": "world"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if body["hello"] != "world" {
		t.Fatalf("body = %+v", body)
	}

	// A nil payload writes the status only.
	w = httptest.NewRecorder()
	WriteJSON(w, http.StatusNoContent, nil)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	WriteError(w, http.StatusUnauthorized, CodeUnauthorized)
	var errBody ErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errBody.Code != "unauthorized" {
		t.Fatalf("code = %q", errBody.Code)
	}
}

func TestWriteErrMapsCodesToStatus(t *testing.T) {
	cases := []struct {
		code   Code
		status int
	}{
		{CodeBadRequest, http.StatusBadRequest},
		{CodeUnauthorized, http.StatusUnauthorized},
		{CodeNotFound, http.StatusNotFound},
		{CodeMethodNotAllowed, http.StatusMethodNotAllowed},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge},
		{Code("resource_not_found"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		WriteErr(w, &Error{Code: c.code})
		if w.Code != c.status {
			t.Errorf("%s: status = %d, want %d", c.code, w.Code, c.status)
		}
		var body ErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Code != string(c.code) {
			t.Errorf("code = %q, want %q", body.Code, c.code)
		}
	}

	// Any non httputil error degrades to internal_error, never to prose.
	w := httptest.NewRecorder()
	WriteErr(w, context.Canceled)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), string(CodeInternal)) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestAsError(t *testing.T) {
	if AsError(&Error{Code: CodeNotFound}).Code != CodeNotFound {
		t.Fatal("AsError must keep the code")
	}
	if AsError(context.DeadlineExceeded).Code != CodeInternal {
		t.Fatal("unknown errors must become internal_error")
	}
	if (&Error{Code: CodeBadRequest}).Error() != "bad_request" {
		t.Fatal("Error must stringify to its code")
	}
}

func TestDecode(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	var got payload
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"aria"}`))
	if err := Decode(w, r, &got, 1<<10); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "aria" {
		t.Fatalf("payload = %+v", got)
	}

	// Empty body, malformed body and unknown fields all surface as codes.
	for _, tc := range []struct {
		name string
		body string
		code Code
	}{
		{"empty", ``, CodeBadRequest},
		{"malformed", `{"name":`, CodeBadRequest},
		{"unknown field", `{"name":"aria","nope":1}`, CodeBadRequest},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		err := Decode(w, r, &payload{}, 1<<10)
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
		if AsError(err).Code != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, AsError(err).Code, tc.code)
		}
	}

	// An oversized body is its own code so clients can react to the limit.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"`+strings.Repeat("x", 512)+`"}`))
	if err := Decode(w, r, &payload{}, 32); AsError(err).Code != CodePayloadTooLarge {
		t.Fatalf("code = %s, want %s", AsError(err).Code, CodePayloadTooLarge)
	}
}

func TestStreamRequiresFlusher(t *testing.T) {
	w := &plainWriter{}
	if _, ok := Stream(w); ok {
		t.Fatal("a writer without Flush must not be upgraded to SSE")
	}

	rec := httptest.NewRecorder()
	flusher, ok := Stream(rec)
	if !ok {
		t.Fatal("httptest recorder must be flusher capable")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers = %+v", rec.Header())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if err := WriteSSE(rec, flusher, "ping", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("write sse: %v", err)
	}
}

func TestWriteSSEEscapesNewlines(t *testing.T) {
	rec := httptest.NewRecorder()
	flusher, ok := Stream(rec)
	if !ok {
		t.Fatal("expected a flusher")
	}
	if err := WriteSSE(rec, flusher, "message", map[string]string{"text": "line1\nline2\r\nline3"}); err != nil {
		t.Fatalf("write sse: %v", err)
	}
	out := rec.Body.String()
	if strings.Count(out, "\n\n") != 1 {
		t.Fatalf("payload newlines leaked into the frame: %q", out)
	}
	if !strings.Contains(out, "event: message\n") || !strings.Contains(out, `line1\nline2`) {
		t.Fatalf("frame = %q", out)
	}
	// The data payload stays parseable JSON.
	var payload map[string]string
	line := strings.TrimPrefix(strings.Split(out, "\n")[1], "data: ")
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("data is not JSON: %v (%s)", err, line)
	}

	if err := WriteRawSSE(rec, flusher, "raw", `{"already":"json"}`); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "event: raw") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCommentAndKeepAlive(t *testing.T) {
	rec := httptest.NewRecorder()
	flusher, ok := Stream(rec)
	if !ok {
		t.Fatal("expected a flusher")
	}
	if err := Comment(rec, flusher, "keep-alive"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if !strings.HasPrefix(rec.Body.String(), ": keep-alive") {
		t.Fatalf("body = %q", rec.Body.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	KeepAlive(ctx, rec, flusher, 10*time.Millisecond)
	if !strings.Contains(rec.Body.String(), "keep-alive") {
		t.Fatalf("expected keep-alive frames: %q", rec.Body.String())
	}

	// A cancelled context returns immediately instead of leaking a goroutine.
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	done := make(chan struct{})
	go func() {
		KeepAlive(cancelled, httptest.NewRecorder(), nil, 10*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepAlive did not honour the cancelled context")
	}
}
