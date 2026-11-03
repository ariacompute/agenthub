package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/schedules"
)

// stubSchedules is the cron source's view of the schedule store: a map and one
// injectable failure, which is all RunSchedule and the scanner read.
type stubSchedules struct {
	items map[string]*schedules.Schedule
	err   error
}

func (s *stubSchedules) List(ctx context.Context, ownerID string) ([]*schedules.Schedule, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*schedules.Schedule, 0, len(s.items))
	for _, sc := range s.items {
		out = append(out, sc)
	}
	return out, nil
}

func (s *stubSchedules) Get(ctx context.Context, id, ownerID string) (*schedules.Schedule, error) {
	if s.err != nil {
		return nil, s.err
	}
	sc := s.items[id]
	if sc == nil || sc.OwnerID != ownerID {
		return nil, nil
	}
	return sc, nil
}

func (s *stubSchedules) Create(ctx context.Context, sc *schedules.Schedule) (*schedules.Schedule, error) {
	return sc, nil
}

func (s *stubSchedules) Update(ctx context.Context, sc *schedules.Schedule, ownerID string) (*schedules.Schedule, error) {
	return sc, nil
}

func (s *stubSchedules) Delete(ctx context.Context, id, ownerID string) (bool, error) {
	return false, nil
}

// newTestService builds the facade the HTTP layer sees, over the memory store and
// the hub stub, with a hand-cranked clock so a beat is always deterministic.
func newTestService(t *testing.T, o ServiceOptions) (*Service, *MemoryStore, *clock, *stubRunner) {
	t.Helper()
	c := &clock{at: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	store, ok := o.Store.(*MemoryStore)
	if !ok || store == nil {
		store = NewMemoryStore()
		o.Store = store
	}
	runner, ok := o.Runner.(*stubRunner)
	if !ok || runner == nil {
		runner = &stubRunner{task: taskIn(a2a.TaskStateCompleted)}
		o.Runner = runner
	}
	if o.Clock == nil {
		o.Clock = c.now
	}
	if o.Resource == "" {
		o.Resource = "res-agent"
	}
	if o.RetryBase == 0 {
		o.RetryBase = time.Millisecond
	}
	svc := NewService(o)
	t.Cleanup(svc.Stop)
	return svc, store, c, runner
}

// openOneTicket drives one trigger through intake and evaluation, returning the
// ticket the router opened for it.
func openOneTicket(t *testing.T, svc *Service, c *clock, body string) (*Ticket, *Trigger) {
	t.Helper()
	ctx := context.Background()
	trig := cronTrigger(c, body)
	trig.DedupKey = "cron:sched-1:" + body
	if _, _, err := svc.Engine().Intake(ctx, trig); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, err := svc.Engine().EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	tickets, err := svc.ListTickets(ctx, TicketQuery{Limit: 10})
	if err != nil || len(tickets) != 1 {
		t.Fatalf("want one ticket, got %d (err=%v)", len(tickets), err)
	}
	saved, err := storeTrigger(ctx, svc, trig)
	if err != nil {
		t.Fatalf("reload trigger: %v", err)
	}
	return tickets[0], saved
}

func storeTrigger(ctx context.Context, svc *Service, trig *Trigger) (*Trigger, error) {
	all, err := svc.ListTriggers(ctx, TriggerQuery{Limit: 10})
	if err != nil {
		return nil, err
	}
	for _, got := range all {
		if got.DedupKey == trig.DedupKey {
			return got, nil
		}
	}
	return nil, nil
}

func TestServiceInjectAdmitsOnceAndCounts(t *testing.T) {
	svc, _, c, _ := newTestService(t, ServiceOptions{})
	ctx := context.Background()

	first := &Trigger{
		Source: string(SourceManual), ActorKind: ActorSystem, Kind: "reminder",
		Body: "run the report", Explicit: true, At: c.now(), DueAt: c.now(),
		DedupKey: "manual:1",
	}
	out, fresh, err := svc.Inject(ctx, first)
	if err != nil || !fresh {
		t.Fatalf("first inject: fresh=%v err=%v", fresh, err)
	}
	if out == nil || out.ID == "" {
		t.Fatalf("a fresh trigger must be returned with an id, got %+v", out)
	}

	again := *first
	dup, fresh, err := svc.Inject(ctx, &again)
	if err != nil {
		t.Fatalf("redelivery must not error: %v", err)
	}
	if fresh || dup != nil {
		t.Fatalf("a redelivery is a no-op, got fresh=%v trigger=%+v", fresh, dup)
	}

	list, err := svc.ListTriggers(ctx, TriggerQuery{Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("the queue must hold one trigger, got %d (err=%v)", len(list), err)
	}

	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Pending != 1 {
		t.Fatalf("pending must count the queued trigger, got %d", stats.Pending)
	}
	if stats.Live {
		t.Fatal("the beat is not running until Run is called")
	}
	if stats.Passes != 0 || !stats.LastPassAt.IsZero() {
		t.Fatalf("no pass has run yet, got passes=%d at=%v", stats.Passes, stats.LastPassAt)
	}
}

func TestServiceRunScheduleFiresOneGoal(t *testing.T) {
	src := &stubSchedules{items: map[string]*schedules.Schedule{
		"sched-9": {ID: "sched-9", Goal: "collect the metrics", Trigger: "cron", CronExpr: "0 * * * *", Enabled: true, OwnerID: "usr-1"},
	}}
	svc, _, _, _ := newTestService(t, ServiceOptions{Schedules: src})
	ctx := context.Background()

	out, err := svc.RunSchedule(ctx, "sched-9", "usr-1")
	if err != nil {
		t.Fatalf("run schedule: %v", err)
	}
	if out == nil || out.Explicit != true || out.Body != "collect the metrics" {
		t.Fatalf("the fired trigger must carry the goal and be explicit, got %+v", out)
	}
	if out.ActorID != "usr-1" {
		t.Fatalf("the trigger inherits the schedule owner, got %q", out.ActorID)
	}
	if out.Meta["manual"] != true {
		t.Fatalf("a manual run must be marked in the metadata, got %+v", out.Meta)
	}

	if _, err := svc.RunSchedule(ctx, "nope", "usr-1"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("an unknown schedule must be not-found, got %v", err)
	}

	// Another owner's goal is not found — not "fired on their behalf". Firing it
	// would open a ticket whose actor id is the victim's, which is the
	// action-shaped half of the cross-tenant hole this parameter closes.
	if _, err := svc.RunSchedule(ctx, "sched-9", "usr-2"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("another owner's schedule must be not-found, got %v", err)
	}

	src.err = errors.New("db down")
	if _, err := svc.RunSchedule(ctx, "sched-9", "usr-1"); err == nil || errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("a store failure must surface as-is, got %v", err)
	}
}

func TestServiceRunScheduleNeedsAScheduleStore(t *testing.T) {
	svc, _, _, _ := newTestService(t, ServiceOptions{})
	// Without a schedule store the endpoint must refuse rather than invent a goal.
	if _, err := svc.RunSchedule(context.Background(), "sched-9", "usr-1"); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("want ErrPolicyInvalid, got %v", err)
	}
}

