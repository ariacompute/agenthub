package hub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
)

// ---------------------------------------------------------------------------
// stubs: they implement the persistence ports without a database so the
// degradation rules stay testable offline.
// ---------------------------------------------------------------------------

type stubEventRepo struct {
	mu     sync.Mutex
	events []Event
	fail   bool
	maxSeq uint64
}

func (s *stubEventRepo) Append(ctx context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("stub: append failed")
	}
	s.events = append(s.events, e)
	if e.Seq > s.maxSeq {
		s.maxSeq = e.Seq
	}
	return nil
}

func (s *stubEventRepo) Since(ctx context.Context, taskID string, after uint64, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return nil, errors.New("stub: since failed")
	}
	out := make([]Event, 0, len(s.events))
	for _, e := range s.events {
		if e.TaskID == taskID && e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *stubEventRepo) MaxSeq(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, errors.New("stub: max seq failed")
	}
	return s.maxSeq, nil
}

func (s *stubEventRepo) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// blockingEventRepo parks the writer until release is closed, which is how a
// saturated queue is reproduced deterministically.
type blockingEventRepo struct {
	release  chan struct{}
	entered  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	appended int
}

func (b *blockingEventRepo) Append(ctx context.Context, e Event) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	b.appended++
	b.mu.Unlock()
	return nil
}

func (b *blockingEventRepo) Since(ctx context.Context, taskID string, after uint64, limit int) ([]Event, error) {
	return nil, nil
}

func (b *blockingEventRepo) MaxSeq(ctx context.Context) (uint64, error) { return 0, nil }

func (b *blockingEventRepo) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.appended
}

type stubPushRepo struct {
	mu         sync.Mutex
	rows       []PushRecord
	failAppend bool
	failList   bool
	kept       int
}

func (s *stubPushRepo) Append(ctx context.Context, r PushRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAppend {
		return errors.New("stub: append failed")
	}
	s.rows = append(s.rows, r)
	return nil
}

func (s *stubPushRepo) List(ctx context.Context, taskID string, limit int) ([]PushRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failList {
		return nil, errors.New("stub: list failed")
	}
	out := make([]PushRecord, 0, len(s.rows))
	for _, r := range s.rows {
		if taskID == "" || r.TaskID == taskID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *stubPushRepo) Prune(ctx context.Context, keepPerTask int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept = keepPerTask
	return nil
}

type stubTaskRepo struct {
	mu    sync.Mutex
	tasks map[string]*a2a.Task
	spans []Span
	fail  bool
}

func newStubTaskRepo() *stubTaskRepo {
	return &stubTaskRepo{tasks: map[string]*a2a.Task{}}
}

func (s *stubTaskRepo) Create(ctx context.Context, t *a2a.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("stub: create failed")
	}
	cp := *t
	s.tasks[t.ID] = &cp
	return nil
}

func (s *stubTaskRepo) Get(ctx context.Context, id string) (*a2a.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return nil, errors.New("stub: get failed")
	}
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *stubTaskRepo) List(ctx context.Context) ([]*a2a.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*a2a.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

func (s *stubTaskRepo) SetState(ctx context.Context, id string, from, to a2a.TaskState, msg *a2a.Message) (*a2a.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	if t.Status.State != from {
		return nil, ErrStateConflict
	}
	t.Status = a2a.TaskStatus{State: to, Message: msg}
	cp := *t
	return &cp, nil
}

func (s *stubTaskRepo) AddArtifact(ctx context.Context, id string, art a2a.Artifact) (*a2a.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	t.Artifacts = append(t.Artifacts, art)
	cp := *t
	return &cp, nil
}

func (s *stubTaskRepo) AppendHistory(ctx context.Context, id string, msg a2a.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	t.History = append(t.History, msg)
	return nil
}

func (s *stubTaskRepo) AddSpan(ctx context.Context, span Span) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("stub: add span failed")
	}
	s.spans = append(s.spans, span)
	return nil
}

func (s *stubTaskRepo) Spans(ctx context.Context, id string) ([]Span, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Span, 0, len(s.spans))
	for _, sp := range s.spans {
		if sp.TaskID == id {
			out = append(out, sp)
		}
	}
	return out, nil
}

func (s *stubTaskRepo) SetPending(ctx context.Context, id string, p *PendingInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return ErrTaskNotFound
	}
	return nil
}

func (s *stubTaskRepo) Pending(ctx context.Context, id string) (*PendingInput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return nil, ErrTaskNotFound
	}
	return nil, nil
}

func (s *stubTaskRepo) SetPush(ctx context.Context, id string, cfg *a2a.PushNotificationConfig) error {
	return nil
}

