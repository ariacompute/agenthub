package hub

// Branches that the task-level suite never reaches, collected here rather than
// scattered through the behavioural tests: the owner-attributed entry points, the
// resource resolution the A2A handler authorises against, the error translation
// helpers, and the real HTTP notifier (signing, dedupe, recording, refusal).
//
// The last group is the one that used to be unreachable: Notify was 8% covered
// because the natural test — post to httptest — is refused by the SSRF guard. The
// guard checks the URL scheme before dialling and the address at dial time, so a
// TLS test server passes the first and fails the second; swapping n.client for
// srv.Client() is the seam that lets the delivery path itself be asserted without
// weakening netutil.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// ---------------------------------------------------------------------------
// owner attributed entry points
// ---------------------------------------------------------------------------

func TestSendAsAndStreamAsRunLikeTheirAnonymousForms(t *testing.T) {
	// The wrappers exist so the router's tasks carry an owner into the execution
	// plane. A bug in the wiring would make them silently behave like Send/Stream,
	// so the assertion is that a task is created, advanced and completed with the
	// same history — plus, for the streaming form, that frames arrive.
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "owned-skill", Version: "1.0.0", Capabilities: []string{"search"}}
	h, _, _ := newTestHub(t, skill)
	defer h.Close()

	params := sendParams("run it", map[string]any{"resourceId": skill.ID})
	task, err := h.SendAs(context.Background(), "owner-9", params)
	if err != nil {
		t.Fatalf("SendAs: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("SendAs state = %s, want completed", task.Status.State)
	}
	if len(task.History) != 2 || task.History[1].Role != a2a.RoleAgent {
		t.Fatalf("SendAs history = %+v, want user + agent", task.History)
	}

	var mu sync.Mutex
	var frames []string
	emit := func(event string, _ any) error {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, event)
		return nil
	}
	streamed, err := h.StreamAs(context.Background(), "owner-9", params, emit)
	if err != nil {
		t.Fatalf("StreamAs: %v", err)
	}
	if streamed.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("StreamAs state = %s, want completed", streamed.Status.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(frames) == 0 {
		t.Fatal("StreamAs emitted no frames")
	}
}

