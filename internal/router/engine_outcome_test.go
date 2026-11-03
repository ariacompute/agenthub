package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These cases pin what the engine does with the fact an executor reports back,
// and what it refuses. The interesting part is not the happy path — the beat
// covers that — but every branch that ends a ticket, parks it, or propagates a
// store failure, none of which the all-success in-memory store can reach.

func TestRecordAssignmentParksOnInputRequired(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	a := &Assignment{ID: "asg1", TicketID: "tk1", State: AssignmentStarted, StartedAt: c.now()}

	if err := e.RecordAssignment(ctx, "tk1", a, &AssignmentResult{State: AssignmentWaiting, TaskID: "task-1"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketWaiting || !tk.WaitsForPerson {
		t.Fatalf("ticket = %+v, want waiting and parked", tk)
	}
	if tk.TaskID != "task-1" {
		t.Fatalf("task id = %q", tk.TaskID)
	}
	rows, err := st.ListAssignments(ctx, "tk1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != AssignmentWaiting || rows[0].EndedAt == nil {
		t.Fatalf("assignment = %+v, want the report recorded", rows)
	}
}

func TestRecordAssignmentRequeuesOnAFollowUp(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	a := &Assignment{ID: "asg1", TicketID: "tk1", StartedAt: c.now()}
	// Arrived while the run was in flight: it must trigger one more run.
	if err := st.SaveTrigger(ctx, &Trigger{
		ID:          "trg1",
		Source:      string(SourceThread),
		TicketID:    "tk1",
		Disposition: string(DispositionRouted),
		At:          c.now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if err := e.RecordAssignment(ctx, "tk1", a, &AssignmentResult{State: AssignmentReported}); err != nil {
		t.Fatalf("record: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketQueued || tk.WaitsForPerson {
		t.Fatalf("ticket = %+v, want it queued for the follow-up", tk)
	}
}

func TestRecordAssignmentEndsWhenFollowedThreadsClose(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveThread(ctx, &Thread{ID: "th1", Closed: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Own: true, At: c.now()}); err != nil {
		t.Fatal(err)
	}

	a := &Assignment{ID: "asg1", TicketID: "tk1", StartedAt: c.now()}
	if err := e.RecordAssignment(ctx, "tk1", a, &AssignmentResult{State: AssignmentReported}); err != nil {
		t.Fatalf("record: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketDone || tk.EndedAt == nil {
		t.Fatalf("ticket = %+v, want done", tk)
	}
}

func TestRecordAssignmentGoesIdleWhileAThreadIsOpen(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveThread(ctx, &Thread{ID: "th1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Own: true, At: c.now()}); err != nil {
		t.Fatal(err)
	}

	a := &Assignment{ID: "asg1", TicketID: "tk1", StartedAt: c.now()}
	if err := e.RecordAssignment(ctx, "tk1", a, &AssignmentResult{State: AssignmentReported}); err != nil {
		t.Fatalf("record: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketIdle {
		t.Fatalf("state = %q, want idle: the thread is still live", tk.State)
	}
}

func TestRecordAssignmentBacksOffThenFails(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, Attempts: 1, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	a := &Assignment{ID: "asg1", TicketID: "tk1", StartedAt: c.now()}
	res := &AssignmentResult{State: AssignmentFailed, Error: strings.Repeat("boom ", 200)}

	if err := e.RecordAssignment(ctx, "tk1", a, res); err != nil {
		t.Fatalf("record: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketBlocked {
		t.Fatalf("state = %q, want blocked", tk.State)
	}
	if !tk.NextTryAt.After(c.now()) {
		t.Fatalf("next try = %v, want a backoff", tk.NextTryAt)
	}
	if len(tk.LastError) != 400 {
		t.Fatalf("last error length = %d, want the 400 character cap", len(tk.LastError))
	}

	// At the attempt budget the same failure ends the ticket instead.
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk2", State: TicketRunning, Attempts: 3, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordAssignment(ctx, "tk2", nil, &AssignmentResult{State: AssignmentFailed}); err != nil {
		t.Fatalf("record: %v", err)
	}
	failed, err := st.GetTicket(ctx, "tk2")
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != TicketFailed {
		t.Fatalf("state = %q, want failed", failed.State)
	}
}

func TestRecordAssignmentHandlesCancelAndUnknownStates(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	for _, id := range []string{"tk1", "tk2"} {
		if err := st.SaveTicket(ctx, &Ticket{ID: id, State: TicketRunning, CreatedAt: c.now()}); err != nil {
			t.Fatal(err)
		}
	}

	if err := e.RecordAssignment(ctx, "tk1", nil, &AssignmentResult{State: AssignmentCanceled}); err != nil {
		t.Fatalf("record: %v", err)
	}
	canceled, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != TicketCanceled {
		t.Fatalf("state = %q, want canceled", canceled.State)
	}

	// An empty state is not a verdict: the ticket goes idle rather than closing.
	if err := e.RecordAssignment(ctx, "tk2", nil, &AssignmentResult{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	idle, err := st.GetTicket(ctx, "tk2")
	if err != nil {
		t.Fatal(err)
	}
	if idle.State != TicketIdle {
		t.Fatalf("state = %q, want idle", idle.State)
	}
}

func TestRecordAssignmentRejectsUnknownTicket(t *testing.T) {
	e, _, _ := newErrorEngine(t)
	if err := e.RecordAssignment(context.Background(), "nope", nil, &AssignmentResult{State: AssignmentReported}); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("err = %v, want ErrTicketNotFound", err)
	}
}

func TestRecordAssignmentSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fail []string
	}{
		{name: "save assignment", fail: []string{"SaveAssignment"}},
		{name: "get ticket", fail: []string{"GetTicket"}},
		{name: "save ticket", fail: []string{"SaveTicket"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, c := newErrorEngine(t)
			if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
				t.Fatal(err)
			}
			st.only(tc.fail...)
			a := &Assignment{ID: "asg1", TicketID: "tk1", StartedAt: c.now()}
			if err := e.RecordAssignment(ctx, "tk1", a, &AssignmentResult{State: AssignmentWaiting}); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

// TestRecordAssignmentFallsBackWhenSupportingReadsFail pins the two defensive
// reads around a report: an unreadable follow-up list means "no follow-up", and
// an unreadable subscription list means "nothing left to follow".
func TestRecordAssignmentFallsBackWhenSupportingReadsFail(t *testing.T) {
	ctx := context.Background()

	t.Run("triggers unreadable", func(t *testing.T) {
		e, st, c := newErrorEngine(t)
		if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
			t.Fatal(err)
		}
		st.only("ListTriggers")
		a := &Assignment{ID: "asg1", TicketID: "tk1", StartedAt: c.now()}
		if err := e.RecordAssignment(ctx, "tk1", a, &AssignmentResult{State: AssignmentReported}); err != nil {
			t.Fatalf("record: %v", err)
		}
		tk, err := st.GetTicket(ctx, "tk1")
		if err != nil {
			t.Fatal(err)
		}
		if tk.State != TicketDone {
			t.Fatalf("state = %q, want done", tk.State)
		}
	})

	t.Run("subscriptions unreadable", func(t *testing.T) {
		e, st, c := newErrorEngine(t)
		if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
			t.Fatal(err)
		}
		st.only("ListSubscriptions")
		if err := e.RecordAssignment(ctx, "tk1", nil, &AssignmentResult{State: AssignmentReported}); err != nil {
			t.Fatalf("record: %v", err)
		}
		tk, err := st.GetTicket(ctx, "tk1")
		if err != nil {
			t.Fatal(err)
		}
		if tk.State != TicketDone {
			t.Fatalf("state = %q, want done", tk.State)
		}
	})

	t.Run("thread unreadable", func(t *testing.T) {
		e, st, c := newErrorEngine(t)
		if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
			t.Fatal(err)
		}
		if err := st.MemoryStore.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Own: true, At: c.now()}); err != nil {
			t.Fatal(err)
		}
		st.only("GetThread")
		if err := e.RecordAssignment(ctx, "tk1", nil, &AssignmentResult{State: AssignmentReported}); err != nil {
			t.Fatalf("record: %v", err)
		}
		tk, err := st.GetTicket(ctx, "tk1")
		if err != nil {
			t.Fatal(err)
		}
		if tk.State != TicketIdle {
			t.Fatalf("state = %q, want idle when the thread cannot be read", tk.State)
		}
	})
}

func TestEndTicketSurfacesARequeueFailure(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	// An unread follow-up that arrived after the run: ending the ticket must
	// return it to the queue, and a store that refuses that save is fatal.
	if err := st.SaveTrigger(ctx, &Trigger{
		ID:          "trg1",
		Source:      string(SourceThread),
		TicketID:    "tk1",
		Disposition: string(DispositionRouted),
		At:          c.now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	// No assignment to compare against, so the follow-up is not routed back to
	// this ticket; the reported run ends it instead, and ending it re-queues.
	st.only("SaveTrigger")
	if err := e.RecordAssignment(ctx, "tk1", nil, &AssignmentResult{State: AssignmentReported}); !errors.Is(err, errStoreDown) {
		t.Fatalf("err = %v, want the store failure", err)
	}
}

func TestCancelTicketRefusesAndEnds(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()

	if err := e.CancelTicket(ctx, "nope"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("unknown err = %v", err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "done1", State: TicketDone, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelTicket(ctx, "done1"); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("closed err = %v", err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelTicket(ctx, "tk1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketCanceled || tk.EndedAt == nil {
		t.Fatalf("ticket = %+v, want canceled", tk)
	}

	st.only("GetTicket")
	if err := e.CancelTicket(ctx, "tk1"); !errors.Is(err, errStoreDown) {
		t.Fatalf("err = %v, want the store failure", err)
	}
}

func TestCloseThreadEndsTheTicketsThatFollowIt(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveThread(ctx, &Thread{ID: "th1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Own: true, At: c.now()}); err != nil {
		t.Fatal(err)
	}

	if err := e.CloseThread(ctx, "th1", "resolved"); err != nil {
		t.Fatalf("close: %v", err)
	}
	th, err := st.GetThread(ctx, "th1")
	if err != nil {
		t.Fatal(err)
	}
	if !th.Closed || th.CloseOutcome != "resolved" {
		t.Fatalf("thread = %+v, want closed", th)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketDone {
		t.Fatalf("state = %q, want done", tk.State)
	}
}

func TestCloseThreadKeepsTicketsWithAnotherOpenThread(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveThread(ctx, &Thread{ID: "th1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveThread(ctx, &Thread{ID: "th2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	// Observation only, on two threads: closing one leaves the other live, so
	// the ticket keeps running.
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Context: true, At: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th2", TicketID: "tk1", Context: true, At: c.now()}); err != nil {
		t.Fatal(err)
	}

	if err := e.CloseThread(ctx, "th1", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketQueued {
		t.Fatalf("state = %q, want it left alone", tk.State)
	}
}

func TestCloseThreadSkipsDanglingAndClosedSubscribers(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveThread(ctx, &Thread{ID: "th1"}); err != nil {
		t.Fatal(err)
	}
	// One subscription whose ticket no longer exists, one on a closed ticket.
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "gone", At: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "done1", State: TicketDone, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "done1", At: c.now()}); err != nil {
		t.Fatal(err)
	}

	if err := e.CloseThread(ctx, "th1", "stale"); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestCloseThreadRefusesUnknownThread(t *testing.T) {
	e, _, _ := newErrorEngine(t)
	if err := e.CloseThread(context.Background(), "nope", ""); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("err = %v, want ErrThreadNotFound", err)
	}
}

func TestCloseThreadSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fail []string
	}{
		{name: "save thread", fail: []string{"SaveThread"}},
		{name: "list subscribers", fail: []string{"ListSubscribers"}},
		{name: "save subscription", fail: []string{"SaveSubscription"}},
		{name: "get subscriber ticket", fail: []string{"GetTicket"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, c := newErrorEngine(t)
			if err := st.MemoryStore.SaveThread(ctx, &Thread{ID: "th1"}); err != nil {
				t.Fatal(err)
			}
			if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.MemoryStore.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Own: true, At: c.now()}); err != nil {
				t.Fatal(err)
			}
			st.only(tc.fail...)
			if err := e.CloseThread(ctx, "th1", ""); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

func TestGetTicketDetailRefusesAndExplains(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()

	if _, err := e.GetTicket(ctx, "nope"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("unknown err = %v", err)
	}

	if err := st.SaveTrigger(ctx, &Trigger{ID: "trg1", Source: string(SourceManual)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTicket(ctx, &Ticket{
		ID:              "tk1",
		State:           TicketQueued,
		OriginTriggerID: "trg1",
		CreatedAt:       c.now(),
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := e.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Origin == nil || detail.Origin.ID != "trg1" {
		t.Fatalf("origin = %+v, want the trigger", detail.Origin)
	}

	for _, fail := range []string{"ListSubscriptions", "ListAssignments", "ListDecisions"} {
		t.Run(fail, func(t *testing.T) {
			st.only(fail)
			if _, err := e.GetTicket(ctx, "tk1"); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

func TestStatsSurfacesCountErrors(t *testing.T) {
	cases := []string{
		"CountTriggers",
		"CountTickets:queued",
		"CountTickets:running",
		"CountTickets:waiting",
		"CountSubscriptions",
	}
	for _, fail := range cases {
		t.Run(fail, func(t *testing.T) {
			e, st, _ := newErrorEngine(t)
			st.only(fail)
			if _, err := e.Stats(context.Background(), true); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}

	e, _, _ := newErrorEngine(t)
	s, err := e.Stats(context.Background(), true)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !s.Live || s.Streams != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestSavePolicyValidatesAndStores(t *testing.T) {
	e, st, _ := newErrorEngine(t)
	ctx := context.Background()

	if _, err := e.SavePolicy(ctx, nil); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("nil err = %v", err)
	}
	if _, err := e.SavePolicy(ctx, &Policy{Source: string(SourceCron)}); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("invalid mode err = %v", err)
	}
	got, err := e.SavePolicy(ctx, &Policy{
		Source:     string(SourceCron),
		Mode:       PolicyCandidate,
		Allow:      []string{" Alice ", "alice", ""},
		QuietStart: -1,
		QuietEnd:   -1,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(got.Allow) != 1 || got.Allow[0] != "Alice" {
		t.Fatalf("allow = %v, want the list normalized", got.Allow)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated at not stamped")
	}

	st.only("SavePolicy")
	if _, err := e.SavePolicy(ctx, &Policy{Source: string(SourceCron), Mode: PolicyCandidate, QuietStart: -1, QuietEnd: -1}); !errors.Is(err, errStoreDown) {
		t.Fatalf("err = %v, want the store failure", err)
	}
}

func TestPoliciesSeedsACoverageForAnOlderDatabase(t *testing.T) {
	e, st, _ := newErrorEngine(t)
	ctx := context.Background()
	// A database migrated before a source existed has no row for it.
	st.MemoryStore.mu.Lock()
	delete(st.MemoryStore.policies, string(SourceChannel))
	st.MemoryStore.mu.Unlock()

	got, err := e.Policies(ctx)
	if err != nil {
		t.Fatalf("policies: %v", err)
	}
	seen := false
	for _, p := range got {
		if p.Source == string(SourceChannel) {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("policies = %+v, want the missing source seeded", got)
	}

	st.only("ListPolicies")
	if _, err := e.Policies(ctx); !errors.Is(err, errStoreDown) {
		t.Fatalf("err = %v, want the store failure", err)
	}
}

func TestPolicyAndReadPathsSurfaceStoreErrors(t *testing.T) {
	e, st, _ := newErrorEngine(t)
	ctx := context.Background()
	st.only("GetPolicy")
	if _, err := e.Policy(ctx, string(SourceCron)); !errors.Is(err, errStoreDown) {
		t.Fatalf("policy err = %v", err)
	}

	st.only("Prune")
	if _, err := e.Prune(ctx, time.Now(), 10); !errors.Is(err, errStoreDown) {
		t.Fatalf("prune err = %v", err)
	}

	st.only("ListTriggers")
	if _, err := e.ListTriggers(ctx, TriggerQuery{}); !errors.Is(err, errStoreDown) {
		t.Fatalf("list triggers err = %v", err)
	}

	st.only("GetTrigger")
	if _, err := e.GetTrigger(ctx, "trg1"); !errors.Is(err, errStoreDown) {
		t.Fatalf("get trigger err = %v", err)
	}

	st.only("ListTickets")
	if _, err := e.ListTickets(ctx, TicketQuery{}); !errors.Is(err, errStoreDown) {
		t.Fatalf("list tickets err = %v", err)
	}

	st.only("EventsSince")
	if _, err := e.EventsSince(ctx, "", 0, 10); !errors.Is(err, errStoreDown) {
		t.Fatalf("events since err = %v", err)
	}
}

// TestBackoffAndAttemptDefaults pins the two knobs the retry loop depends on,
// including the ceiling that stops a long outage from scheduling work hours out.
func TestBackoffAndAttemptDefaults(t *testing.T) {
	e := NewEngine(Options{RetryBase: time.Minute})
	if got := e.maxAttempts(); got != 3 {
		t.Fatalf("max attempts = %d, want 3", got)
	}
	if got := e.backoff(10); got != 5*time.Minute {
		t.Fatalf("backoff(10) = %v, want the 5 minute ceiling", got)
	}
	if got := e.Clock(); got == nil {
		t.Fatal("clock not defaulted")
	}
}
