package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is a hand-cranked time source: the tests drive the beat instead of
// sleeping through it. The mutex is not decoration: a beat runs in its own
// goroutine and reads the clock while the test body cranks it, which -race
// reports as a data race on an otherwise correct test.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newTestEngine(t *testing.T) (*Engine, *MemoryStore, *clock) {
	t.Helper()
	c := &clock{at: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	store := NewMemoryStore()
	bus := NewBus()
	t.Cleanup(bus.Close)
	e := NewEngine(Options{Store: store, Bus: bus, Clock: c.now, Resource: "res-agent", RetryBase: time.Millisecond})
	return e, store, c
}

func cronTrigger(c *clock, body string) *Trigger {
	return &Trigger{
		Source:    string(SourceCron),
		SourceID:  "sched-1",
		ActorKind: ActorSystem,
		Body:      body,
		Explicit:  true,
		At:        c.now(),
		DueAt:     c.now(),
		Meta:      map[string]any{"goal": body},
	}
}

func TestIntakeDedupesARedelivery(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	first := cronTrigger(c, "collect the metrics")
	first.DedupKey = "cron:sched-1:2026110110"
	if _, fresh, err := e.Intake(ctx, first); err != nil || !fresh {
		t.Fatalf("first intake: fresh=%v err=%v", fresh, err)
	}

	second := cronTrigger(c, "collect the metrics")
	second.DedupKey = "cron:sched-1:2026110110"
	got, fresh, err := e.Intake(ctx, second)
	if err != nil {
		t.Fatalf("second intake: %v", err)
	}
	if fresh || got != nil {
		t.Fatalf("a redelivery must be a no-op, got fresh=%v trigger=%+v", fresh, got)
	}
}

func TestEvaluateDueOpensATicketForACronTrigger(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.Intake(ctx, cronTrigger(c, "write the weekly report")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	applied, err := e.EvaluateDue(ctx, c.now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}

	tickets, err := e.ListTickets(ctx, TicketQuery{})
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
	tk := tickets[0]
	if tk.Goal != "write the weekly report" {
		t.Fatalf("goal = %q", tk.Goal)
	}
	if tk.Resource != "res-agent" {
		t.Fatalf("resource = %q, want the instance default", tk.Resource)
	}
	if tk.State != TicketQueued {
		t.Fatalf("state = %q, want queued", tk.State)
	}

	// The verdict is recorded, so the console can explain the ticket.
	triggers, err := e.ListTriggers(ctx, TriggerQuery{})
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) != 1 || triggers[0].Disposition != string(DispositionRouted) {
		t.Fatalf("trigger disposition = %+v, want routed", triggers)
	}
	if triggers[0].TicketID != tk.ID {
		t.Fatalf("trigger ticket = %q, want %q", triggers[0].TicketID, tk.ID)
	}
	if triggers[0].DecisionID == "" {
		t.Fatal("trigger carries no decision id")
	}
}

func TestHumanMessageOnASubscribedThreadSkipsTheEvaluation(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	// A thread message addressed at the router opens a ticket that owns the
	// thread.
	opening := &Trigger{
		Source:    string(SourceThread),
		ThreadID:  "th-1",
		ActorID:   "u-1",
		ActorKind: ActorHuman,
		Body:      "please fix the failing build",
		Explicit:  true,
		At:        c.now(),
		DueAt:     c.now(),
	}
	if _, _, err := e.Intake(ctx, opening); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, err := e.EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	tickets, _ := e.ListTickets(ctx, TicketQuery{})
	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
	owner := tickets[0]

	c.advance(time.Minute)
	followUp := &Trigger{
		Source:    string(SourceThread),
		ThreadID:  "th-1",
		ActorID:   "u-1",
		ActorKind: ActorHuman,
		Body:      "also check the logs",
		At:        c.now(),
		DueAt:     c.now(),
	}
	got, fresh, err := e.Intake(ctx, followUp)
	if err != nil || !fresh {
		t.Fatalf("follow-up: fresh=%v err=%v", fresh, err)
	}
	if got.Disposition != string(DispositionRouted) {
		t.Fatalf("disposition = %q, want routed by subscription", got.Disposition)
	}
	if got.TicketID != owner.ID {
		t.Fatalf("ticket = %q, want the thread owner %q", got.TicketID, owner.ID)
	}

	// Nothing was queued for evaluation, so a beat changes nothing.
	if applied, err := e.EvaluateDue(ctx, c.now()); err != nil || applied != 0 {
		t.Fatalf("evaluate = %d, %v; want 0, nil", applied, err)
	}
	after, _ := e.ListTickets(ctx, TicketQuery{})
	if len(after) != 1 {
		t.Fatalf("a pass-through must not open a second ticket, got %d", len(after))
	}
}

