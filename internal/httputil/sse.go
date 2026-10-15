package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Stream opens an SSE response. It returns false when the client cannot be
// upgraded to a streaming response (already written, hijacked, ...).
func Stream(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return flusher, true
}

// WriteSSE writes one named SSE frame whose data payload is JSON encoded.
func WriteSSE(w http.ResponseWriter, flusher http.Flusher, event string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return WriteRawSSE(w, flusher, event, string(raw))
}

// WriteRawSSE writes one named SSE frame with an already serialised payload.
func WriteRawSSE(w http.ResponseWriter, flusher http.Flusher, event, data string) error {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.WriteString(oneLine(data))
	b.WriteString("\n\n")
	if _, err := writeString(w, b.String()); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// Comment writes an SSE comment frame, used as a keep-alive probe.
func Comment(w http.ResponseWriter, flusher http.Flusher, text string) error {
	if _, err := writeString(w, ": "+oneLine(text)+"\n\n"); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// KeepAlive emits a comment frame every interval until the context is done.
func KeepAlive(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := Comment(w, flusher, "keep-alive"); err != nil {
				if !errors.Is(err, http.ErrBodyNotAllowed) {
					slog.Debug("httputil: keep-alive write failed", "error", err)
				}
				return
			}
		}
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\\n")
}

func writeString(w http.ResponseWriter, s string) (int, error) {
	return w.Write([]byte(s))
}
