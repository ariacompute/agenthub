// Package audit records every control plane and data plane call.
//
// The shape follows CloudTrail, because the questions an operator asks about an
// agent hub are the same ones they ask about a cloud account: who called what,
// from where, with which identity, and did it succeed. Field names map one to
// one (eventID / eventTime / eventSource / eventName / readOnly / userIdentity /
// sourceIPAddress / errorCode), so the vocabulary carries over.
//
// Two properties matter more than the field list:
//
//   - Recording never blocks or fails a request. Accepted events go through a
//     bounded queue; a full queue drops the frame and counts it.
//   - The trail is tamper evident. Each row carries prev_hash and hash, so
//     editing or removing one row breaks every later link and shows up in
//     /api/audit/verify.
//
// This file holds the event, the mutable trace that travels on the request
// context, and the HTTP middleware. The chain lives in chain.go and the single
// writer in recorder.go.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sources of an event.
const (
	SourceAPI  = "api"
	SourceA2A  = "a2a"
	SourceAuth = "auth"
)

// Actor kinds.
const (
	ActorAnonymous = "anonymous"
	ActorUser      = "user"
	ActorAPIKey    = "apikey"
)

// Event is one recorded call.
type Event struct {
	// Seq is the storage sequence. It is presentational and cursor material
	// only: it is not part of the hash, because a database-assigned number is
	// not reproducible from the row content.
	Seq          int64          `json:"seq,omitempty"`
	EventID      string         `json:"eventId"`
	Time         time.Time      `json:"eventTime"`
	RequestID    string         `json:"requestId"`
	Source       string         `json:"eventSource"`
	ActorKind    string         `json:"actorKind"`
	ActorID      string         `json:"actorId"`
	ActorLabel   string         `json:"actorLabel,omitempty"`
	OwnerID      string         `json:"ownerId,omitempty"`
	IP           string         `json:"sourceIPAddress"`
	Method       string         `json:"httpMethod"`
	Path         string         `json:"path"`
	Action       string         `json:"eventName"`
	ResourceID   string         `json:"resourceId,omitempty"`
	ResourceKind string         `json:"resourceKind,omitempty"`
	Status       int            `json:"status"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	ReadOnly     bool           `json:"readOnly"`
	LatencyMS    int            `json:"latencyMs"`
	UserAgent    string         `json:"userAgent,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	PrevHash     string         `json:"prevHash,omitempty"`
	Hash         string         `json:"hash,omitempty"`
}

// Query narrows a listing.
type Query struct {
	From       time.Time
	To         time.Time
	ActorID    string
	Action     string
	ResourceID string
	Source     string
	// Outcome filters by result: "ok", "denied", "failed". Empty means all.
	Outcome string
	// Search is a substring match on the path, the action or the request id.
	Search string
	// Before is the cursor: return rows with id < Before, newest first.
	Before int64
	Limit  int
}

// Stats is the one line summary the console shows above the table.
type Stats struct {
	Total   int64 `json:"total"`
	Failed  int64 `json:"failed"`
	Denied  int64 `json:"denied"`
	Written int64 `json:"written"`
}

// Store is the persistence port. It is implemented in internal/store so this
// package stays free of GORM, and it is nil-safe in tests: with no store the
// recorder keeps the chain in memory only.
type Store interface {
	Append(ctx context.Context, events []Event) error
	// LastHash returns the hash of the newest row ("" when the table is empty)
	// and its id, which is what lets a restart continue the chain instead of
	// starting a second one.
	LastHash(ctx context.Context) (hash string, id int64, err error)
	List(ctx context.Context, q Query) ([]Event, error)
	Get(ctx context.Context, eventID string) (*Event, error)
	Stats(ctx context.Context, q Query) (Stats, error)
	// Segment returns rows ordered by id ascending in [from, id <= to], at most
	// limit per page, for the chain verifier.
	Segment(ctx context.Context, from time.Time, afterID int64, limit int) ([]Event, error)
	// Prune deletes rows older than cutoff. It is the only delete the policies
	// accept, and it runs under the retention identity.
	Prune(ctx context.Context, cutoff time.Time) (int64, error)
}

// ---------------------------------------------------------------------------
// Trace: the mutable per-request record
// ---------------------------------------------------------------------------

// Trace is the event being built while the request runs. Handlers annotate it
// (principal, A2A method, resource, error code) and the middleware flushes it.
type Trace struct {
	mu      sync.Mutex
	event   Event
	started time.Time
	notes   map[string]any
}

type traceKey struct{}

// StartTrace attaches a fresh trace to ctx.
func StartTrace(ctx context.Context, ev Event) (context.Context, *Trace) {
	tr := &Trace{event: ev, started: time.Now()}
	return context.WithValue(ctx, traceKey{}, tr), tr
}

// FromContext returns the trace of the current request, or nil.
func FromContext(ctx context.Context) *Trace {
	if ctx == nil {
		return nil
	}
	tr, _ := ctx.Value(traceKey{}).(*Trace)
	return tr
}

// note applies fn under the trace lock, ignoring a nil trace so every helper is
// safe to call from code that also runs outside a request.
func (t *Trace) note(fn func(*Event)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&t.event)
}