func (s *stubTaskRepo) Push(ctx context.Context, id string) (*a2a.PushNotificationConfig, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// event persistence
// ---------------------------------------------------------------------------

func TestBrokerStartResumesSeqAndPersists(t *testing.T) {
	repo := &stubEventRepo{maxSeq: 10}
	b := NewBroker()
	if err := b.Start(repo, 8); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Start(repo, 8); err == nil {
		t.Fatal("starting twice must fail")
	}
	defer b.Close()

	b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus})
	waitFor(t, func() bool { return repo.count() == 1 })

	repo.mu.Lock()
	got := repo.events[0]
	repo.mu.Unlock()
	// The counter resumes after the highest persisted seq so a replayed frame
	// and a live one can never share a seq.
	if got.Seq != 11 {
		t.Fatalf("event seq = %d, want 11", got.Seq)
	}
	// Fan-out still works alongside persistence. The sequence counter is shared
	// with subscriber ids, so the invariant worth pinning is monotonicity: a
	// live frame can never reuse a seq that was already handed out.
	ch, cancel := b.Subscribe("task-1", 4)
	defer cancel()
	b.Publish(Event{TaskID: "task-1", Kind: EventKindSpan})
	select {
	case ev := <-ch:
		if ev.Seq <= got.Seq {
			t.Fatalf("live seq = %d, must be greater than the persisted %d", ev.Seq, got.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber received nothing")
	}
}

func TestBrokerStartRejectsBadInput(t *testing.T) {
	b := NewBroker()
	if err := b.Start(nil, 8); err == nil {
		t.Fatal("Start(nil) must fail")
	}
	failing := &stubEventRepo{fail: true}
	if err := b.Start(failing, 8); err == nil {
		t.Fatal("Start with an unreachable repository must fail")
	}
	// Close on a broker that never started must stay a no-op.
	b.Close()
	b.Close()
}

// TestBrokerPersistNeverBackPressures pins the most important rule of event
// persistence: a stalled database must not slow down task execution.
func TestBrokerPersistNeverBackPressures(t *testing.T) {
	repo := &blockingEventRepo{release: make(chan struct{}), entered: make(chan struct{})}
	b := NewBroker()
	if err := b.Start(repo, 1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The first frame parks the writer inside Append, which saturates the queue.
	b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus})
	<-repo.entered

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 16; i++ {
			b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked while the event writer was stalled")
	}

	close(repo.release)
	b.Close()
	if repo.count() < 1 {
		t.Fatal("Close must drain the queue before returning")
	}
}

func TestBrokerCloseDrainsQueuedFrames(t *testing.T) {
	repo := &stubEventRepo{}
	b := NewBroker()
	if err := b.Start(repo, 64); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 5; i++ {
		b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus})
	}
	b.Close()
	if got := repo.count(); got != 5 {
		t.Fatalf("persisted = %d, want 5 after Close drained the queue", got)
	}
	// Publishing after Close keeps working: persistence is best effort.
	b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus})
}

// ---------------------------------------------------------------------------
// push record persistence
// ---------------------------------------------------------------------------

