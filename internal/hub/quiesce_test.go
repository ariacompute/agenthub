package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/registry"
)

func testHub(t *testing.T) *Hub {
	t.Helper()
	reg := registry.New()
	entry := &registry.Entry{ID: "model-1", Kind: registry.KindModel, Name: "test model"}
	if _, err := reg.Register(entry); err != nil {
		t.Fatalf("register: %v", err)
	}
	return New(&config.Config{
		MaxParallel:     2,
		CLITimeout:      time.Second,
		CLIAllowlist:    []string{"echo"},
		MaxOutputBytes:  4096,
		EventBuffer:     8,
		PushEnabled:     true,
		PushRecordLimit: 10,
	}, reg, adapter.Options{CLITimeout: time.Second, MaxOutputBytes: 4096}, &MockNotifier{}, Persistence{})
}

func plainSendParams() a2a.MessageSendParams {
	return a2a.MessageSendParams{Message: a2a.Message{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.NewTextPart("hello")}}}
}

func TestDrainRefusesNewWorkUntilItIsLifted(t *testing.T) {
	h := testHub(t)
	defer h.Close()

	if _, err := h.Send(context.Background(), plainSendParams()); err != nil {
		t.Fatalf("send before the drain: %v", err)
	}
	if err := h.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if h.ActiveTaskCount() != 0 {
		t.Fatalf("active tasks = %d, want 0 after a drain", h.ActiveTaskCount())
	}
	// The gate is closed: no new task may start while the database is being
	// rewritten underneath it.
	if _, err := h.Send(context.Background(), plainSendParams()); !errors.Is(err, ErrRestoring) {
		t.Fatalf("send while draining = %v, want ErrRestoring", err)
	}
	if _, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: "task-1"}); !errors.Is(err, ErrRestoring) {
		t.Fatalf("resume while draining = %v, want ErrRestoring", err)
	}

	h.Allow()
	if _, err := h.Send(context.Background(), plainSendParams()); err != nil {
		t.Fatalf("send after the drain was lifted: %v", err)
	}
}

// TestDrainWaitsForWorkInFlight uses the same gate primitives a task uses, so
// the waiting logic is exercised without having to make a real run slow.
func TestDrainWaitsForWorkInFlight(t *testing.T) {
	h := testHub(t)
	defer h.Close()

	if !h.enterTask() {
		t.Fatal("the gate must accept work while it is open")
	}
	if h.ActiveTaskCount() != 1 {
		t.Fatalf("active tasks = %d, want the one in flight", h.ActiveTaskCount())
	}

	drained := make(chan error, 1)
	go func() { drained <- h.Drain(context.Background()) }()

	// While the task is in flight the drain waits, and new work is refused.
	select {
	case err := <-drained:
		t.Fatalf("Drain returned %v while a task was still running", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := h.Send(context.Background(), plainSendParams()); !errors.Is(err, ErrRestoring) {
		t.Fatalf("send during the drain = %v, want ErrRestoring", err)
	}

	h.leaveTask()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not finish after the last task released the gate")
	}
	h.Allow()
	// Lifting the gate twice is harmless, which is what lets a restore defer it.
	h.Allow()
	if _, err := h.Send(context.Background(), plainSendParams()); err != nil {
		t.Fatalf("send after the gate reopened: %v", err)
	}
}

func TestDrainGivesUpWhenTheContextIsCancelled(t *testing.T) {
	h := testHub(t)
	defer h.Close()

	// Work in flight that never finishes: a drain must not wait forever, and it
	// must leave the gate open when it gives up, or the hub would stay closed
	// for good.
	if !h.enterTask() {
		t.Fatal("the gate must accept work while it is open")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain with a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := h.Send(context.Background(), plainSendParams()); err != nil {
		t.Fatalf("send after a cancelled drain = %v, want it accepted again", err)
	}
	h.leaveTask()
}

// A drain with nothing to wait for succeeds at once, however short the
// deadline: idleness is not a failure.
func TestDrainReturnsImmediatelyWhenIdle(t *testing.T) {
	h := testHub(t)
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("drain while idle: %v", err)
	}
	if _, err := h.Send(context.Background(), plainSendParams()); !errors.Is(err, ErrRestoring) {
		t.Fatalf("send after an idle drain = %v, want ErrRestoring", err)
	}
	h.Allow()
}

func TestResetEventSeqFollowsTheDatabase(t *testing.T) {
	h := testHub(t)
	defer h.Close()

	repo := &fakeEventRepo{maxSeq: 41}
	h.eventRepo = repo
	if err := h.ResetEventSeq(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := brokerSeq(h.broker); got != 42 {
		t.Fatalf("next seq = %d, want 42 (one past the highest persisted)", got)
	}
	// A restore can move the database backwards; the counter must follow it
	// rather than keeping a value that no longer exists.
	repo.maxSeq = 7
	if err := h.ResetEventSeq(); err != nil {
		t.Fatalf("reset again: %v", err)
	}
	if got := brokerSeq(h.broker); got != 8 {
		t.Fatalf("next seq = %d, want 8 after the database was rewound", got)
	}
	// Without an event repository there is nothing to reconcile, and that is
	// not an error: the in-memory broker has no persisted counter.
	h.eventRepo = nil
	if err := h.ResetEventSeq(); err != nil {
		t.Fatalf("reset without a repository: %v", err)
	}
}

func TestMockNotifierResetClearsTheMemoryLog(t *testing.T) {
	n := &MockNotifier{}
	err := n.Notify(context.Background(), a2a.PushNotificationConfig{URL: "https://example.com/hook"}, "task-1", a2a.TaskStateCompleted)
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(n.Records()) != 1 {
		t.Fatalf("records = %d, want 1", len(n.Records()))
	}
	n.Reset()
	if len(n.Records()) != 0 {
		t.Fatalf("records after reset = %d, want 0", len(n.Records()))
	}
}

// brokerSeq reads the broker's next sequence under its lock. A test in this
// package may look at the counter directly; nothing outside it should need to.
func brokerSeq(b *Broker) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next
}

// ---------------------------------------------------------------------------
// test doubles
// ---------------------------------------------------------------------------

type fakeEventRepo struct {
	maxSeq uint64
}

func (r *fakeEventRepo) Append(ctx context.Context, e Event) error { return nil }

func (r *fakeEventRepo) Since(ctx context.Context, taskID string, after uint64, limit int) ([]Event, error) {
	return nil, nil
}

func (r *fakeEventRepo) MaxSeq(ctx context.Context) (uint64, error) { return r.maxSeq, nil }