// A thread some ticket already owns passes human messages straight through. An
// explicit @mention still names the worker the person wants, so the pass-through
// has to re-target that ticket instead of answering from the agent that opened
// the conversation.
func TestExplicitMentionReTargetsTheThreadTicket(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()

	opening := &Trigger{
		Source: string(SourceThread), ThreadID: "th-retarget",
		ActorID: "u-1", ActorKind: ActorHuman, Explicit: true,
		Body: "hi", At: c.now(), DueAt: c.now(),
		Meta: map[string]any{"resource": "aria-agent-cloud"},
	}
	if _, _, err := e.Intake(ctx, opening); err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, err := e.EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	tickets, _ := e.ListTickets(ctx, TicketQuery{})
	if len(tickets) != 1 || tickets[0].Resource != "aria-agent-cloud" {
		t.Fatalf("owner = %+v", tickets)
	}
	owner := tickets[0]

	c.advance(time.Minute)
	followUp := &Trigger{
		Source: string(SourceThread), ThreadID: "th-retarget",
		ActorID: "u-1", ActorKind: ActorHuman, Explicit: true,
		Body: "@google-adk hi", At: c.now(), DueAt: c.now(),
		Meta: map[string]any{"resource": "google-adk", "mention": true},
	}
	got, fresh, err := e.Intake(ctx, followUp)
	if err != nil || !fresh {
		t.Fatalf("follow-up: fresh=%v err=%v", fresh, err)
	}
	if got.Disposition != string(DispositionRouted) {
		t.Fatalf("disposition = %q, want the subscription pass-through", got.Disposition)
	}
	after, _ := store.GetTicket(ctx, owner.ID)
	if after == nil || after.Resource != "google-adk" {
		t.Fatalf("ticket resource = %+v, want google-adk", after)
	}
	if all, _ := e.ListTickets(ctx, TicketQuery{}); len(all) != 1 {
		t.Fatalf("re-targeting opened %d tickets, want 1", len(all))
	}

	// A plain follow-up still carries the client's default worker, but without
	// an @handle it must not undo the re-target.
	c.advance(time.Minute)
	plain := &Trigger{
		Source: string(SourceThread), ThreadID: "th-retarget",
		ActorID: "u-1", ActorKind: ActorHuman, Explicit: true,
		Body: "thanks", At: c.now(), DueAt: c.now(),
		Meta: map[string]any{"resource": "aria-agent-cloud"},
	}
	if _, _, err := e.Intake(ctx, plain); err != nil {
		t.Fatalf("plain follow-up: %v", err)
	}
	after, _ = store.GetTicket(ctx, owner.ID)
	if after.Resource != "google-adk" {
		t.Fatalf("a plain follow-up changed the resource to %q", after.Resource)
	}
}

func TestHandbackIsAllowedOnceThenRefused(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	opening := &Trigger{
		Source: string(SourceThread), ThreadID: "th-2",
		ActorID: "u-1", ActorKind: ActorHuman, Explicit: true,
		At: c.now(), DueAt: c.now(),
	}
	if _, _, err := e.Intake(ctx, opening); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, err := e.EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	tickets, _ := e.ListTickets(ctx, TicketQuery{})
	triggers, _ := e.ListTriggers(ctx, TriggerQuery{})

	if err := e.Handback(ctx, tickets[0].ID, triggers[0].ID, "not mine"); err != nil {
		t.Fatalf("first handback: %v", err)
	}
	if err := e.Handback(ctx, tickets[0].ID, triggers[0].ID, "still not mine"); !errors.Is(err, ErrHandbackExhausted) {
		t.Fatalf("second handback = %v, want ErrHandbackExhausted", err)
	}
	requeued, _ := e.GetTrigger(ctx, triggers[0].ID)
	if requeued.Disposition != string(DispositionPending) {
		t.Fatalf("disposition = %q, want pending", requeued.Disposition)
	}
	if metaString(requeued.Meta, "handbackFrom") != tickets[0].ID {
		t.Fatalf("handbackFrom = %q", metaString(requeued.Meta, "handbackFrom"))
	}
}