func TestMockNotifierPersistsRecords(t *testing.T) {
	repo := &stubPushRepo{}
	n := &MockNotifier{Repo: repo}
	cfg := a2a.PushNotificationConfig{ID: "cfg", URL: "http://callback.example"}
	if err := n.Notify(context.Background(), cfg, "task-1", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	records := n.Records()
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}
	if records[0].TaskID != "task-1" || records[0].State != a2a.TaskStateCompleted {
		t.Fatalf("record = %+v", records[0])
	}
	// The persisted row is the source of truth, not the in-memory fallback.
	n.mu.Lock()
	memOnly := len(n.records)
	n.mu.Unlock()
	if memOnly != 0 {
		t.Fatalf("memory fallback grew alongside persistence: %d", memOnly)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Notify(ctx, cfg, "task-2", a2a.TaskStateFailed); err == nil {
		t.Fatal("a cancelled context must abort before any write")
	}
	if len(n.Records()) != 1 {
		t.Fatal("a cancelled notification must not be recorded")
	}
}

func TestMockNotifierFallsBackWhenPersistenceFails(t *testing.T) {
	repo := &stubPushRepo{failAppend: true, failList: true}
	n := &MockNotifier{Repo: repo}
	cfg := a2a.PushNotificationConfig{URL: "http://callback.example"}
	// A failing store must never change the notification outcome.
	if err := n.Notify(context.Background(), cfg, "task-1", a2a.TaskStateCompleted); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	records := n.Records()
	if len(records) != 1 || records[0].TaskID != "task-1" {
		t.Fatalf("records = %+v, want the in-memory fallback", records)
	}
}

// ---------------------------------------------------------------------------
// task persistence delegation
// ---------------------------------------------------------------------------

func TestTaskStoreDelegatesToRepository(t *testing.T) {
	repo := newStubTaskRepo()
	store := NewTaskStoreWithRepository(repo)

	task := store.Create("res-1", a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
	if task == nil || task.ID == "" {
		t.Fatalf("Create = %+v", task)
	}
	if got := store.Get(task.ID); got == nil || got.ID != task.ID {
		t.Fatalf("Get = %+v", got)
	}

	// The A2A contract decides legality, the repository performs the write.
	if _, err := store.SetState(task.ID, a2a.TaskStateCompleted, nil); err == nil {
		t.Fatal("submitted -> completed must be rejected")
	} else if httputil.AsError(err).Code != CodeInvalidStateTransition {
		t.Fatalf("error = %v, want invalid_state_transition", err)
	}
	if _, err := store.SetState(task.ID, a2a.TaskStateWorking, nil); err != nil {
		t.Fatalf("submitted -> working: %v", err)
	}
	if _, err := store.SetState("missing", a2a.TaskStateWorking, nil); httputil.AsError(err).Code != CodeTaskNotFound {
		t.Fatalf("missing task = %v, want task_not_found", err)
	}

	if _, err := store.AddArtifact(task.ID, a2a.Artifact{Name: "out"}); err != nil {
		t.Fatalf("AddArtifact: %v", err)
	}
	if err := store.AppendHistory(task.ID, a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("hi"))); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if len(store.Get(task.ID).History) != 2 {
		t.Fatalf("history = %+v", store.Get(task.ID).History)
	}

	span := store.StartSpan(task.ID, "res-1", "", "invoke:res-1")
	store.FinishSpan(span, SpanStatusOK, "")
	if spans := store.Spans(task.ID); len(spans) != 1 {
		t.Fatalf("spans = %+v", spans)
	}

	if err := store.SetPending(task.ID, "approve?"); err != nil {
		t.Fatalf("SetPending: %v", err)
	}
	if err := store.SetPush(task.ID, &a2a.PushNotificationConfig{URL: "http://cb"}); err != nil {
		t.Fatalf("SetPush: %v", err)
	}
	if len(store.List()) != 1 {
		t.Fatalf("List = %+v", store.List())
	}
}

func TestTaskStoreSurvivesPersistenceFailures(t *testing.T) {
	repo := newStubTaskRepo()
	repo.fail = true
	store := NewTaskStoreWithRepository(repo)

	// A broken store must surface as an honest error, never as a silent no-op.
	task := store.Create("res-1", a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
	if task == nil {
		t.Fatal("Create must still return the task it built")
	}
	if got := store.Get(task.ID); got != nil {
		t.Fatalf("Get after a failed write = %+v, want nil", got)
	}
	// A database failure is reported honestly: it is an internal error, not a
	// missing task.
	if _, err := store.SetState(task.ID, a2a.TaskStateWorking, nil); httputil.AsError(err).Code != httputil.CodeInternal {
		t.Fatalf("SetState = %v, want internal_error", err)
	}
	if err := store.AppendHistory(task.ID, a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("hi"))); err == nil {
		t.Fatal("AppendHistory must report the failure")
	}
	// A span that cannot be stored must not panic the run.
	span := store.StartSpan(task.ID, "res-1", "", "invoke:res-1")
	store.FinishSpan(span, SpanStatusOK, "")
}

func TestHubEventsDelegatesToRepository(t *testing.T) {
	cfg := testConfig()
	// Without an event repository the replay is empty instead of failing.
	h := New(cfg, nil, testOptions(), &MockNotifier{}, Persistence{})
	defer h.Close()
	if got := h.Events("task-1", 0, 10); got != nil {
		t.Fatalf("Events without a repository = %+v", got)
	}

	repo := &stubEventRepo{maxSeq: 3}
	h2 := New(cfg, nil, testOptions(), &MockNotifier{}, Persistence{Events: repo})
	defer h2.Close()
	h2.broker.Publish(Event{TaskID: "task-1", Kind: EventKindStatus})
	waitFor(t, func() bool { return len(mustEvents(h2, "task-1", 0)) == 1 })
	got := mustEvents(h2, "task-1", 0)
	if len(got) != 1 || got[0].Kind != EventKindStatus {
		t.Fatalf("Events = %+v", got)
	}
	if len(mustEvents(h2, "task-1", 4)) != 0 {
		t.Fatal("since must filter by seq")
	}
}

func mustEvents(h *Hub, taskID string, since uint64) []Event {
	return h.Events(taskID, since, 10)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within the timeout")
}
