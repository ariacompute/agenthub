package router

import (
	"context"
	"errors"
	"testing"
	"time"
)

// openTicket seeds one queued ticket in the store, which is what the attach path
// reads before it decides whether the owner still exists.
func openTicket(t *testing.T, store *MemoryStore, id string, state string) *Ticket {
	t.Helper()
	now := time.Now().UTC()
	tk := &Ticket{
		ID:        id,
		Title:     "seeded",
		Goal:      "seeded",
		State:     state,
		Resource:  "res-agent",
		CreatedAt: now,
		UpdatedAt: now,
		NextTryAt: now,
	}
	if err := store.SaveTicket(context.Background(), tk); err != nil {
		t.Fatalf("save ticket %s: %v", id, err)
	}
	return tk
}

func TestApplyAttachOpensANewTicketWhenTheOwnerIsGone(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()
	now := c.now()

	trig, _, err := e.Intake(ctx, cronTrigger(c, "follow up on the outage"))
	if err != nil || trig == nil {
		t.Fatalf("intake: trigger=%v err=%v", trig, err)
	}

	// The verdict names a ticket that was closed (or removed) between the claim
	// and the write: work must not be dropped on the floor.
	openTicket(t, store, "tkt-closed", TicketDone)
	d := Decision{Kind: string(DecisionAttach), TicketID: "tkt-closed", Reason: "late"}
	if err := e.apply(ctx, trig, d, "batch-1", now); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if trig.TicketID == "" || trig.TicketID == "tkt-closed" {
		t.Fatalf("trigger must point at a fresh ticket, got %q", trig.TicketID)
	}
	if trig.Disposition != string(DispositionRouted) {
		t.Fatalf("disposition = %q, want routed", trig.Disposition)
	}
	if tk, _ := store.GetTicket(ctx, trig.TicketID); tk == nil || !tk.Open() {
		t.Fatalf("the rebuilt ticket must exist and be open, got %+v", tk)
	}
}

func TestApplyAttachBindsTheThreadToTheExistingOwner(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()
	now := c.now()

	owner := openTicket(t, store, "tkt-owner", TicketQueued)
	trig, _, err := e.Intake(ctx, cronTrigger(c, "reply in the thread"))
	if err != nil || trig == nil {
		t.Fatalf("intake: trigger=%v err=%v", trig, err)
	}
	trig.ThreadID = "th-attach"

	d := Decision{Kind: string(DecisionAttach), TicketID: owner.ID, Reason: "thread_owner"}
	if err := e.apply(ctx, trig, d, "batch-1", now); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if trig.TicketID != owner.ID {
		t.Fatalf("an open owner must keep the work, got %q", trig.TicketID)
	}
	subs, err := store.ListSubscribers(ctx, "th-attach")
	if err != nil {
		t.Fatalf("list subscribers: %v", err)
	}
	if len(subs) != 1 || subs[0].TicketID != owner.ID {
		t.Fatalf("the thread must be bound to the owner, got %+v", subs)
	}
}

func TestApplyRecordsAVerdictThatOpensNothing(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()
	now := c.now()

	cases := []struct {
		kind        DecisionKind
		disposition Disposition
	}{
		{DecisionStop, DispositionStopped},
		{DecisionIgnore, DispositionIgnored},
		{DecisionUnavailable, DispositionUnavailable},
	}
	for i, tc := range cases {
		in := cronTrigger(c, "nothing to do")
		// Each case needs its own dedup key, or the second intake is a redelivery.
		in.DedupKey = "verdict-" + string(tc.kind) + string(rune('a'+i))
		trig, _, err := e.Intake(ctx, in)
		if err != nil || trig == nil {
			t.Fatalf("%s intake: %v", tc.kind, err)
		}
		if err := e.apply(ctx, trig, Decision{Kind: string(tc.kind)}, "batch", now); err != nil {
			t.Fatalf("%s apply: %v", tc.kind, err)
		}
		if trig.Disposition != string(tc.disposition) {
			t.Fatalf("%s: disposition = %q, want %q", tc.kind, trig.Disposition, tc.disposition)
		}
		if trig.TicketID != "" {
			t.Fatalf("%s must not open a ticket, got %q", tc.kind, trig.TicketID)
		}
		if tk, _ := store.ListTickets(ctx, TicketQuery{Limit: 10}); len(tk) != 0 {
			t.Fatalf("%s opened %d tickets", tc.kind, len(tk))
		}
	}
}

func TestApplyRefusesAVerdictOutsideTheVocabulary(t *testing.T) {
	e, _, c := newTestEngine(t)
	ctx := context.Background()

	trig, _, err := e.Intake(ctx, cronTrigger(c, "bogus verdict"))
	if err != nil || trig == nil {
		t.Fatalf("intake: %v", err)
	}
	err = e.apply(ctx, trig, Decision{Kind: "escalate"}, "batch", c.now())
	if !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("err = %v, want ErrPolicyInvalid", err)
	}
}

// A ticket that only follows a thread for context does not own it, so a new
// message must not be routed into it.
func TestOwnerOfIgnoresContextOnlySubscriptions(t *testing.T) {
	e, store, c := newTestEngine(t)
	ctx := context.Background()

	owner := openTicket(t, store, "tkt-context", TicketQueued)
	if err := e.BindThreadLocked(ctx, owner.ID, "th-ctx", false, true); err != nil {
		t.Fatalf("bind context-only: %v", err)
	}
	if got := e.ownerOf(ctx, "th-ctx"); got != nil {
		t.Fatalf("a context-only subscription must not own the thread, got %+v", got)
	}

	if err := e.BindThreadLocked(ctx, owner.ID, "th-ctx", true, false); err != nil {
		t.Fatalf("bind owning: %v", err)
	}
	if got := e.ownerOf(ctx, "th-ctx"); got == nil || got.ID != owner.ID {
		t.Fatalf("the owning subscription must be found, got %+v", got)
	}
	if got := e.ownerOf(ctx, "th-unknown"); got != nil {
		t.Fatalf("an unbound thread has no owner, got %+v", got)
	}
	_ = c
}