func TestClosingTheThreadEndsTheTicket(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	opening := &Trigger{
		Source: string(SourceThread), ThreadID: "th-3",
		ActorID: "u-1", ActorKind: ActorHuman, Explicit: true,
		At: c.now(), DueAt: c.now(),
	}
	if _, _, err := e.Intake(ctx, opening); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, err := e.EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	tickets, _ := e.ListTickets(ctx, TicketQuery{})
	tk, _, err := e.NextTicket(ctx, c.now())
	if err != nil || tk == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := e.RecordAssignment(ctx, tk.ID, &Assignment{ID: "asg-1", TicketID: tk.ID, StartedAt: c.now()},
		&AssignmentResult{State: AssignmentReported, TaskID: "task-1"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// The thread is still open, so the ticket waits for the next event instead
	// of closing itself.
	idle, _ := e.GetTicket(ctx, tk.ID)
	if idle.Ticket.State != TicketIdle {
		t.Fatalf("state = %q, want idle", idle.Ticket.State)
	}

	if err := e.CloseThread(ctx, "th-3", "merged"); err != nil {
		t.Fatalf("close thread: %v", err)
	}
	done, _ := e.GetTicket(ctx, tk.ID)
	if done.Ticket.State != TicketDone {
		t.Fatalf("state = %q, want done after the thread closed", done.Ticket.State)
	}
	if done.Ticket.EndedAt == nil {
		t.Fatal("ended ticket has no endedAt")
	}
	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
}

func TestFailedTicketStopsAfterTheRetryBudget(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()
	e.opts.MaxAttempts = 2

	if _, _, err := e.Intake(ctx, cronTrigger(c, "flaky job")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, err := e.EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		tk, a, err := e.NextTicket(ctx, c.now())
		if err != nil || tk == nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if err := e.RecordAssignment(ctx, tk.ID, a, &AssignmentResult{State: AssignmentFailed, Error: "boom"}); err != nil {
			t.Fatalf("record: %v", err)
		}
		c.advance(time.Second)
	}
	tickets, _ := e.ListTickets(ctx, TicketQuery{})
	if tickets[0].State != TicketFailed {
		t.Fatalf("state = %q, want failed after the retry budget", tickets[0].State)
	}
	if tickets[0].LastError != "boom" {
		t.Fatalf("lastError = %q", tickets[0].LastError)
	}
}

func TestUnsubscribeDropsEverySubscriptionOnAThread(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()

	opening := &Trigger{
		Source: string(SourceThread), ThreadID: "th-4",
		ActorID: "u-1", ActorKind: ActorHuman, Explicit: true,
		At: c.now(), DueAt: c.now(),
	}
	if _, _, err := e.Intake(ctx, opening); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, err := e.EvaluateDue(ctx, c.now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if subs, _ := store.ListSubscribers(ctx, "th-4"); len(subs) != 1 {
		t.Fatalf("subscribers = %d, want 1", len(subs))
	}
	if err := e.UnsubscribeThread(ctx, "th-4"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if subs, _ := store.ListSubscribers(ctx, "th-4"); len(subs) != 0 {
		t.Fatalf("subscribers = %d, want 0", len(subs))
	}
}

func TestPolicyDefaultsCoverEverySource(t *testing.T) {
	e, _, _ := newTestEngine(t)
	policies, err := e.Policies(context.Background())
	if err != nil {
		t.Fatalf("policies: %v", err)
	}
	if len(policies) != len(Sources) {
		t.Fatalf("policies = %d, want %d", len(policies), len(Sources))
	}
	for _, p := range policies {
		if p.Mode != PolicyCandidate {
			t.Fatalf("source %s mode = %q, want candidate", p.Source, p.Mode)
		}
	}
}

func TestSavePolicyRejectsInvalidInput(t *testing.T) {
	e, _, _ := newTestEngine(t)
	if _, err := e.SavePolicy(context.Background(), &Policy{Source: "nope", Mode: PolicyCandidate, QuietStart: -1, QuietEnd: -1}); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("err = %v, want ErrPolicyInvalid", err)
	}
}

func TestPruneDropsOnlyOldHistory(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	if _, _, err := e.Intake(ctx, cronTrigger(c, "old job")); err != nil {
		t.Fatalf("intake: %v", err)
	}
	c.advance(48 * time.Hour)
	fresh := cronTrigger(c, "fresh job")
	fresh.SourceID = "sched-2"
	if _, _, err := e.Intake(ctx, fresh); err != nil {
		t.Fatalf("intake 2: %v", err)
	}

	removed, err := e.Prune(ctx, c.now().Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed == 0 {
		t.Fatal("nothing was pruned")
	}
	triggers, _ := e.ListTriggers(ctx, TriggerQuery{})
	if len(triggers) != 1 || triggers[0].Body != "fresh job" {
		t.Fatalf("remaining triggers = %+v, want only the fresh one", triggers)
	}
}