func TestScopeOfPrefersTheTicketThenTheContext(t *testing.T) {
	// The router passes its ticket as metadata; a plain client passes a context id.
	// Falling through to "default" is what keeps the two from colliding.
	// The ticket travels on the message (the router's own metadata), not on the
	// request envelope; a plain client only has a context id.
	withTicket := func(ticket any, contextID string) a2a.MessageSendParams {
		msg := a2a.Message{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.NewTextPart("x")}, ContextID: contextID}
		msg.Metadata = map[string]any{"ticket": ticket}
		return a2a.MessageSendParams{Message: msg}
	}
	cases := []struct {
		name   string
		params a2a.MessageSendParams
		want   string
	}{
		{"ticket wins", withTicket("  T-1  ", ""), "T-1"},
		{"context id when there is no ticket", a2a.MessageSendParams{Message: a2a.Message{ContextID: " ctx "}}, "ctx"},
		{"ticket beats the context id", withTicket("T-2", "ctx"), "T-2"},
		{"neither", sendParams("x", nil), "default"},
		{"blank ticket falls through", withTicket("   ", ""), "default"},
		{"non string ticket falls through", withTicket(7, ""), "default"},
	}
	for _, tc := range cases {
		if got := scopeOf(tc.params); got != tc.want {
			t.Fatalf("%s: scopeOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// resource resolution
// ---------------------------------------------------------------------------

func TestResolveResourceFollowsTheSameRulesAsExecution(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "manual", Version: "1.0.0"}
	h, _, _ := newTestHub(t, agent, skill)
	defer h.Close()

	// An unnamed request lands on the agent: that is the documented preference and
	// the path an anonymous caller used to abuse through the kind fallback.
	got, err := h.ResolveResource(sendParams("hi", nil))
	if err != nil {
		t.Fatalf("resolve unnamed: %v", err)
	}
	if got.ID != agent.ID {
		t.Fatalf("resolve unnamed = %q, want the agent %q", got.ID, agent.ID)
	}

	for _, key := range []string{"resourceId", "resource_id", "resource"} {
		got, err = h.ResolveResource(sendParams("hi", map[string]any{key: skill.ID}))
		if err != nil {
			t.Fatalf("resolve by %s: %v", key, err)
		}
		if got.ID != skill.ID {
			t.Fatalf("resolve by %s = %q, want %q", key, got.ID, skill.ID)
		}
	}

	// A selector that matches nothing falls back to the kind hint, which is how a
	// caller says "the skill, whichever one you have".
	got, err = h.ResolveResource(sendParams("hi", map[string]any{"resourceId": "gone", "kind": "skill"}))
	if err != nil {
		t.Fatalf("resolve by kind hint: %v", err)
	}
	if got.ID != skill.ID {
		t.Fatalf("resolve by kind hint = %q, want %q", got.ID, skill.ID)
	}

	// Names resolve case insensitively, like selectBy.
	if _, err := h.ResolveResource(sendParams("hi", map[string]any{"resourceId": "MANUAL"})); err != nil {
		t.Fatalf("resolve by name: %v", err)
	}

	// Nothing to resolve at all: the caller gets not-found, not a silent default.
	empty, _, _ := newTestHub(t)
	defer empty.Close()
	if _, err := empty.ResolveResource(sendParams("hi", nil)); err == nil {
		t.Fatal("an empty registry must not resolve a resource")
	} else if rpc, ok := err.(*a2a.Error); !ok || rpc.Code != a2a.CodeResourceNotFound {
		t.Fatalf("empty registry error = %v, want resource not found", err)
	}

	// A hub without a registry at all: the router keeps working, the selector does
	// not, and the answer must be the same not-found rather than a panic.
	bare := New(testConfig(), nil, testOptions(), &MockNotifier{}, Persistence{})
	defer bare.Close()
	if _, err := bare.ResolveResource(sendParams("hi", nil)); err == nil {
		t.Fatal("a hub without a registry must not resolve a resource")
	}
}

func TestLastUserTextFindsTheNewestUserTurn(t *testing.T) {
	task := &a2a.Task{History: []a2a.Message{
		userMessage("first"),
		a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("answer")),
		userMessage("second"),
		a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("final")),
	}}
	if got := lastUserText(task); got != "second" {
		t.Fatalf("lastUserText = %q, want the newest user turn", got)
	}
	if got := lastUserText(&a2a.Task{History: []a2a.Message{
		a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("only agent")),
	}}); got != "" {
		t.Fatalf("lastUserText without a user turn = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// error translation
// ---------------------------------------------------------------------------

func TestTransitionErrorDistinguishesNotFoundFromAnIllegalMove(t *testing.T) {
	notFound := transitionError(&httputil.Error{Code: CodeTaskNotFound})
	if notFound.Code != a2a.CodeTaskNotFound {
		t.Fatalf("a missing task = %v, want task not found", notFound.Code)
	}
	conflict := transitionError(errors.New("stub: state conflict"))
	if conflict.Code != a2a.CodeInvalidStateTransition {
		t.Fatalf("an illegal move = %v, want invalid state transition", conflict.Code)
	}
	// The sentinel the repository returns is not an httputil.Error, so it takes the
	// conflict branch; that is deliberate — only the store knows the code.
	if err := transitionError(ErrTaskNotFound); err.Code != a2a.CodeInvalidStateTransition {
		t.Fatalf("sentinel = %v, want the conflict branch", err.Code)
	}
	if !isNotFound(&httputil.Error{Code: CodeTaskNotFound}) {
		t.Fatal("isNotFound must accept the not-found code")
	}
	if isNotFound(errors.New("boom")) || isNotFound(nil) {
		t.Fatal("isNotFound must reject everything else")
	}
}

// failingEventRepo answers every read with an error: the replay endpoint degrades
// to "no history" instead of failing a request.
type failingEventRepo struct{}

func (failingEventRepo) Append(context.Context, Event) error { return nil }
func (failingEventRepo) Since(context.Context, string, uint64, int) ([]Event, error) {
	return nil, errors.New("stub: since failed")
}
func (failingEventRepo) MaxSeq(context.Context) (uint64, error) { return 0, nil }

func TestEventsSwallowsARepositoryFailure(t *testing.T) {
	h := New(testConfig(), nil, testOptions(), &MockNotifier{}, Persistence{Events: failingEventRepo{}})
	defer h.Close()
	if got := h.Events(context.Background(), "task-1", 0, 10); got != nil {
		t.Fatalf("Events on a failing repository = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// push notification early returns
// ---------------------------------------------------------------------------

func TestNotifyPushReturnsEarlyWithoutWorkToDo(t *testing.T) {
	// Four conditions guard the call: a task, push enabled, a pusher, and a
	// registered callback. Any of them missing must be a no-op rather than a panic
	// on a nil receiver or a stray egress request.
	notifier := &MockNotifier{}
	task := &a2a.Task{ID: "task-1", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}

	h := New(testConfig(), nil, testOptions(), notifier, Persistence{})
	h.cfg.PushEnabled = false
	h.notifyPush(task)
	h.Close()

	h2 := New(testConfig(), nil, testOptions(), nil, Persistence{})
	h2.notifyPush(task) // no pusher
	h2.Close()

	h3 := New(testConfig(), nil, testOptions(), notifier, Persistence{})
	h3.notifyPush(nil)  // no task
	h3.notifyPush(task) // no registered callback
	h3.Close()
}

// failingReads is a repository whose read methods fail: the store must degrade to
// nil instead of surfacing an error the control plane cannot act on.
type failingReads struct{ *stubTaskRepo }

func (f failingReads) Pending(context.Context, string) (*PendingInput, error) {
	return nil, errors.New("stub: pending failed")
}

func (f failingReads) Push(context.Context, string) (*a2a.PushNotificationConfig, error) {
	return nil, errors.New("stub: push failed")
}

func (f failingReads) Spans(context.Context, string) ([]Span, error) {
	return nil, errors.New("stub: spans failed")
}

func TestTaskStoreReadsSurviveARepositoryFailure(t *testing.T) {
	store := NewTaskStoreWithRepository(failingReads{newStubTaskRepo()})
	if got := store.Pending("task-1"); got != nil {
		t.Fatalf("Pending = %+v, want nil", got)
	}
	if got := store.Push("task-1"); got != nil {
		t.Fatalf("Push = %+v, want nil", got)
	}
	if got := store.Spans(context.Background(), "task-1"); got != nil {
		t.Fatalf("Spans = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// the real notifier
// ---------------------------------------------------------------------------

type capturedPush struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// pushServer is a TLS stub for the callback: it records what arrived so the test
// can verify the signature against the same bytes the receiver saw.
type pushServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedPush
	status   int
}

func newPushServer(t *testing.T, status int) *pushServer {
	t.Helper()
	s := &pushServer{status: status}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, capturedPush{
			method: r.Method,
			path:   r.URL.Path,
			header: r.Header.Clone(),
			body:   body,
		})
		s.mu.Unlock()
		w.WriteHeader(s.status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *pushServer) captured() []capturedPush {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedPush(nil), s.requests...)
}

func TestHTTPNotifierDeliversSignsAndDeduplicates(t *testing.T) {
	srv := newPushServer(t, http.StatusNoContent)
	repo := &stubPushRepo{}
	n := NewHTTPNotifier(repo, "s3cret")
	// The SSRF guard blocks loopback at dial time; a test server is loopback, so
	// the client is swapped for the one that trusts the test certificate.
	n.client = srv.Client()

	cfg := a2a.PushNotificationConfig{URL: srv.URL + "/hook", Token: "peer-token"}
	if err := n.Notify(context.Background(), cfg, "task-1", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	sent := srv.captured()
	if len(sent) != 1 {
		t.Fatalf("peer saw %d requests, want 1", len(sent))
	}
	first := sent[0]
	if first.method != http.MethodPost || first.path != "/hook" {
		t.Fatalf("request = %s %s, want POST /hook", first.method, first.path)
	}
	if got := first.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := first.header.Get("Authorization"); got != "Bearer peer-token" {
		t.Fatalf("Authorization = %q, want the callback's own token", got)
	}

	// The receiver must be able to verify the signature from what it received.
	ts := first.header.Get("X-Agenthub-Timestamp")
	nonce := first.header.Get("X-Agenthub-Nonce")
	if ts == "" || nonce == "" {
		t.Fatalf("missing signature headers: %v", first.header)
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q: %v", ts, err)
	}
	if want := sign("s3cret", unix, nonce, first.body); first.header.Get("X-Agenthub-Signature") != want {
		t.Fatal("the signature does not cover the timestamp, nonce and body")
	}

	var payload pushPayload
	if err := json.Unmarshal(first.body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload.TaskID != "task-1" || payload.State != string(a2a.TaskStateCompleted) {
		t.Fatalf("payload = %+v", payload)
	}

	// The same (task, state) pair is delivered once per TTL.
	if err := n.Notify(context.Background(), cfg, "task-1", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("second Notify: %v", err)
	}
	if got := len(srv.captured()); got != 1 {
		t.Fatalf("a duplicate delivery reached the peer %d times", got)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("recorded %d callbacks, want 1", len(repo.rows))
	}

	// A different state is a different delivery.
	if err := n.Notify(context.Background(), cfg, "task-1", a2a.TaskStateFailed); err != nil {
		t.Fatalf("third Notify: %v", err)
	}
	if got := len(srv.captured()); got != 2 {
		t.Fatalf("a new state did not reach the peer, %d requests", got)
	}
}

func TestHTTPNotifierRecordsFailuresAndPeerRejections(t *testing.T) {
	// A peer that answers 5xx is a rejection, not an error: the task is already in
	// a terminal state and the callback must not change it.
	rejecting := newPushServer(t, http.StatusInternalServerError)
	repo := &stubPushRepo{}
	n := NewHTTPNotifier(repo, "")
	n.client = rejecting.Client()
	if err := n.Notify(context.Background(), a2a.PushNotificationConfig{URL: rejecting.URL}, "task-2", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify against a rejecting peer: %v", err)
	}
	if len(rejecting.captured()) != 1 || len(repo.rows) != 1 {
		t.Fatalf("rejection: requests=%d records=%d", len(rejecting.captured()), len(repo.rows))
	}

	// A transport failure is also swallowed, but the intent is still recorded.
	broken := NewHTTPNotifier(repo, "")
	broken.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	if err := broken.Notify(context.Background(), a2a.PushNotificationConfig{URL: "https://peer.example/hook"}, "task-3", a2a.TaskStateFailed); err != nil {
		t.Fatalf("Notify over a broken transport: %v", err)
	}
	if len(repo.rows) != 2 {
		t.Fatalf("a failed delivery was not recorded: %d records", len(repo.rows))
	}
}

func TestHTTPNotifierRefusesAnInsecureDestination(t *testing.T) {
	// http:// is refused before any dial: a caller supplied callback is never
	// allowed to be plaintext, whatever the host is.
	n := NewHTTPNotifier(&stubPushRepo{}, "")
	called := false
	n.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not be reached")
	})}
	if err := n.Notify(context.Background(), a2a.PushNotificationConfig{URL: "http://peer.example/hook"}, "task-4", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify must return nil for a refused destination: %v", err)
	}
	if called {
		t.Fatal("a refused destination was dialled")
	}
}

func TestResetAllowsTheSameDeliveryAgain(t *testing.T) {
	srv := newPushServer(t, http.StatusNoContent)
	n := NewHTTPNotifier(nil, "")
	n.client = srv.Client()
	cfg := a2a.PushNotificationConfig{URL: srv.URL}

	if err := n.Notify(context.Background(), cfg, "task-5", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	n.Reset()
	if err := n.Notify(context.Background(), cfg, "task-5", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify after Reset: %v", err)
	}
	if got := len(srv.captured()); got != 2 {
		t.Fatalf("Reset did not clear the idempotency cache: %d requests", got)
	}
}

func TestNewNonceIsRandomHex(t *testing.T) {
	first, second := newNonce(), newNonce()
	if first == second {
		t.Fatal("two nonces must differ, or a replay would look fresh")
	}
	if len(first) != 32 {
		t.Fatalf("nonce = %q, want 16 bytes hex encoded", first)
	}
	for _, r := range first {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("nonce %q is not hex", first)
		}
	}
}

func TestMarkSeenSweepsTheExpiredEntries(t *testing.T) {
	// The cache is swept only when it grows past 1024, which is why this is tested
	// directly: a leak here would grow unbounded on a long-lived instance.
	n := NewHTTPNotifier(nil, "")
	past := time.Now().Add(-time.Hour)
	for i := 0; i < 1100; i++ {
		n.seen[strconv.Itoa(i)] = past
	}
	if !n.markSeen("fresh") {
		t.Fatal("a first sight must be reported as fresh")
	}
	if got := len(n.seen); got > 1100 {
		t.Fatalf("expired entries were not swept: %d remain", got)
	}
	if n.markSeen("fresh") {
		t.Fatal("a repeat within the TTL must be reported as seen")
	}
}

// roundTripFunc adapts a function to http.RoundTripper, so a test can fail a
// request without opening a socket.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
