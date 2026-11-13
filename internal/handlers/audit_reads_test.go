package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenthub/internal/audit"
	"agenthub/internal/httputil"
	"agenthub/internal/identity"
)

// auditStub is the audit store as the read endpoints see it. The chain itself
// is covered in internal/audit; what matters here is that every filter the
// console sends arrives in the query and that every failure has its own status.
type auditStub struct {
	rows     []audit.Event
	stats    audit.Stats
	listErr  error
	statsErr error
	getErr   error
	verifyN  int64
	broken   *audit.Verification
	segErr   error

	lastQuery audit.Query
	lastFrom  time.Time
	lastTo    time.Time
	lastCtx   context.Context
	getCalls  []string
}

func (s *auditStub) Append(context.Context, []audit.Event) error     { return nil }
func (s *auditStub) LastHash(context.Context) (string, int64, error) { return "", 0, nil }

func (s *auditStub) List(ctx context.Context, q audit.Query) ([]audit.Event, error) {
	s.lastQuery, s.lastCtx = q, ctx
	return s.rows, s.listErr
}

func (s *auditStub) Get(_ context.Context, eventID string) (*audit.Event, error) {
	s.getCalls = append(s.getCalls, eventID)
	if s.getErr != nil {
		return nil, s.getErr
	}
	for i := range s.rows {
		if s.rows[i].EventID == eventID {
			return &s.rows[i], nil
		}
	}
	return nil, nil
}

func (s *auditStub) Stats(context.Context, audit.Query) (audit.Stats, error) {
	return s.stats, s.statsErr
}

func (s *auditStub) Segment(_ context.Context, from time.Time, _ int64, _ int) ([]audit.Event, error) {
	s.lastFrom = from
	if s.segErr != nil {
		return nil, s.segErr
	}
	if s.broken != nil {
		return []audit.Event{{Seq: 1, EventID: "broken", Hash: "nope"}}, nil
	}
	return nil, nil
}

func (s *auditStub) Prune(context.Context, time.Time) (int64, error) { return 0, nil }
func (s *auditStub) Anchor(context.Context) (audit.Anchor, error) {
	return audit.Anchor{}, nil
}

// readDeps returns Deps wired to the stub. No auth services: the handlers are
// called directly, and the permission gate is covered separately by
// TestAuditEndpointsRequirePermission.
func readDeps(stub *auditStub) Deps {
	return Deps{Audit: audit.New(stub, audit.Options{})}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// TestHandleListAuditPassesEveryFilterThrough is the console contract: the
// screen sends a window plus whichever narrowing the operator typed, and all of
// it has to reach the query or the list silently shows the wrong rows.
func TestHandleListAuditPassesEveryFilterThrough(t *testing.T) {
	stub := &auditStub{
		rows:  []audit.Event{{Seq: 40, EventID: "e1"}, {Seq: 41, EventID: "e2"}},
		stats: audit.Stats{Total: 2, Denied: 1, Written: 2},
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/audit?from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z"+
			"&actor=u-1&action=tasks%2Fget&resource=res-1&source=a2a&outcome=denied"+
			"&q=nightly&before=41&limit=25", nil)
	rec := httptest.NewRecorder()
	readDeps(stub).handleListAudit(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	q := stub.lastQuery
	if q.From.Format(time.RFC3339) != "2026-11-01T00:00:00Z" || q.To.Format(time.RFC3339) != "2026-11-02T00:00:00Z" {
		t.Fatalf("window = %v..%v", q.From, q.To)
	}
	if q.ActorID != "u-1" || q.Action != "tasks/get" || q.ResourceID != "res-1" {
		t.Fatalf("narrowing lost: %+v", q)
	}
	if q.Source != "a2a" || q.Outcome != "denied" || q.Search != "nightly" {
		t.Fatalf("narrowing lost: %+v", q)
	}
	if q.Before != 41 || q.Limit != 25 {
		t.Fatalf("cursor = %d/%d, want 41/25", q.Before, q.Limit)
	}

	var body struct {
		Events []audit.Event `json:"events"`
		Stats  audit.Stats   `json:"stats"`
		Next   int64         `json:"next"`
	}
	decodeInto(t, rec, &body)
	if len(body.Events) != 2 || body.Stats.Total != 2 || body.Stats.Denied != 1 {
		t.Fatalf("body = %+v", body)
	}
	// The cursor is the last row's seq so the next page continues below it.
	if body.Next != 41 {
		t.Fatalf("next = %d, want the last seq", body.Next)
	}
}

// TestHandleListAuditEmptyWindow: an empty result is not an error, and the
// cursor stays zero so the console knows there is nothing more to fetch.
func TestHandleListAuditEmptyWindow(t *testing.T) {
	stub := &auditStub{stats: audit.Stats{Total: 0, Written: 9}}
	rec := httptest.NewRecorder()
	readDeps(stub).handleListAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Events []audit.Event `json:"events"`
		Next   int64         `json:"next"`
	}
	decodeInto(t, rec, &body)
	if body.Next != 0 || len(body.Events) != 0 {
		t.Fatalf("body = %+v", body)
	}
}

// TestHandleListAuditRejectsUnparseableFilters: each malformed parameter is a
// 400 rather than a silently ignored filter, because an ignored filter is how
// an operator ends up reading a different window than the one on screen.
func TestHandleListAuditRejectsUnparseableFilters(t *testing.T) {
	for _, target := range []string{
		"/api/audit?before=abc",
		"/api/audit?limit=many",
		"/api/audit?from=yesterday",
		"/api/audit?to=tomorrow",
	} {
		stub := &auditStub{}
		rec := httptest.NewRecorder()
		readDeps(stub).handleListAudit(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", target, rec.Code)
		}
		if stub.lastQuery.Limit != 0 || stub.lastQuery.Before != 0 {
			t.Fatalf("%s reached the store: %+v", target, stub.lastQuery)
		}
	}
}

// TestHandleListAuditSurfacesStoreFailures: a database that is down is a 500,
// not an empty trail — an empty trail would look like "nothing happened".
func TestHandleListAuditSurfacesStoreFailures(t *testing.T) {
	stub := &auditStub{listErr: errors.New("connection refused")}
	rec := httptest.NewRecorder()
	readDeps(stub).handleListAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("list failure = %d, want 500", rec.Code)
	}

	// Same for the summary: the table would render without its totals.
	stub = &auditStub{statsErr: errors.New("aggregate failed")}
	rec = httptest.NewRecorder()
	readDeps(stub).handleListAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("stats failure = %d, want 500", rec.Code)
	}
}

