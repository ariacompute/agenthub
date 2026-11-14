package handlers

import (
	"context"
	"encoding/json"
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
	"agenthub/internal/router"
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

// GetThreadUnscoped mirrors the GORM implementation's process-identity read: the
// webhook reaches a bound thread before any session exists.
func (m *memThreads) GetThreadUnscoped(_ context.Context, id string) (*threads.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.threads[id]
	if !ok {
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

func (m *memThreads) DeleteThread(_ context.Context, id, ownerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.threads[id]
	if !ok || t.OwnerID != ownerID {
		return threads.ErrNotFound
	}
	delete(m.threads, id)
	// The GORM store relies on ON DELETE CASCADE; the fake drops the same rows.
	delete(m.messages, id)
	return nil
}

func (m *memThreads) ClearMessages(_ context.Context, threadID, ownerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.threads[threadID]
	if !ok || t.OwnerID != ownerID {
		return threads.ErrNotFound
	}
	// The thread row survives; only the conversation goes away.
	m.messages[threadID] = nil
	return nil
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
	d.Hub.Store().Create(context.Background(), agent.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
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

// The agentbuddy landing view must only list agents the caller may reach: a
// resource still being curated (draft/pending) is not discoverable, so it must
// not appear alongside the approved ones.
func TestWorkspaceHidesNonApproved(t *testing.T) {
	approved := &registry.Entry{ID: "a-ok", Kind: registry.KindAgent, Name: "ok"}
	pending := &registry.Entry{ID: "a-pending", Kind: registry.KindAgent, Name: "pending", Status: registry.StatusPending}
	d := testDeps(t, openConfig(""), approved, pending)
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/workspace", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Agents []*registry.Entry `json:"agents"`
	}
	decodeJSON(t, w, &body)
	if len(body.Agents) != 1 || body.Agents[0].ID != approved.ID {
		t.Fatalf("agents = %+v, want only %s", body.Agents, approved.ID)
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

func TestDeleteThread(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	mine, _ := mt.CreateThread(context.Background(), &threads.Thread{Title: "mine", OwnerID: "dev"})
	mt.AddMessage(context.Background(), &threads.Message{ThreadID: mine.ID, AuthorType: "user", AuthorID: "dev", Body: "hi"})
	theirs, _ := mt.CreateThread(context.Background(), &threads.Thread{Title: "theirs", OwnerID: "someone-else"})
	h := Register(d)

	// Not-owned and missing ids must answer the same 404 and leave the row alone.
	for _, id := range []string{theirs.ID, "nope"} {
		w := do(t, h, http.MethodDelete, "/api/threads/"+id, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("delete %s: status = %d, want 404", id, w.Code)
		}
		if code := errCode(t, w); code != "not_found" {
			t.Fatalf("delete %s: code = %q, want not_found", id, code)
		}
	}
	if _, err := mt.GetThread(context.Background(), theirs.ID, "someone-else"); err != nil {
		t.Fatalf("somebody else's thread was touched: %v", err)
	}

	// The owner's delete is a bodyless 204 and takes the message history with it.
	w := do(t, h, http.MethodDelete, "/api/threads/"+mine.ID, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 carried a body: %q", w.Body.String())
	}
	if _, err := mt.GetThread(context.Background(), mine.ID, "dev"); !errors.Is(err, threads.ErrNotFound) {
		t.Fatalf("thread survived: %v", err)
	}
	if msgs, err := mt.ListMessages(context.Background(), mine.ID, 0); err != nil || len(msgs) != 0 {
		t.Fatalf("message history survived: %v %+v", err, msgs)
	}

	// A retry is still "already gone", which is what makes a batch delete safe.
	if w := do(t, h, http.MethodDelete, "/api/threads/"+mine.ID, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("retry status = %d, want 404", w.Code)
	}
}

func TestClearThreadMessages(t *testing.T) {
	mt := newMemThreads()
	d := testDeps(t, openConfig(""))
	d.Threads = mt
	mine, _ := mt.CreateThread(context.Background(), &threads.Thread{Title: "mine", OwnerID: "dev"})
	mt.AddMessage(context.Background(), &threads.Message{ThreadID: mine.ID, AuthorType: "user", AuthorID: "dev", Body: "hi"})
	mt.AddMessage(context.Background(), &threads.Message{ThreadID: mine.ID, AuthorType: "agent", AuthorID: "a1", Body: "reply"})
	theirs, _ := mt.CreateThread(context.Background(), &threads.Thread{Title: "theirs", OwnerID: "someone-else"})
	mt.AddMessage(context.Background(), &threads.Message{ThreadID: theirs.ID, AuthorType: "user", AuthorID: "someone-else", Body: "keep"})
	h := Register(d)

	// Not-owned and missing ids must answer the same 404 and change no history.
	for _, id := range []string{theirs.ID, "nope"} {
		w := do(t, h, http.MethodDelete, "/api/threads/"+id+"/messages", "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("clear %s: status = %d, want 404", id, w.Code)
		}
		if code := errCode(t, w); code != "not_found" {
			t.Fatalf("clear %s: code = %q, want not_found", id, code)
		}
	}
	if msgs, err := mt.ListMessages(context.Background(), theirs.ID, 0); err != nil || len(msgs) != 1 {
		t.Fatalf("somebody else's messages were touched: %v %+v", err, msgs)
	}
	if msgs, err := mt.ListMessages(context.Background(), mine.ID, 0); err != nil || len(msgs) != 2 {
		t.Fatalf("owner's messages were touched: %v %+v", err, msgs)
	}

	// The owner's clear is a bodyless 204, keeps the thread and empties only its
	// own conversation.
	w := do(t, h, http.MethodDelete, "/api/threads/"+mine.ID+"/messages", "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 carried a body: %q", w.Body.String())
	}
	if _, err := mt.GetThread(context.Background(), mine.ID, "dev"); err != nil {
		t.Fatalf("thread did not survive: %v", err)
	}
	if msgs, err := mt.ListMessages(context.Background(), mine.ID, 0); err != nil || len(msgs) != 0 {
		t.Fatalf("message history survived: %v %+v", err, msgs)
	}
	if msgs, err := mt.ListMessages(context.Background(), theirs.ID, 0); err != nil || len(msgs) != 1 {
		t.Fatalf("another thread's history was cleared: %v %+v", err, msgs)
	}

	// An already empty conversation is a successful no-op, not a 404.
	if w := do(t, h, http.MethodDelete, "/api/threads/"+mine.ID+"/messages", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("retry status = %d, want 204", w.Code)
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

// A mention names the agent that should answer. The run must land on that agent,
// not on whichever one sorts first: the id used to be written only to the message
// metadata, which the hub ignored, so every @mention answered on the first agent.
func TestPostThreadMessageDelegatesToTheMentionedAgent(t *testing.T) {
	first := &registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "first"}
	second := &registry.Entry{ID: "agent-2", Kind: registry.KindAgent, Name: "second"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), first, second)
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"hello second","mentions":["`+second.ID+`"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Delegated []string `json:"delegated"`
	}
	decodeJSON(t, w, &body)
	if len(body.Delegated) != 1 {
		t.Fatalf("delegated = %+v, want one task", body.Delegated)
	}
	task := d.Hub.Store().Get(context.Background(), body.Delegated[0])
	if task == nil {
		t.Fatalf("task %q not found", body.Delegated[0])
	}
	if task.ResourceID != second.ID {
		t.Fatalf("task ran on %q, want the mentioned %q", task.ResourceID, second.ID)
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

// ---------------------------------------------------------------------------
// threads: the prompt a mention delegates, and replies persisted on read
// ---------------------------------------------------------------------------

// The run gets the instruction, not the addressing: the console writes the
// "@handle" token into the body, and passing it through changed the prompt the
// agent saw ("@google-adk hi" instead of "hi").
func TestPostThreadMessageStripsTheMentionToken(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "google-adk"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), agent)
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"@google-adk hi","mentions":["`+agent.ID+`"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Delegated []string `json:"delegated"`
	}
	decodeJSON(t, w, &body)
	if len(body.Delegated) != 1 {
		t.Fatalf("delegated = %+v, want one task", body.Delegated)
	}
	task := d.Hub.Store().Get(context.Background(), body.Delegated[0])
	if task == nil || len(task.History) == 0 {
		t.Fatalf("task %q carries no history", body.Delegated[0])
	}
	if got := task.History[0].Text(); got != "hi" {
		t.Fatalf("prompt = %q, want the handle stripped", got)
	}
}

// A hand-typed @handle is authoritative. When the console's picker could not
// resolve it and sent its default agent, the run used to land on that default
// (aria-agent-cloud's offline mock, answering "agent=... steps=... digest=...")
// instead of the agent the person addressed.
func TestPostThreadMessageResolvesHandTypedHandleOverTheFallback(t *testing.T) {
	fallback := &registry.Entry{ID: "demo-agent", Kind: registry.KindAgent, Name: "aria-agent-cloud"}
	google := &registry.Entry{ID: "example-agent-google-adk", Kind: registry.KindAgent, Name: "google-adk"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), fallback, google)
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"@google-adk hi","mentions":["demo-agent"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Delegated []string `json:"delegated"`
		Failed    []string `json:"failed"`
	}
	decodeJSON(t, w, &body)
	if len(body.Delegated) != 1 || len(body.Failed) != 0 {
		t.Fatalf("delegated = %+v, failed = %+v", body.Delegated, body.Failed)
	}
	task := d.Hub.Store().Get(context.Background(), body.Delegated[0])
	if task == nil || len(task.History) == 0 {
		t.Fatalf("task %q carries no history", body.Delegated[0])
	}
	if task.ResourceID != google.ID {
		t.Fatalf("task ran on %q, want the mentioned %q", task.ResourceID, google.ID)
	}
	if got := task.History[0].Text(); got != "hi" {
		t.Fatalf("prompt = %q, want the handle stripped", got)
	}
}

// The scheduler path must carry the same target: the ticket's resource is read
// from the trigger metadata, so a stale client id there would reproduce the bug
// even though the direct delegation path was fixed.
func TestPostThreadMessageRoutesHandTypedHandleToTheMentionedAgent(t *testing.T) {
	fallback := &registry.Entry{ID: "demo-agent", Kind: registry.KindAgent, Name: "aria-agent-cloud"}
	google := &registry.Entry{ID: "example-agent-google-adk", Kind: registry.KindAgent, Name: "google-adk"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), fallback, google)
	d.Threads = mt
	d.Router = routerService(t)
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"@google-adk hi","mentions":["demo-agent"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	triggers, err := d.Router.ListTriggers(context.Background(), router.TriggerQuery{Limit: 10})
	if err != nil || len(triggers) != 1 {
		t.Fatalf("triggers = %+v, err = %v", triggers, err)
	}
	trig := triggers[0]
	if !trig.Explicit {
		t.Fatal("a resolved @handle must mark the trigger explicit")
	}
	if got, _ := trig.Meta["resource"].(string); got != google.ID {
		t.Fatalf("meta resource = %v, want %q", trig.Meta["resource"], google.ID)
	}
	if trig.Meta["mention"] != true {
		t.Fatal("a resolved @handle must be marked so the scheduler re-targets an existing ticket")
	}
	if trig.Body != "hi" {
		t.Fatalf("trigger body = %q, want the handle stripped", trig.Body)
	}
}

// An address to an agent nobody registered is not a request to the default
// worker: the message is kept, but no run is started and no trigger is injected.
func TestPostThreadMessageDoesNotDelegateAnUnknownHandle(t *testing.T) {
	fallback := &registry.Entry{ID: "demo-agent", Kind: registry.KindAgent, Name: "aria-agent-cloud"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), fallback)
	d.Threads = mt
	d.Router = routerService(t)
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages",
		`{"body":"@google-adk hi","mentions":[]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Delegated []string `json:"delegated"`
		Failed    []string `json:"failed"`
		Routed    bool     `json:"routed"`
	}
	decodeJSON(t, w, &body)
	if len(body.Delegated) != 0 || body.Routed {
		t.Fatalf("delegated = %+v, routed = %v", body.Delegated, body.Routed)
	}
	if len(body.Failed) != 1 || body.Failed[0] != "google-adk" {
		t.Fatalf("failed = %+v, want [google-adk]", body.Failed)
	}
	if tasks := d.Hub.Store().List(context.Background()); len(tasks) != 0 {
		t.Fatalf("an unknown handle opened %d tasks", len(tasks))
	}
	if triggers, _ := d.Router.ListTriggers(context.Background(), router.TriggerQuery{Limit: 10}); len(triggers) != 0 {
		t.Fatalf("an unknown handle injected %d triggers", len(triggers))
	}
	if msgs, _ := mt.ListMessages(context.Background(), th.ID, 0); len(msgs) != 1 || msgs[0].Body != "@google-adk hi" {
		t.Fatalf("the message was not kept as typed: %+v", msgs)
	}
}

func TestMentionHandles(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"@google-adk hi", []string{"google-adk"}},
		{"@a @b @c", []string{"a", "b", "c"}},
		{"@a @a @A", []string{"a"}}, // handles are case insensitive
		{"mail@example.com hi", nil},
		{"@", nil},
		{"hi there", nil},
	}
	for _, tc := range cases {
		got := mentionHandles(tc.body)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("mentionHandles(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestResolveMentionHandlesMatchesEverySpelling(t *testing.T) {
	google := &registry.Entry{
		ID: "adk-1", Kind: registry.KindAgent, Name: "google-adk", DisplayName: "Google ADK",
	}
	d := testDeps(t, openConfig(""), google)
	r := httptest.NewRequest(http.MethodPost, "/api/threads/1/messages", nil)
	got := d.resolveMentionHandles(r, []string{"GOOGLE-ADK", "adk-1", "Google ADK", "nobody"})
	if len(got) != 1 || got[0] != google.ID {
		t.Fatalf("resolved = %v, want [%s] once", got, google.ID)
	}
	// Without a registry there is nothing to resolve, but no panic either.
	if empty := (Deps{}).resolveMentionHandles(r, []string{"anyone"}); len(empty) != 0 {
		t.Fatalf("resolved without a registry = %v", empty)
	}
}

// Stripping must not eat text it cannot attribute: a body that is only the
// mention, a handle naming nobody, and an address inside a word all survive.
func TestPostThreadMessageKeepsHandlesItCannotAttribute(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "planner"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), agent)
	d.Threads = mt
	th, _ := mt.CreateThread(context.Background(), &threads.Thread{OwnerID: "dev", Title: "t"})
	h := Register(d)

	for _, tc := range []struct{ body, want string }{
		{"@planner", "@planner"},
		{"ask @nobody here", "ask @nobody here"},
		{"mail@planner", "mail@planner"},
	} {
		raw, _ := json.Marshal(map[string]any{"body": tc.body, "mentions": []string{agent.ID}})
		w := do(t, h, http.MethodPost, "/api/threads/"+th.ID+"/messages", string(raw), nil)
		if w.Code != http.StatusCreated {
			t.Fatalf("%q: status = %d (%s)", tc.body, w.Code, w.Body.String())
		}
		var body struct {
			Delegated []string `json:"delegated"`
		}
		decodeJSON(t, w, &body)
		if len(body.Delegated) != 1 {
			t.Fatalf("%q: delegated = %+v", tc.body, body.Delegated)
		}
		task := d.Hub.Store().Get(context.Background(), body.Delegated[0])
		if task == nil || len(task.History) == 0 {
			t.Fatalf("%q: task carries no history", tc.body)
		}
		if got := task.History[0].Text(); got != tc.want {
			t.Fatalf("%q: prompt = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// A reply that landed while nobody was streaming must still be there on a read:
// the SSE stream is the live path, not the only writer.
func TestGetThreadFillsTheReplyOfAFinishedTask(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "a1"}
	mt := newMemThreads()
	d := testDeps(t, openConfig(""), agent)
	d.Threads = mt
	ctx := context.Background()
	th, _ := mt.CreateThread(ctx, &threads.Thread{OwnerID: "dev", Title: "t"})

	task, err := d.Hub.Send(ctx, a2a.MessageSendParams{
		Message:  a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")),
		Metadata: map[string]any{"resourceId": agent.ID},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(task.History) < 2 {
		t.Fatalf("history = %+v, want a reply", task.History)
	}
	// The placeholder the post handler writes while the task is still running.
	if _, err := mt.UpsertAgentMessage(ctx, th.ID, task.ID, agent.ID, ""); err != nil {
		t.Fatalf("placeholder: %v", err)
	}

	h := Register(d)
	w := do(t, h, http.MethodGet, "/api/threads/"+th.ID, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Messages []threads.Message `json:"messages"`
	}
	decodeJSON(t, w, &body)
	want := task.History[len(task.History)-1].Text()
	got := ""
	for _, m := range body.Messages {
		if m.TaskID == task.ID {
			got = m.Body
		}
	}
	if got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

// Only a finished task has a reply to persist: a task still in flight, and a
// missing one, leave the placeholder alone.
func TestTaskReplyIgnoresUnfinishedTasks(t *testing.T) {
	if got := taskReply(nil); got != "" {
		t.Fatalf("nil task = %q, want empty", got)
	}
	running := &a2a.Task{
		Status:  a2a.TaskStatus{State: a2a.TaskStateWorking},
		History: []a2a.Message{{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.NewTextPart("partial")}}},
	}
	if got := taskReply(running); got != "" {
		t.Fatalf("working task = %q, want empty", got)
	}
	userOnly := &a2a.Task{
		Status:  a2a.TaskStatus{State: a2a.TaskStateCompleted},
		History: []a2a.Message{{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}}},
	}
	if got := taskReply(userOnly); got != "" {
		t.Fatalf("user-only task = %q, want empty", got)
	}
}
