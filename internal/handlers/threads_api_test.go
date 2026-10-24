package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
	"agenthub/internal/threads"
)

// memThreads is an in-memory threads.Store used to exercise the collaboration
// handlers without a database. It mirrors the ownership and upsert semantics of
// the GORM implementation so the handler logic (not the persistence) is what is
// under test.
type memThreads struct {
	mu         sync.Mutex
	threads    map[string]*threads.Thread
	messages   map[string][]*threads.Message
	seq        int64
	failUpsert bool
}

func newMemThreads() *memThreads {
	return &memThreads{
		threads:  map[string]*threads.Thread{},
		messages: map[string][]*threads.Message{},
	}
}

func (m *memThreads) CreateThread(_ context.Context, t *threads.Thread) (*threads.Thread, error) {
	if t.OwnerID == "" {
		return nil, errors.New("owner is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	nt := *t
	nt.ID = "thread-" + strconv.FormatInt(m.seq, 10)
	if nt.Title == "" {
		nt.Title = "New thread"
	}
	m.threads[nt.ID] = &nt
	m.messages[nt.ID] = nil
	return &nt, nil
}

func (m *memThreads) GetThread(_ context.Context, id, ownerID string) (*threads.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.threads[id]
	if !ok || t.OwnerID != ownerID {
		return nil, threads.ErrNotFound
	}
	return t, nil
}

func (m *memThreads) ListThreads(_ context.Context, ownerID string) ([]*threads.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*threads.Thread, 0, len(m.threads))
	for _, t := range m.threads {
		if t.OwnerID == ownerID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *memThreads) SetParticipants(_ context.Context, threadID string, resourceIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.threads[threadID]
	if !ok {
		return threads.ErrNotFound
	}
	t.Participants = resourceIDs
	return nil
}

func (m *memThreads) AddMessage(_ context.Context, msg *threads.Message) (*threads.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	nm := *msg
	nm.ID = strconv.FormatInt(m.seq, 10)
	m.messages[msg.ThreadID] = append(m.messages[msg.ThreadID], &nm)
	return &nm, nil
}

func (m *memThreads) ListMessages(_ context.Context, threadID string, sinceSeq int64) ([]*threads.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*threads.Message, 0)
	for _, msg := range m.messages[threadID] {
		id, _ := strconv.ParseInt(msg.ID, 10, 64)
		if sinceSeq > 0 && id <= sinceSeq {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

func (m *memThreads) UpsertAgentMessage(_ context.Context, threadID, taskID, resourceID, body string) (*threads.Message, error) {
	if taskID == "" {
		return nil, errors.New("task id is required")
	}
	if m.failUpsert {
		return nil, errors.New("upsert failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.messages[threadID] {
		if msg.TaskID == taskID {
			if body != "" {
				msg.Body = body
			}
			return msg, nil
		}
	}
	m.seq++
	nm := &threads.Message{
		ID:         strconv.FormatInt(m.seq, 10),
		ThreadID:   threadID,
		AuthorType: "agent",
		AuthorID:   resourceID,
		AuthorName: resourceID,
		Body:       body,
		TaskID:     taskID,
		ResourceID: resourceID,
	}
	m.messages[threadID] = append(m.messages[threadID], nm)
	return nm, nil
}

// ---------------------------------------------------------------------------
// workspace
// ---------------------------------------------------------------------------

func TestWorkspaceAggregates(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "a1"}
	d := testDeps(t, openConfig(""), agent)
	d.Hub.Store().Create(agent.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/workspace", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Agents   []*registry.Entry `json:"agents"`
		Activity []struct {
			ID         string `json:"id"`
			ResourceID string `json:"resourceId"`
			State      string `json:"state"`
		} `json:"activity"`
	}
	decodeJSON(t, w, &body)
	if len(body.Agents) != 1 || body.Agents[0].ID != agent.ID {
		t.Fatalf("agents = %+v", body.Agents)
	}
	if len(body.Activity) != 1 {
		t.Fatalf("activity = %+v", body.Activity)
	}

	// The workspace handler itself rejects anything but GET before doing work.
	if w := do(t, h, http.MethodPost, "/api/workspace", "", nil); w.Code == http.StatusOK {
		t.Fatalf("post should not be served by the workspace route")
	}
	direct := httptest.NewRecorder()
	d.handleWorkspace(direct, httptest.NewRequest(http.MethodPost, "/api/workspace", nil))
	if direct.Code != http.StatusMethodNotAllowed {
		t.Fatalf("handler post status = %d, want 405", direct.Code)
	}
}

// ---------------------------------------------------------------------------
// threads: list / create / get
// ---------------------------------------------------------------------------

func TestListThreadsScopedToOwner(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	mt.CreateThread(context.Background(), &threads.Thread{Title: "mine", OwnerID: "dev"})
	mt.CreateThread(context.Background(), &threads.Thread{Title: "theirs", OwnerID: "someone-else"})
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/threads", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct {
		Threads []*threads.Thread `json:"threads"`
	}
	decodeJSON(t, w, &body)
	if len(body.Threads) != 1 || body.Threads[0].Title != "mine" {
		t.Fatalf("threads = %+v", body.Threads)
	}
}

func TestCreateThreadSetsOwnerAndParticipants(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads", `{"title":"My thread","participants":["agent-1","agent-2"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var created threads.Thread
	decodeJSON(t, w, &created)
	if created.OwnerID != "dev" {
		t.Fatalf("owner = %q, want dev", created.OwnerID)
	}
	if created.Title != "My thread" || len(created.Participants) != 2 {
		t.Fatalf("created = %+v", created)
	}

	// No title falls back to the default.
	w = do(t, h, http.MethodPost, "/api/threads", `{}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("default-title status = %d (%s)", w.Code, w.Body.String())
	}
	var def threads.Thread
	decodeJSON(t, w, &def)
	if def.Title != "New thread" {
		t.Fatalf("default title = %q", def.Title)
	}
}

func TestGetThread(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	mine, _ := mt.CreateThread(context.Background(), &threads.Thread{Title: "t", OwnerID: "dev"})
	mt.AddMessage(context.Background(), &threads.Message{ThreadID: mine.ID, AuthorType: "user", AuthorID: "dev", Body: "hi"})
	theirs, _ := mt.CreateThread(context.Background(), &threads.Thread{Title: "other", OwnerID: "someone-else"})
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/threads/"+mine.ID, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Thread   threads.Thread     `json:"thread"`
		Messages []*threads.Message `json:"messages"`
	}
	decodeJSON(t, w, &body)
	if body.Thread.ID != mine.ID || len(body.Messages) != 1 {
		t.Fatalf("body = %+v", body)
	}

	// A thread owned by someone else is indistinguishable from a missing one.
	if w := do(t, h, http.MethodGet, "/api/threads/"+theirs.ID, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("other-owner status = %d, want 404", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/threads/nope", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", w.Code)
	}
}

// ---------------------------------------------------------------------------
// threads: post message + delegation
// ---------------------------------------------------------------------------

func TestPostThreadMessageEmptyBody(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages", `{"body":"   "} `, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &body)
	if body.Code != "empty_message" {
		t.Fatalf("code = %q, want empty_message", body.Code)
	}
}

func TestPostThreadMessageMissingThread(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/nope/messages", `{"body":"hi"}`, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPostThreadMessageDelegates(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "a1"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), agent)
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"hello agent","mentions":["`+agent.ID+`"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Message   threads.Message `json:"message"`
		Delegated []string        `json:"delegated"`
		Failed    []string        `json:"failed"`
	}
	decodeJSON(t, w, &body)
	if body.Message.Body != "hello agent" {
		t.Fatalf("message = %+v", body.Message)
	}
	if len(body.Delegated) != 1 {
		t.Fatalf("delegated = %+v, want one task id", body.Delegated)
	}
	if len(body.Failed) != 0 {
		t.Fatalf("failed = %+v, want empty", body.Failed)
	}
}

func TestPostThreadMessageDelegationFailure(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig("")) // empty registry: no resource to delegate to
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"hi","mentions":["no-such-resource"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Delegated []string `json:"delegated"`
		Failed    []string `json:"failed"`
	}
	decodeJSON(t, w, &body)
	if len(body.Delegated) != 0 {
		t.Fatalf("delegated = %+v, want empty", body.Delegated)
	}
	if len(body.Failed) != 1 || body.Failed[0] != "no-such-resource" {
		t.Fatalf("failed = %+v, want [no-such-resource]", body.Failed)
	}
}

func TestPostThreadMessageUpsertFailureIsNonFatal(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "a1"}
	mt := newMemThreads()
	mt.failUpsert = true // simulate a persistence hiccup on the agent-message write
	d := testDeps(t, openConfig(""), agent)
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	// The task still runs (Hub.Send succeeds); only persisting the placeholder
	// agent message fails. The handler must degrade gracefully: it must not turn
	// that into a client error, nor brand it a failed delegation — the reply still
	// arrives later over the SSE stream.
	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"hello","mentions":["`+agent.ID+`"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Delegated []string `json:"delegated"`
		Failed    []string `json:"failed"`
	}
	decodeJSON(t, w, &body)
	if len(body.Failed) != 0 {
		t.Fatalf("failed = %+v, a storage hiccup must not be a delegation failure", body.Failed)
	}
	if len(body.Delegated) != 0 {
		t.Fatalf("delegated = %+v, the unpersisted placeholder is intentionally not tracked", body.Delegated)
	}
}

// ---------------------------------------------------------------------------
// threads: SSE event stream
// ---------------------------------------------------------------------------

func TestThreadEventStreamReplaysAndFollows(t *testing.T) {
	mt := newMemThreads()
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	mt.AddMessage(context.Background(), &threads.Message{ThreadID: th.ID, AuthorType: "user", AuthorID: "dev", Body: "hi"})
	// An existing agent message keyed by a task id the stream will follow.
	mt.AddMessage(context.Background(), &threads.Message{
		ThreadID: th.ID, AuthorType: "agent", AuthorID: "agent-x", ResourceID: "agent-x", TaskID: "task-live", Body: "",
	})
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	h := Register(d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+th.ID+"/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()

	// Once the handler has subscribed, publish a delegated reply so the live
	// follow branch (merged broker channel -> applyTaskEvent -> SSE) runs.
	time.Sleep(50 * time.Millisecond)
	live := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("live reply"))
	d.Hub.Broker().Publish(hub.Event{TaskID: "task-live", Kind: hub.EventKindMessage, Message: &live})
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: thread.message") {
		t.Fatalf("history not streamed: %q", body)
	}
	if !strings.Contains(body, "event: thread.agent") {
		t.Fatalf("live agent reply not streamed: %q", body)
	}
}

func TestThreadEventStreamMissingThread(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/threads/nope/events", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// ---------------------------------------------------------------------------
// unit: applyTaskEvent + subscribeMerged
// ---------------------------------------------------------------------------

func TestApplyTaskEvent(t *testing.T) {
	mt := newMemThreads()
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	d := Deps{Threads: mt}

	// Message frame with a body -> persisted agent reply.
	msgBody := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("hello"))
	msgEv := hub.Event{Kind: hub.EventKindMessage, TaskID: "task-1", Message: &msgBody}
	if m := d.applyTaskEvent(context.Background(), th.ID, msgEv, map[string]string{"task-1": "agent-1"}); m == nil || m.Body != "hello" || m.ResourceID != "agent-1" {
		t.Fatalf("message event = %+v", m)
	}

	// Empty body -> nothing to show.
	emptyBody := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart(""))
	emptyEv := hub.Event{Kind: hub.EventKindMessage, TaskID: "task-2", Message: &emptyBody}
	if got := d.applyTaskEvent(context.Background(), th.ID, emptyEv, nil); got != nil {
		t.Fatalf("empty body should be nil, got %+v", got)
	}

	// Terminal status -> agent message recorded (body stays empty).
	termEv := hub.Event{Kind: hub.EventKindStatus, TaskID: "task-3", Status: &a2a.TaskStatusUpdateEvent{Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}}
	if st := d.applyTaskEvent(context.Background(), th.ID, termEv, map[string]string{"task-3": "agent-3"}); st == nil || st.Body != "" {
		t.Fatalf("terminal status = %+v", st)
	}

	// Non-terminal status -> nothing to show yet.
	workEv := hub.Event{Kind: hub.EventKindStatus, TaskID: "task-4", Status: &a2a.TaskStatusUpdateEvent{Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}}
	if got := d.applyTaskEvent(context.Background(), th.ID, workEv, nil); got != nil {
		t.Fatalf("non-terminal status should be nil, got %+v", got)
	}
}

func TestSubscribeMerged(t *testing.T) {
	reg := registry.New()
	h := hub.New(openConfig(""), reg,
		adapter.Options{CLITimeout: time.Second, CLIAllowlist: []string{"echo"}, MaxOutputBytes: 1024},
		&hub.MockNotifier{}, hub.Persistence{})
	b := h.Broker()
	ch, cancel := subscribeMerged(b, []string{"t1"})
	defer cancel()

	b.Publish(hub.Event{TaskID: "t1", Kind: hub.EventKindMessage})
	select {
	case ev := <-ch:
		if ev.TaskID != "t1" || ev.Kind != hub.EventKindMessage {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered through the merged channel")
	}
}
