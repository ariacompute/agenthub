package router

// The fast path's rule verdict, the beat's defaults, and retention: three small
// surfaces that the engine tests reach only around the edges. The rule cases are
// the ones the console explains to an operator ("why did this become a stop?"),
// so each verdict is asserted with its reason, not just its kind.

import (
	"context"
	"testing"
	"time"
)

func TestRuleDecisionPrefersTheThreadsOwningTicket(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()
	now := c.now()

	ticket := &Ticket{ID: "tk-owner", State: "running", CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTicket(ctx, ticket); err != nil {
		t.Fatalf("save ticket: %v", err)
	}
	if err := store.SaveSubscription(ctx, &Subscription{
		ThreadID: "th-1", TicketID: ticket.ID, Own: true,
	}); err != nil {
		t.Fatalf("save subscription: %v", err)
	}

	// A person in a thread that a ticket already owns is relayed to it.
	human := &Trigger{ID: "t-human", Source: string(SourceThread), ThreadID: "th-1", ActorKind: ActorHuman, At: now}
	if d := e.ruleDecision(ctx, human); d.Kind != string(DecisionAttach) || d.TicketID != ticket.ID {
		t.Fatalf("human in an owned thread = %+v, want attach to %s", d, ticket.ID)
	}

	// Background traffic in the same thread is left to the owner rather than
	// opening a second ticket.
	background := &Trigger{ID: "t-agent", Source: string(SourceThread), ThreadID: "th-1", ActorKind: ActorAgent, At: now}
	if d := e.ruleDecision(ctx, background); d.Kind != string(DecisionStop) || d.TicketID != ticket.ID {
		t.Fatalf("background in an owned thread = %+v, want stop on %s", d, ticket.ID)
	}

	// The ticket a trigger was handed back from does not get to claim it again.
	handedBack := &Trigger{
		ID: "t-back", Source: string(SourceThread), ThreadID: "th-1", ActorKind: ActorAgent,
		Meta: map[string]any{"handbackFrom": ticket.ID}, At: now,
	}
	if d := e.ruleDecision(ctx, handedBack); d.Kind == string(DecisionStop) {
		t.Fatalf("handed-back trigger = %+v, want it evaluated rather than stopped by the same ticket", d)
	}

	// An explicit request outside a ticket is accepted by the rule layer.
	explicit := &Trigger{ID: "t-explicit", Source: string(SourceThread), Explicit: true, At: now}
	if d := e.ruleDecision(ctx, explicit); d.Kind != string(DecisionAccept) || d.Reason != "explicit_request" {
		t.Fatalf("explicit request = %+v, want accept / explicit_request", d)
	}

	// A schedule and a manual run are explicit by construction.
	manual := &Trigger{ID: "t-manual", Source: string(SourceManual), At: now}
	if d := e.ruleDecision(ctx, manual); d.Kind != string(DecisionAccept) || d.Reason != "scheduled" {
		t.Fatalf("manual trigger = %+v, want accept / scheduled", d)
	}
}

func TestPassesDefaultsAndRetentionSweep(t *testing.T) {
	// The zero value is what a misconfigured deployment passes: it must still
	// produce a working beat rather than a busy loop or an unbounded fan-out.
	p := NewPasses(PassOptions{})
	if p.interval <= 0 || p.maxParallel <= 0 || p.clock == nil {
		t.Fatalf("defaults = interval %v, parallel %d, clock %v", p.interval, p.maxParallel, p.clock)
	}

	e, store, c := newTestEngine(t)
	ctx := context.Background()
	now := c.now()
	old := &Trigger{
		ID: "old", Source: string(SourceThread), Disposition: string(DispositionRouted),
		At: now.Add(-2 * time.Hour), DueAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}
	if _, err := store.AppendTrigger(ctx, old); err != nil {
		t.Fatalf("seed trigger: %v", err)
	}

	beat := NewPasses(PassOptions{
		Engine:     e,
		PruneEvery: time.Nanosecond,
		TriggerTTL: time.Hour,
		Clock:      c.now,
	})
	beat.pass(ctx)

	if gone, err := store.GetTrigger(ctx, "old"); err != nil || gone != nil {
		t.Fatalf("trigger past its TTL = %+v, %v (want pruned)", gone, err)
	}
	// The sweep ran inside the beat, so the water level accounts for it rather
	// than reporting a failed pass.
	beat.mu.Lock()
	lastError := beat.stats.LastError
	passes := beat.stats.Passes
	beat.mu.Unlock()
	if lastError != "" {
		t.Fatalf("pass recorded an error: %s", lastError)
	}
	if passes == 0 {
		t.Fatal("the beat did not record a pass")
	}
}
