package router

// The in-memory store is the default backend (and the one the service uses when
// no database is configured), so its filters are the behaviour most deployments
// actually get. These cases drive the store directly: the engine tests cover what
// the router decides, this file covers what the store returns while deciding.

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreClonesWhatItHandsOut(t *testing.T) {
	if cloneTrigger(nil) != nil {
		t.Fatal("cloneTrigger(nil) must stay nil")
	}
	trig := &Trigger{ID: "t1", Meta: map[string]any{"k": "v"}}
	cp := cloneTrigger(trig)
	cp.Meta["k"] = "changed"
	if trig.Meta["k"] != "v" {
		t.Fatal("the clone shares its metadata map with the source")
	}

	end := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ticket := &Ticket{ID: "tk1", EndedAt: &end}
	tcp := cloneTicket(ticket)
	if tcp.EndedAt == ticket.EndedAt {
		t.Fatal("the clone shares its EndedAt pointer with the source")
	}
	*tcp.EndedAt = end.Add(time.Hour)
	if !ticket.EndedAt.Equal(end) {
		t.Fatal("mutating the clone moved the source\u0027s EndedAt")
	}
	if cloneTicket(nil) != nil {
		t.Fatal("cloneTicket(nil) must stay nil")
	}
}

func TestMemoryStoreClaimDueTriggersSkipsWhatItShouldNotTake(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, trig := range []*Trigger{
		{ID: "due", DueAt: now.Add(-time.Minute), Disposition: string(DispositionPending), UpdatedAt: now},
		{ID: "later", DueAt: now.Add(time.Hour), Disposition: string(DispositionPending), UpdatedAt: now},
		{ID: "settled", DueAt: now.Add(-time.Minute), Disposition: string(DispositionRouted), UpdatedAt: now},
	} {
		if _, err := s.AppendTrigger(ctx, trig); err != nil {
			t.Fatalf("append %s: %v", trig.ID, err)
		}
	}
	// A row can disappear between being queued and being claimed (pruned, or a
	// concurrent clear); the scan must skip it rather than panic on a nil.
	s.triggerOrder = append(s.triggerOrder, "ghost")

	due, err := s.ClaimDueTriggers(ctx, now, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(due) != 1 || due[0].ID != "due" {
		t.Fatalf("claimed = %+v, want only the due pending trigger", due)
	}

	// Claiming moves the due time past the window, so a second pass inside it
	// takes nothing.
	again, err := s.ClaimDueTriggers(ctx, now, 10)
	if err != nil {
		t.Fatalf("claim again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("claimed twice: %+v", again)
	}
}

func TestMemoryStoreListTriggersFilters(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, trig := range []*Trigger{
		{ID: "cron-a", Source: string(SourceCron), ThreadID: "th-1", TicketID: "tk-1", Disposition: string(DispositionRouted), At: now, UpdatedAt: now},
		{ID: "cron-b", Source: string(SourceCron), ThreadID: "th-2", Disposition: string(DispositionPending), At: now, UpdatedAt: now},
		{ID: "manual", Source: string(SourceManual), ThreadID: "th-1", Disposition: string(DispositionPending), At: now, UpdatedAt: now},
	} {
		if _, err := s.AppendTrigger(ctx, trig); err != nil {
			t.Fatalf("append %s: %v", trig.ID, err)
		}
	}

	cases := []struct {
		name string
		q    TriggerQuery
		want []string
	}{
		{"by source", TriggerQuery{Source: string(SourceCron)}, []string{"cron-b", "cron-a"}},
		{"by disposition", TriggerQuery{Disposition: string(DispositionPending)}, []string{"manual", "cron-b"}},
		{"by thread", TriggerQuery{ThreadID: "th-1"}, []string{"manual", "cron-a"}},
		{"by ticket", TriggerQuery{TicketID: "tk-1"}, []string{"cron-a"}},
	}
	for _, c := range cases {
		got, err := s.ListTriggers(ctx, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ids := make([]string, 0, len(got))
		for _, trig := range got {
			ids = append(ids, trig.ID)
		}
		if len(ids) != len(c.want) {
			t.Fatalf("%s: ids = %v, want %v", c.name, ids, c.want)
		}
		for i := range c.want {
			if ids[i] != c.want[i] {
				t.Fatalf("%s: ids = %v, want %v", c.name, ids, c.want)
			}
		}
	}
}

func TestMemoryStoreListTicketsFiltersAndPages(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	end := base.Add(time.Hour)

	if err := s.SaveTicket(ctx, &Ticket{ID: "open-1", State: "open", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTicket(ctx, &Ticket{ID: "open-2", State: "open", CreatedAt: base.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTicket(ctx, &Ticket{ID: "closed", State: TicketCanceled, CreatedAt: base.Add(2 * time.Minute), EndedAt: &end}); err != nil {
		t.Fatal(err)
	}

	// A state filter is exact: open does not mean "not closed".
	open, err := s.ListTickets(ctx, TicketQuery{State: "open", Limit: 10})
	if err != nil || len(open) != 2 {
		t.Fatalf("state filter = %d tickets, %v", len(open), err)
	}
	// Open() is what the router means by a live ticket.
	live, err := s.ListTickets(ctx, TicketQuery{Open: true, Limit: 10})
	if err != nil || len(live) != 2 {
		t.Fatalf("open filter = %d tickets, %v", len(live), err)
	}
	// Newest first, then windowed.
	page, err := s.ListTickets(ctx, TicketQuery{Offset: 1, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != "open-2" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if beyond, err := s.ListTickets(ctx, TicketQuery{Offset: 9, Limit: 1}); err != nil || len(beyond) != 0 {
		t.Fatalf("offset past the end = %+v, %v", beyond, err)
	}
	if missing, err := s.GetTicket(ctx, "nope"); err != nil || missing != nil {
		t.Fatalf("GetTicket(missing) = %+v, %v", missing, err)
	}
	if n, err := s.CountTickets(ctx, "open"); err != nil || n != 2 {
		t.Fatalf("CountTickets(open) = %d, %v", n, err)
	}
}

func TestMemoryStoreDecisionsAndPolicies(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, d := range []*Decision{
		{ID: "d1", TicketID: "tk-1", Kind: string(DecisionAccept), At: now},
		{ID: "d2", TicketID: "tk-2", Kind: string(DecisionIgnore), At: now},
	} {
		if err := s.AppendDecision(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	byTicket, err := s.ListDecisions(ctx, TriggerQuery{TicketID: "tk-1"})
	if err != nil || len(byTicket) != 1 || byTicket[0].ID != "d1" {
		t.Fatalf("by ticket = %+v, %v", byTicket, err)
	}
	// Decisions have no thread of their own; asking by thread must not silently
	// answer with every decision in the log.
	if byThread, err := s.ListDecisions(ctx, TriggerQuery{ThreadID: "th-1"}); err != nil || len(byThread) != 0 {
		t.Fatalf("by thread = %+v, %v", byThread, err)
	}

	if p, err := s.GetPolicy(ctx, "not-a-source"); err != nil || p != nil {
		t.Fatalf("GetPolicy on an unknown source = %+v, %v", p, err)
	}
	if _, err := s.GetTrigger(ctx, "nope"); err != nil {
		t.Fatalf("GetTrigger missing: %v", err)
	}
}

func TestMemoryStoreEventsSinceAndPrune(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	base := time.Date(2001, 1, 2, 3, 4, 5, 0, time.UTC)

	for i, subject := range []string{"th-1", "th-1", "tk-1"} {
		if err := s.AppendEvent(ctx, &Event{Kind: "trigger.appended", Subject: subject, At: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	all, err := s.EventsSince(ctx, "", 0, 0) // limit <= 0 means the default page
	if err != nil || len(all) != 3 {
		t.Fatalf("all events = %d, %v", len(all), err)
	}
	onlyThread, err := s.EventsSince(ctx, "th-1", 0, 10)
	if err != nil || len(onlyThread) != 2 {
		t.Fatalf("by subject = %d, %v", len(onlyThread), err)
	}
	after, err := s.EventsSince(ctx, "", 2, 10)
	if err != nil || len(after) != 1 || after[0].Subject != "tk-1" {
		t.Fatalf("after seq 2 = %+v, %v", after, err)
	}
	limited, err := s.EventsSince(ctx, "", 0, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limited = %d, %v", len(limited), err)
	}

	// Prune drops triggers (and their dedup keys), events and decisions older
	// than the cutoff, and nothing younger.
	if _, err := s.AppendTrigger(ctx, &Trigger{ID: "old", DedupKey: "dedup-old", DueAt: base, UpdatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendTrigger(ctx, &Trigger{ID: "new", DueAt: base.Add(48 * time.Hour), UpdatedAt: base.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendDecision(ctx, &Decision{ID: "old-decision", At: base}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Prune(ctx, base.Add(24*time.Hour), 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed == 0 {
		t.Fatal("prune removed nothing")
	}
	if trig, _ := s.GetTrigger(ctx, "new"); trig == nil {
		t.Fatal("prune removed a trigger younger than the cutoff")
	}
	if trig, _ := s.GetTrigger(ctx, "old"); trig != nil {
		t.Fatal("prune kept a trigger older than the cutoff")
	}
	// The dedup key must go with the trigger, or a redelivered webhook would be
	// swallowed by a row that no longer exists.
	if _, err := s.AppendTrigger(ctx, &Trigger{ID: "old-again", DedupKey: "dedup-old", DueAt: base}); err != nil {
		t.Fatalf("append after prune: %v", err)
	}
}