// NotePrincipal records who the caller turned out to be. It is called from the
// identity middleware once the session is resolved, which is why the audit
// middleware itself does not have to parse cookies twice.
func (t *Trace) NotePrincipal(kind, id, label, ownerID string) {
	t.note(func(e *Event) {
		e.ActorKind = kind
		e.ActorID = id
		e.ActorLabel = label
		e.OwnerID = ownerID
	})
}

// NoteAction records the logical operation (an A2A method name, for instance).
func (t *Trace) NoteAction(action string) {
	t.note(func(e *Event) { e.Action = action })
}

// NoteResource records the resource the request reached.
func (t *Trace) NoteResource(id, kind string) {
	t.note(func(e *Event) {
		e.ResourceID = id
		e.ResourceKind = kind
	})
}

// NoteError records the machine readable failure code.
func (t *Trace) NoteError(code string) {
	t.note(func(e *Event) { e.ErrorCode = code })
}

// NoteReadOnly marks the call as a read.
func (t *Trace) NoteReadOnly(readOnly bool) {
	t.note(func(e *Event) { e.ReadOnly = readOnly })
}

// NoteMeta attaches a small key/value fact worth keeping (counts, filters).
// Values are strings and numbers only: a nested body would turn the audit table
// into a copy of every payload, which is exactly what it must not be.
func (t *Trace) NoteMeta(key string, value any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.event.Metadata == nil {
		t.event.Metadata = map[string]any{}
	}
	t.event.Metadata[key] = value
}

// Snapshot returns a copy of the event as it stands.
func (t *Trace) Snapshot() Event {
	if t == nil {
		return Event{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.event
	if len(t.event.Metadata) > 0 {
		out.Metadata = make(map[string]any, len(t.event.Metadata))
		for k, v := range t.event.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// Recorder is what the middleware needs; *Recorder implements it.
type Sink interface {
	Record(ev Event)
}

// Tap records every request that passes through it.
//
// It is applied outside the mux so an unknown path, a rejected request and a
// panic are all captured: "who tried what and got refused" is the most useful
// half of an audit trail. The response is buffered only in the sense that the
// status code is observed; the body streams through untouched, which keeps SSE
// working.
func Tap(sink Sink, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sink == nil {
			next.ServeHTTP(w, r)
			return
		}
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = newID()
		}
		w.Header().Set("X-Request-Id", requestID)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		tr := &Trace{
			started: time.Now(),
			event: Event{
				EventID:   newID(),
				Time:      time.Now().UTC(),
				RequestID: requestID,
				Source:    sourceOf(r.URL.Path),
				ActorKind: ActorAnonymous,
				ActorID:   "",
				IP:        clientIP(r),
				Method:    r.Method,
				Path:      redactPath(r.URL),
				Action:    defaultAction(r),
				ReadOnly:  isReadMethod(r.Method),
				UserAgent: truncate(r.UserAgent(), 256),
			},
		}
		ctx := context.WithValue(r.Context(), traceKey{}, tr)
		next.ServeHTTP(rec, r.WithContext(ctx))

		ev := tr.Snapshot()
		ev.Status = rec.status
		ev.LatencyMS = int(time.Since(tr.started).Milliseconds())
		if ev.ErrorCode == "" {
			ev.ErrorCode = codeFromStatus(rec.status)
		}
		if ev.Action == "" {
			ev.Action = defaultAction(r)
		}
		sink.Record(ev)
	})
}

// statusRecorder observes the status code without touching the body.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Flush keeps SSE working through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func sourceOf(path string) string {
	switch {
	case strings.HasPrefix(path, "/a2a"):
		return SourceA2A
	case strings.HasPrefix(path, "/api/auth/"):
		return SourceAuth
	default:
		return SourceAPI
	}
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// defaultAction is the fallback operation name: method plus a path template.
func defaultAction(r *http.Request) string {
	return r.Method + " " + templatePath(r.URL.Path)
}

// templatePath replaces identifier shaped segments with {id} so the action name
// groups a family of calls instead of exploding per row.
func templatePath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "" {
			continue
		}
		if looksLikeID(p) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// looksLikeID reports whether a path segment is an opaque identifier rather than
// a route word. registry.NewID emits lowercase base32-ish ids; UUIDs, numeric
// ids and hex digests all land here too.
func looksLikeID(seg string) bool {
	if len(seg) >= 20 {
		return true
	}
	if _, err := strconv.ParseInt(seg, 10, 64); err == nil {
		return true
	}
	return false
}

// redactPath drops query values: they can carry a token, a filter on a private
// resource, or a search phrase that must not be copied into an audit table. The
// keys are kept so the shape of the call survives.
func redactPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return truncate(u.Path, 512)
	}
	keys := make([]string, 0, 4)
	for k := range u.Query() {
		keys = append(keys, k+"=REDACTED")
	}
	return truncate(u.Path+"?"+strings.Join(keys, "&"), 512)
}

// clientIP mirrors the limiter's resolution: the leftmost X-Forwarded-For hop,
// then the socket address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, found := strings.Cut(xff, ","); found || first != "" {
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func codeFromStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusUnauthorized:
		return "unauthenticated"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "internal_error"
	default:
		return "invalid_request"
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// newID returns a 128 bit hex identifier. crypto/rand is the only source: an
// audit event id must not be guessable, or a caller could probe for rows.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not recoverable in any useful way here; a
		// timestamp keeps the trail flowing and is still unique enough per row.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}