func TestServiceCancelTicketStopsTheTask(t *testing.T) {
	svc, _, c, runner := newTestService(t, ServiceOptions{})
	ctx := context.Background()
	ticket, _ := openOneTicket(t, svc, c, "write the weekly report")

	ticket.TaskID = "task-9"
	if err := svc.Engine().Store().SaveTicket(ctx, ticket); err != nil {
		t.Fatalf("save ticket: %v", err)
	}

	if err := svc.CancelTicket(ctx, ticket.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(runner.cancel) != 1 || runner.cancel[0] != "task-9" {
		t.Fatalf("the hub task behind the ticket must be cancelled, got %v", runner.cancel)
	}
	if err := svc.CancelTicket(ctx, ticket.ID); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("cancelling twice must be refused, got %v", err)
	}
	if err := svc.CancelTicket(ctx, "tkt-missing"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("an unknown ticket must be not-found, got %v", err)
	}
}

func TestServiceCancelLeavesTheTaskAloneBeforeItRan(t *testing.T) {
	svc, _, c, runner := newTestService(t, ServiceOptions{})
	ctx := context.Background()
	ticket, _ := openOneTicket(t, svc, c, "draft the announcement")

	// A queued ticket has no hub task yet, so there is nothing to cancel.
	if err := svc.CancelTicket(ctx, ticket.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(runner.cancel) != 0 {
		t.Fatalf("nothing was launched, so nothing may be cancelled: %v", runner.cancel)
	}
}

func TestServiceHandbackRequeuesOnce(t *testing.T) {
	svc, _, c, _ := newTestService(t, ServiceOptions{})
	ctx := context.Background()
	ticket, trig := openOneTicket(t, svc, c, "summarise the thread")

	if err := svc.Handback(ctx, ticket.ID, trig.ID, "wrong worker"); err != nil {
		t.Fatalf("hand back: %v", err)
	}
	back, err := svc.GetTrigger(ctx, trig.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if back.Disposition != string(DispositionPending) || back.TicketID != "" {
		t.Fatalf("a handed-back trigger returns to the queue, got %+v", back)
	}
	if back.Meta["handbackReason"] != "wrong worker" {
		t.Fatalf("the reason is kept for the console, got %+v", back.Meta)
	}

	// The second hand-back is refused: that is what stops two workers ping-ponging
	// the same trigger forever.
	if err := svc.Handback(ctx, ticket.ID, trig.ID, "again"); !errors.Is(err, ErrHandbackExhausted) {
		t.Fatalf("want ErrHandbackExhausted, got %v", err)
	}
	if err := svc.Handback(ctx, "tkt-missing", trig.ID, ""); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("want not-found for a missing ticket, got %v", err)
	}
}

func TestServiceThreadBindingAndUnsubscribe(t *testing.T) {
	svc, _, c, _ := newTestService(t, ServiceOptions{})
	ctx := context.Background()
	ticket, _ := openOneTicket(t, svc, c, "follow up with the customer")

	if err := svc.UnsubscribeThread(ctx, "thr-unknown"); err != nil {
		t.Fatalf("dropping an unknown thread is a no-op, got %v", err)
	}

	if err := svc.BindThread(ctx, ticket.ID, "thr-1", true, false); err != nil {
		t.Fatalf("bind: %v", err)
	}
	subs, err := svc.Engine().Store().ListSubscribers(ctx, "thr-1")
	if err != nil || len(subs) != 1 {
		t.Fatalf("want one subscription, got %d (err=%v)", len(subs), err)
	}

	if err := svc.UnsubscribeThread(ctx, "thr-1"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	subs, err = svc.Engine().Store().ListSubscribers(ctx, "thr-1")
	if err != nil || len(subs) != 0 {
		t.Fatalf("the thread must have no subscribers left, got %d (err=%v)", len(subs), err)
	}

	if err := svc.CloseThread(ctx, "thr-1", "done"); err != nil {
		t.Fatalf("close thread: %v", err)
	}
	if err := svc.CloseThread(ctx, "thr-1", "done"); err != nil {
		t.Fatalf("closing twice must be tolerated, got %v", err)
	}
}

func TestServicePoliciesRoundTrip(t *testing.T) {
	svc, _, _, _ := newTestService(t, ServiceOptions{})
	ctx := context.Background()

	policies, err := svc.Policies(ctx)
	if err != nil {
		t.Fatalf("policies: %v", err)
	}
	if len(policies) == 0 {
		t.Fatal("every source ships a default policy, got none")
	}

	bad := &Policy{Source: string(SourceChannel), Mode: "sometimes"}
	if _, err := svc.SavePolicy(ctx, bad); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("an unknown mode must be refused, got %v", err)
	}

	ok := &Policy{Source: string(SourceChannel), Mode: PolicyCandidate, Resource: "res-2", Allow: []string{"usr-1"}}
	saved, err := svc.SavePolicy(ctx, ok)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Source != string(SourceChannel) {
		t.Fatalf("the policy keeps its source, got %q", saved.Source)
	}

	got, err := svc.Engine().Policy(ctx, string(SourceChannel))
	if err != nil || got == nil || got.Resource != "res-2" {
		t.Fatalf("the saved policy must be readable back, got %+v (err=%v)", got, err)
	}
}

func TestServiceStreamsAndReplay(t *testing.T) {
	svc, _, c, _ := newTestService(t, ServiceOptions{})
	ctx := context.Background()

	stream, cancel := svc.Subscribe("", 8)
	defer cancel()

	if _, _, err := svc.Inject(ctx, &Trigger{
		Source: string(SourceManual), ActorKind: ActorSystem, Kind: "reminder",
		Body: "ping", Explicit: true, At: c.now(), DueAt: c.now(), DedupKey: "manual:ping",
	}); err != nil {
		t.Fatalf("inject: %v", err)
	}

	select {
	case e := <-stream:
		if e.Kind != EventTriggerReceived {
			t.Fatalf("the first frame is the intake, got %q", e.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("the live stream must see the intake")
	}

	replayed, err := svc.EventsSince(ctx, "", 0, 10)
	if err != nil || len(replayed) == 0 {
		t.Fatalf("replay must serve committed frames, got %d (err=%v)", len(replayed), err)
	}
	max, err := svc.MaxSeq(ctx)
	if err != nil || max == 0 {
		t.Fatalf("the highest sequence must be positive, got %d (err=%v)", max, err)
	}
	if replayed[len(replayed)-1].Seq > max {
		t.Fatalf("a replayed frame cannot be newer than MaxSeq")
	}
}

func TestServiceRunDrivesTheBeatAndStopsCleanly(t *testing.T) {
	svc, _, c, _ := newTestService(t, ServiceOptions{Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for !svc.Live() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !svc.Live() {
		t.Fatal("Run must report the beat as live")
	}

	if _, _, err := svc.Inject(context.Background(), &Trigger{
		Source: string(SourceManual), ActorKind: ActorSystem, Kind: "reminder",
		Body: "do the thing", Explicit: true, At: c.now(), DueAt: c.now(), DedupKey: "manual:beat",
	}); err != nil {
		t.Fatalf("inject: %v", err)
	}

	// The kick should turn the trigger into a ticket without waiting for a tick.
	for time.Now().Before(deadline) {
		tickets, err := svc.ListTickets(context.Background(), TicketQuery{Limit: 10})
		if err == nil && len(tickets) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	tickets, err := svc.ListTickets(context.Background(), TicketQuery{Limit: 10})
	if err != nil || len(tickets) != 1 {
		t.Fatalf("the beat must open one ticket, got %d (err=%v)", len(tickets), err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cancelled beat is a clean stop, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	svc.Stop() // second stop is a no-op, and both a signal and a defer may fire
	if svc.Live() {
		t.Fatal("Stop must leave the beat stopped")
	}
	stats, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats after stop: %v", err)
	}
	if stats.Passes == 0 {
		t.Fatal("at least one beat must have completed")
	}
	if stats.Live {
		t.Fatal("stats must report the beat as stopped")
	}

	closed, cancelSub := svc.Subscribe("", 1)
	defer cancelSub()
	if _, ok := <-closed; ok {
		t.Fatal("Stop closes the bus, so later subscribers see a closed stream")
	}
}
