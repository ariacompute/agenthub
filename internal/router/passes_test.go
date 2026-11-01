package router

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubExec is the Worker layer for the beat tests: it counts launches, can fail,
// and can hold a launch open so concurrency is observable.
type stubExec struct {
	mu       sync.Mutex
	calls    int
	inflight int32
	maxSeen  int32
	result   AssignmentResult
	gate     chan struct{}
	panicOn  bool
}

func (s *stubExec) Launch(ctx context.Context, t *Ticket, a *Assignment) (*AssignmentResult, error) {
	cur := atomic.AddInt32(&s.inflight, 1)
	for {
		seen := atomic.LoadInt32(&s.maxSeen)
		if cur <= seen || atomic.CompareAndSwapInt32(&s.maxSeen, seen, cur) {
			break
		}
	}
	defer atomic.AddInt32(&s.inflight, -1)
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.panicOn {
		panic("adapter exploded")
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return &AssignmentResult{State: AssignmentFailed, Error: "canceled"}, nil
		}
	}
	res := s.result
	if res.State == "" {
		res = AssignmentResult{State: AssignmentReported, TaskID: "task-1"}
	}
	return &res, nil
}

func (s *stubExec) Input(ctx context.Context, t *Ticket, msg string) (*AssignmentResult, error) {
	return &AssignmentResult{State: AssignmentReported}, nil
}

func (s *stubExec) Cancel(ctx context.Context, t *Ticket) error { return nil }

func (s *stubExec) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// waitFor polls until cond holds or the deadline passes. Polling beats sleeping a
// fixed amount: the test suite stays fast and does not flake on a loaded machine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startBeat(t *testing.T, engine *Engine, exec Executor, parallel int) *Passes {
	t.Helper()
	p := NewPasses(PassOptions{
		Engine: engine, Executor: exec, Interval: 10 * time.Millisecond, MaxParallel: parallel,
		// Same clock as the engine: the beat claims what the engine considers
		// due, so a hand cranked clock has to be shared.
		Clock: engine.Clock(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		if err := p.Run(ctx); err != nil {
			t.Errorf("beat stopped with %v", err)
		}
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("beat did not stop")
		}
	})
	return p
}

func TestPassesLaunchAReadyTicket(t *testing.T) {
	e, _, c := newTestEngine(t)
	exec := &stubExec{}
	startBeat(t, e, exec, 1)

	ctx := context.Background()
	if _, _, err := e.Intake(ctx, cronTrigger(c, "nightly rollup")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	waitFor(t, "the ticket to run", func() bool { return exec.callCount() == 1 })
	waitFor(t, "the ticket to be done", func() bool {
		tickets, _ := e.ListTickets(ctx, TicketQuery{})
		return len(tickets) == 1 && tickets[0].State == TicketDone
	})
}

// crank advances a hand driven clock so a backoff elapses without the test
// sleeping through it.
func crank(t *testing.T, c *clock, step time.Duration) {
	t.Helper()
	stop := make(chan struct{})
	ticker := time.NewTicker(5 * time.Millisecond)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				c.advance(step)
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
}

func TestPassesRetryAFailedLaunchUpToTheBudget(t *testing.T) {
	e, _, c := newTestEngine(t)
	exec := &stubExec{result: AssignmentResult{State: AssignmentFailed, Error: "provider down"}}
	startBeat(t, e, exec, 1)
	crank(t, c, 50*time.Millisecond)

	ctx := context.Background()
	if _, _, err := e.Intake(ctx, cronTrigger(c, "flaky job")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	waitFor(t, "the retry budget to be spent", func() bool {
		tickets, _ := e.ListTickets(ctx, TicketQuery{})
		return len(tickets) == 1 && tickets[0].State == TicketFailed
	})
	if got := exec.callCount(); got != 3 {
		t.Fatalf("launches = %d, want 3 (the default retry budget)", got)
	}
}

func TestPassesSurviveAPanickingWorker(t *testing.T) {
	e, _, c := newTestEngine(t)
	exec := &stubExec{panicOn: true}
	startBeat(t, e, exec, 1)

	ctx := context.Background()
	if _, _, err := e.Intake(ctx, cronTrigger(c, "exploding job")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	waitFor(t, "the panic to be recorded as a failure", func() bool {
		tickets, _ := e.ListTickets(ctx, TicketQuery{})
		return len(tickets) == 1 && tickets[0].LastError == "worker_panicked"
	})
	// The loop must still be alive: a second trigger is picked up after it.
	c.advance(time.Minute)
	if _, _, err := e.Intake(ctx, cronTrigger(c, "second job")); err != nil {
		t.Fatalf("intake 2: %v", err)
	}
	waitFor(t, "the loop to keep going", func() bool { return exec.callCount() >= 2 })
}

func TestPassesRespectTheParallelismBound(t *testing.T) {
	e, _, c := newTestEngine(t)
	exec := &stubExec{gate: make(chan struct{})}
	startBeat(t, e, exec, 1)

	ctx := context.Background()
	if _, _, err := e.Intake(ctx, cronTrigger(c, "job one")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	waitFor(t, "the first launch to be in flight", func() bool { return exec.callCount() == 1 })

	second := cronTrigger(c, "job two")
	second.SourceID = "sched-2"
	if _, _, err := e.Intake(ctx, second); err != nil {
		t.Fatalf("intake 2: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&exec.maxSeen); got > 1 {
		t.Fatalf("concurrent launches = %d, want at most 1", got)
	}
	close(exec.gate)
	waitFor(t, "the second job to run", func() bool { return exec.callCount() == 2 })
}

func TestKickCoalescesIntoOneExtraPass(t *testing.T) {
	e, _, _ := newTestEngine(t)
	p := NewPasses(PassOptions{Engine: e, Interval: time.Hour})
	for i := 0; i < 10; i++ {
		p.Kick()
	}
	select {
	case <-p.kick:
	default:
		t.Fatal("a kick should be queued")
	}
	select {
	case <-p.kick:
		t.Fatal("ten kicks must coalesce into one pending pass")
	default:
	}
}