// TestAuditReadEndpointsWithoutARecorder pins the unconfigured answer: 503 on
// all three, which tells the console to hide the screen instead of reporting an
// empty trail.
func TestAuditReadEndpointsWithoutARecorder(t *testing.T) {
	d := Deps{}

	cases := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		url  string
	}{
		{"list", d.handleListAudit, "/api/audit"},
		{"get", d.handleGetAudit, "/api/audit/e1"},
		{"verify", d.handleVerifyAudit, "/api/audit/verify"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.call(rec, httptest.NewRequest(http.MethodGet, c.url, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s without a recorder = %d, want 503", c.name, rec.Code)
		}
		var body struct {
			Code string `json:"code"`
		}
		decodeInto(t, rec, &body)
		if body.Code != string(httputil.CodeFeatureUnavailable) {
			t.Fatalf("%s code = %q", c.name, body.Code)
		}
	}
}

// TestHandleGetAudit covers the detail endpoint: one event, a miss, and a
// failing store.
func TestHandleGetAudit(t *testing.T) {
	stub := &auditStub{rows: []audit.Event{{EventID: "e1", Action: "tasks/get"}}}
	req := httptest.NewRequest(http.MethodGet, "/api/audit/e1", nil)
	req.SetPathValue("id", "e1")
	rec := httptest.NewRecorder()
	readDeps(stub).handleGetAudit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ev audit.Event
	decodeInto(t, rec, &ev)
	if ev.EventID != "e1" || ev.Action != "tasks/get" {
		t.Fatalf("event = %+v", ev)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/audit/nope", nil)
	req.SetPathValue("id", "nope")
	rec = httptest.NewRecorder()
	readDeps(stub).handleGetAudit(rec, req)
	// A miss is a 404, not an empty 200: the console shows "that row is gone".
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/audit/e1", nil)
	req.SetPathValue("id", "e1")
	rec = httptest.NewRecorder()
	readDeps(&auditStub{getErr: errors.New("boom")}).handleGetAudit(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure = %d, want 500", rec.Code)
	}
}

// TestHandleVerifyAudit covers the chain check endpoint: a clean window, a
// broken one (the answer must travel through, including where it broke) and a
// window that cannot be parsed.
func TestHandleVerifyAudit(t *testing.T) {
	stub := &auditStub{}
	rec := httptest.NewRecorder()
	readDeps(stub).handleVerifyAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit/verify", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var ok audit.Verification
	decodeInto(t, rec, &ok)
	if !ok.OK {
		t.Fatalf("verification = %+v, want ok", ok)
	}

	broken := &auditStub{broken: &audit.Verification{OK: false, BrokenAt: "broken", Reason: "hash does not match"}}
	rec = httptest.NewRecorder()
	readDeps(broken).handleVerifyAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit/verify?from=2026-11-01T00:00:00Z", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var bad audit.Verification
	decodeInto(t, rec, &bad)
	if bad.OK || bad.BrokenAt != "broken" || bad.Reason == "" {
		t.Fatalf("verification = %+v, want the break reported", bad)
	}

	rec = httptest.NewRecorder()
	readDeps(stub).handleVerifyAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit/verify?from=nope", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad window = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	readDeps(&auditStub{segErr: errors.New("read failed")}).handleVerifyAudit(rec, httptest.NewRequest(http.MethodGet, "/api/audit/verify", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure = %d, want 500", rec.Code)
	}
}

// TestAuditQueryFromDefaultsToTheWholeTrail: no parameters means "everything",
// which is what the verifier needs and what the console sends on first paint.
func TestAuditQueryFromDefaultsToTheWholeTrail(t *testing.T) {
	q, err := auditQueryFrom(httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	if err != nil {
		t.Fatalf("auditQueryFrom: %v", err)
	}
	if !q.From.IsZero() || !q.To.IsZero() || q.Limit != 0 || q.Before != 0 {
		t.Fatalf("query = %+v, want everything", q)
	}
}

// TestAuditReadRunsUnderTheCallersScope: the handler must not widen the row
// scope to system, or every operator would read every tenant's trail.
func TestAuditReadRunsUnderTheCallersScope(t *testing.T) {
	stub := &auditStub{}
	ctx := identity.With(context.Background(), identity.Identity{OwnerID: "u-1", AuditRead: true})
	req := httptest.NewRequest(http.MethodGet, "/api/audit", nil).WithContext(ctx)
	readDeps(stub).handleListAudit(httptest.NewRecorder(), req)

	got, _ := identity.From(stub.lastCtx)
	if got.System || got.OwnerID != "u-1" || !got.AuditRead {
		t.Fatalf("store saw %+v, want the caller's own scope", got)
	}
}
