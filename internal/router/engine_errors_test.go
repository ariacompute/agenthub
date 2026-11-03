package router

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// errStoreDown is the failure every injected store error carries. The engine
// treats a store error as fatal for the command it is serving, so each case
// below asserts the error comes back untouched rather than being swallowed.
var errStoreDown = errors.New("store is down")

// failingStore is a MemoryStore with an injectable failure per method. The
// in-memory port never fails, which leaves every error branch of the engine
// unexercised; this fake is what makes them reachable without a database.
//
// Only the methods named in the last only(...) call fail, so one test can drive
// one branch without collateral damage from the calls before it.
type failingStore struct {
	*MemoryStore
	mu     sync.Mutex
	failed map[string]bool
	err    error
}

func newFailingStore() *failingStore {
	return &failingStore{MemoryStore: NewMemoryStore(), failed: map[string]bool{}, err: errStoreDown}
}

// only resets the failure set. The method name may carry a suffix after a colon
// (CountTickets:queued) to fail one argument value out of several.
func (s *failingStore) only(methods ...string) *failingStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = make(map[string]bool, len(methods))
	for _, m := range methods {
		s.failed[m] = true
	}
	return s
}

func (s *failingStore) boom(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed[key] {
		return s.err
	}
	return nil
}

func (s *failingStore) AppendTrigger(ctx context.Context, t *Trigger) (bool, error) {
	if err := s.boom("AppendTrigger"); err != nil {
		return false, err
	}
	return s.MemoryStore.AppendTrigger(ctx, t)
}

func (s *failingStore) ClaimDueTriggers(ctx context.Context, now time.Time, limit int) ([]*Trigger, error) {
	if err := s.boom("ClaimDueTriggers"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ClaimDueTriggers(ctx, now, limit)
}

func (s *failingStore) SaveTrigger(ctx context.Context, t *Trigger) error {
	if err := s.boom("SaveTrigger"); err != nil {
		return err
	}
	return s.MemoryStore.SaveTrigger(ctx, t)
}

func (s *failingStore) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	if err := s.boom("GetTrigger"); err != nil {
		return nil, err
	}
	return s.MemoryStore.GetTrigger(ctx, id)
}

func (s *failingStore) ListTriggers(ctx context.Context, q TriggerQuery) ([]*Trigger, error) {
	if err := s.boom("ListTriggers"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListTriggers(ctx, q)
}

func (s *failingStore) CountTriggers(ctx context.Context, disposition string) (int, error) {
	if err := s.boom("CountTriggers"); err != nil {
		return 0, err
	}
	return s.MemoryStore.CountTriggers(ctx, disposition)
}

func (s *failingStore) SaveThread(ctx context.Context, th *Thread) error {
	if err := s.boom("SaveThread"); err != nil {
		return err
	}
	return s.MemoryStore.SaveThread(ctx, th)
}

func (s *failingStore) GetThread(ctx context.Context, id string) (*Thread, error) {
	if err := s.boom("GetThread"); err != nil {
		return nil, err
	}
	return s.MemoryStore.GetThread(ctx, id)
}

func (s *failingStore) SaveSubscription(ctx context.Context, sub *Subscription) error {
	if err := s.boom("SaveSubscription"); err != nil {
		return err
	}
	return s.MemoryStore.SaveSubscription(ctx, sub)
}

func (s *failingStore) DropSubscription(ctx context.Context, threadID, ticketID string) error {
	if err := s.boom("DropSubscription"); err != nil {
		return err
	}
	return s.MemoryStore.DropSubscription(ctx, threadID, ticketID)
}

func (s *failingStore) ListSubscribers(ctx context.Context, threadID string) ([]*Subscription, error) {
	if err := s.boom("ListSubscribers"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListSubscribers(ctx, threadID)
}

func (s *failingStore) ListSubscriptions(ctx context.Context, ticketID string) ([]*Subscription, error) {
	if err := s.boom("ListSubscriptions"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListSubscriptions(ctx, ticketID)
}

func (s *failingStore) CountSubscriptions(ctx context.Context) (int, error) {
	if err := s.boom("CountSubscriptions"); err != nil {
		return 0, err
	}
	return s.MemoryStore.CountSubscriptions(ctx)
}

func (s *failingStore) SaveTicket(ctx context.Context, tk *Ticket) error {
	if err := s.boom("SaveTicket"); err != nil {
		return err
	}
	return s.MemoryStore.SaveTicket(ctx, tk)
}

func (s *failingStore) GetTicket(ctx context.Context, id string) (*Ticket, error) {
	if err := s.boom("GetTicket"); err != nil {
		return nil, err
	}
	return s.MemoryStore.GetTicket(ctx, id)
}

func (s *failingStore) ListTickets(ctx context.Context, q TicketQuery) ([]*Ticket, error) {
	if err := s.boom("ListTickets"); err != nil {
		return nil, err
	}
	if err := s.boom("ListTickets:" + q.State); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListTickets(ctx, q)
}

func (s *failingStore) CountTickets(ctx context.Context, state string) (int, error) {
	if err := s.boom("CountTickets"); err != nil {
		return 0, err
	}
	if err := s.boom("CountTickets:" + state); err != nil {
		return 0, err
	}
	return s.MemoryStore.CountTickets(ctx, state)
}

func (s *failingStore) SaveAssignment(ctx context.Context, a *Assignment) error {
	if err := s.boom("SaveAssignment"); err != nil {
		return err
	}
	return s.MemoryStore.SaveAssignment(ctx, a)
}

func (s *failingStore) ListAssignments(ctx context.Context, ticketID string, limit int) ([]*Assignment, error) {
	if err := s.boom("ListAssignments"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListAssignments(ctx, ticketID, limit)
}

func (s *failingStore) AppendDecision(ctx context.Context, d *Decision) error {
	if err := s.boom("AppendDecision"); err != nil {
		return err
	}
	return s.MemoryStore.AppendDecision(ctx, d)
}

func (s *failingStore) ListDecisions(ctx context.Context, q TriggerQuery) ([]*Decision, error) {
	if err := s.boom("ListDecisions"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListDecisions(ctx, q)
}

func (s *failingStore) GetPolicy(ctx context.Context, source string) (*Policy, error) {
	if err := s.boom("GetPolicy"); err != nil {
		return nil, err
	}
	return s.MemoryStore.GetPolicy(ctx, source)
}

func (s *failingStore) ListPolicies(ctx context.Context) ([]*Policy, error) {
	if err := s.boom("ListPolicies"); err != nil {
		return nil, err
	}
	return s.MemoryStore.ListPolicies(ctx)
}

func (s *failingStore) SavePolicy(ctx context.Context, p *Policy) error {
	if err := s.boom("SavePolicy"); err != nil {
		return err
	}
	return s.MemoryStore.SavePolicy(ctx, p)
}

func (s *failingStore) AppendEvent(ctx context.Context, e *Event) error {
	if err := s.boom("AppendEvent"); err != nil {
		return err
	}
	return s.MemoryStore.AppendEvent(ctx, e)
}

func (s *failingStore) EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*Event, error) {
	if err := s.boom("EventsSince"); err != nil {
		return nil, err
	}
	return s.MemoryStore.EventsSince(ctx, subject, since, limit)
}

func (s *failingStore) MaxSeq(ctx context.Context) (uint64, error) {
	if err := s.boom("MaxSeq"); err != nil {
		return 0, err
	}
	return s.MemoryStore.MaxSeq(ctx)
}

func (s *failingStore) Prune(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := s.boom("Prune"); err != nil {
		return 0, err
	}
	return s.MemoryStore.Prune(ctx, before, limit)
}

// newErrorEngine builds an engine over the failing store, with the same
// hand-cranked clock the other engine tests use.
func newErrorEngine(t *testing.T) (*Engine, *failingStore, *clock) {
	t.Helper()
	c := &clock{at: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	st := newFailingStore()
	bus := NewBus()
	t.Cleanup(bus.Close)
	e := NewEngine(Options{Store: st, Bus: bus, Clock: c.now, Resource: "res-agent", RetryBase: time.Millisecond})
	return e, st, c
}

// TestNewEngineDefaultsToMemoryStore pins the one-liner constructor: no store
// means an in-memory one, no bus means no fan-out, and neither is an error.
func TestNewEngineDefaultsToMemoryStore(t *testing.T) {
	e := NewEngine(Options{})
	if _, ok := e.Store().(*MemoryStore); !ok {
		t.Fatalf("default store = %T, want *MemoryStore", e.Store())
	}
	if e.Bus() != nil {
		t.Fatal("a default engine must not invent a bus")
	}
	if e.Resource() != "" {
		t.Fatalf("resource = %q, want empty", e.Resource())
	}
	ctx := context.Background()
	// Publishing without a bus only writes the frame.
	if _, _, err := e.Intake(ctx, &Trigger{Source: string(SourceManual), Explicit: true}); err != nil {
		t.Fatalf("intake: %v", err)
	}
}

func TestIntakeRejectsInvalidInput(t *testing.T) {
	e, _, _ := newErrorEngine(t)
	ctx := context.Background()

	if _, _, err := e.Intake(ctx, nil); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("nil trigger err = %v, want ErrPolicyInvalid", err)
	}
	if _, _, err := e.Intake(ctx, &Trigger{Source: "telepathy"}); !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("unknown source err = %v, want ErrPolicyInvalid", err)
	}
}

func TestIntakeFillsDefaults(t *testing.T) {
	e, st, _ := newErrorEngine(t)
	ctx := context.Background()

	got, fresh, err := e.Intake(ctx, &Trigger{})
	if err != nil || !fresh {
		t.Fatalf("intake: fresh=%v err=%v", fresh, err)
	}
	if got.Source != string(SourceManual) {
		t.Fatalf("source = %q, want manual", got.Source)
	}
	if got.ActorKind != ActorSystem {
		t.Fatalf("actor kind = %q, want system", got.ActorKind)
	}
	if got.ID == "" || got.DedupKey == "" {
		t.Fatalf("id/dedup not filled: %+v", got)
	}
	if got.At.IsZero() || got.UpdatedAt.IsZero() || got.DueAt.IsZero() {
		t.Fatalf("timestamps not filled: %+v", got)
	}
	stored, err := st.GetTrigger(ctx, got.ID)
	if err != nil || stored == nil {
		t.Fatalf("trigger not stored: %v", err)
	}

	// An actor kind outside the closed set is coerced, not trusted.
	coerced, _, err := e.Intake(ctx, &Trigger{Source: string(SourceManual), ActorKind: "robot"})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if coerced.ActorKind != ActorSystem {
		t.Fatalf("actor kind = %q, want system", coerced.ActorKind)
	}
}

func TestIntakeRecordsAFilteredTrigger(t *testing.T) {
	e, st, _ := newErrorEngine(t)
	ctx := context.Background()
	st.SeedPolicy(&Policy{Source: string(SourceCron), Mode: PolicyOff, QuietStart: -1, QuietEnd: -1})

	got, fresh, err := e.Intake(ctx, &Trigger{Source: string(SourceCron), Explicit: true})
	if err != nil || !fresh {
		t.Fatalf("intake: fresh=%v err=%v", fresh, err)
	}
	if got.Disposition != string(DispositionFiltered) {
		t.Fatalf("disposition = %q, want filtered", got.Disposition)
	}
	if got.Meta["reason"] != "source_off" {
		t.Fatalf("reason = %v, want source_off", got.Meta["reason"])
	}
}

func TestIntakeDefersInsideTheQuietWindow(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	st.SeedPolicy(&Policy{
		Source:     string(SourceCron),
		Mode:       PolicyCandidate,
		QuietStart: 9 * 60,
		QuietEnd:   11 * 60,
	})

	got, _, err := e.Intake(ctx, &Trigger{Source: string(SourceCron), Explicit: true})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if got.Meta["reason"] != "quiet_window" {
		t.Fatalf("reason = %v, want quiet_window", got.Meta["reason"])
	}
	if !got.DueAt.After(c.now()) {
		t.Fatalf("due at = %v, want after %v", got.DueAt, c.now())
	}
	if got.Disposition != string(DispositionPending) {
		t.Fatalf("disposition = %q, want pending: a quiet window postpones, never drops", got.Disposition)
	}
}

func TestIntakeRoutesAHumanMessageToSubscribers(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	now := c.now()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", Own: true, At: now}); err != nil {
		t.Fatal(err)
	}

	got, _, err := e.Intake(ctx, &Trigger{
		Source:    string(SourceThread),
		ThreadID:  "th1",
		ActorKind: ActorHuman,
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if got.Disposition != string(DispositionRouted) {
		t.Fatalf("disposition = %q, want routed", got.Disposition)
	}
	if got.TicketID != "tk1" {
		t.Fatalf("ticket = %q, want tk1", got.TicketID)
	}
	if got.Meta["reason"] != "subscription" {
		t.Fatalf("reason = %v, want subscription", got.Meta["reason"])
	}
	// A running ticket is not restarted: wake only re-saves it.
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != TicketRunning {
		t.Fatalf("state = %q, want the run in flight to survive", tk.State)
	}
}

func TestIntakeSkipsClosedAndObservingSubscribers(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	now := c.now()
	// A closed ticket and a context-only subscription: neither may be woken.
	if err := st.SaveTicket(ctx, &Ticket{ID: "done1", State: TicketDone, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "obs1", State: TicketQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "done1", At: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "obs1", Context: true, At: now}); err != nil {
		t.Fatal(err)
	}

	got, _, err := e.Intake(ctx, &Trigger{Source: string(SourceThread), ThreadID: "th1", ActorKind: ActorHuman})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if got.Disposition == string(DispositionRouted) {
		t.Fatalf("a closed or observing subscriber must not route, got %+v", got)
	}
	obs, err := st.GetTicket(ctx, "obs1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != TicketQueued || obs.Focus != "" {
		t.Fatalf("observer was woken: %+v", obs)
	}

	// An agent message on a subscribed thread is delivered but not routed.
	if err := st.SaveTicket(ctx, &Ticket{ID: "live1", State: TicketQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th2", TicketID: "live1", At: now}); err != nil {
		t.Fatal(err)
	}
	agent, _, err := e.Intake(ctx, &Trigger{Source: string(SourceThread), ThreadID: "th2", ActorKind: ActorAgent})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if agent.Disposition == string(DispositionRouted) {
		t.Fatalf("an agent message must not wake a human pass-through, got %+v", agent)
	}
}

func TestIntakeSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name  string
		fail  []string
		setup func(t *testing.T, st *failingStore, c *clock)
		in    *Trigger
	}{
		{
			name: "append trigger",
			fail: []string{"AppendTrigger"},
			in:   &Trigger{Source: string(SourceManual)},
		},
		{
			name: "get policy",
			fail: []string{"GetPolicy"},
			in:   &Trigger{Source: string(SourceManual)},
		},
		{
			name: "save trigger",
			fail: []string{"SaveTrigger"},
			in:   &Trigger{Source: string(SourceManual)},
		},
		{
			name: "list subscribers",
			fail: []string{"ListSubscribers"},
			in:   &Trigger{Source: string(SourceThread), ThreadID: "th1"},
		},
		{
			name: "get ticket",
			fail: []string{"GetTicket"},
			setup: func(t *testing.T, st *failingStore, c *clock) {
				t.Helper()
				if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", At: c.now()}); err != nil {
					t.Fatal(err)
				}
			},
			in: &Trigger{Source: string(SourceThread), ThreadID: "th1", ActorKind: ActorHuman},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, c := newErrorEngine(t)
			if tc.setup != nil {
				tc.setup(t, st, c)
			}
			st.only(tc.fail...)
			if _, _, err := e.Intake(ctx, tc.in); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

// TestWakeFailureDoesNotLoseTheTrigger pins the second best-effort path: a
// ticket that cannot be woken is logged, but the trigger still ends up routed
// and stored, because the message itself was received.
func TestWakeFailureDoesNotLoseTheTrigger(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.MemoryStore.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", At: c.now()}); err != nil {
		t.Fatal(err)
	}
	st.only("SaveTicket")

	got, _, err := e.Intake(ctx, &Trigger{Source: string(SourceThread), ThreadID: "th1", ActorKind: ActorHuman})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if got.Disposition != string(DispositionRouted) || got.TicketID != "tk1" {
		t.Fatalf("trigger = %+v, want it routed anyway", got)
	}
}

// TestPublishSurvivesADeadEventLog pins the rule that the audit frame is best
// effort: losing the event must never fail the command that produced it.
func TestPublishSurvivesADeadEventLog(t *testing.T) {
	e, st, _ := newErrorEngine(t)
	ctx := context.Background()
	st.only("AppendEvent")

	got, fresh, err := e.Intake(ctx, &Trigger{Source: string(SourceManual), Explicit: true})
	if err != nil || !fresh {
		t.Fatalf("intake: fresh=%v err=%v", fresh, err)
	}
	if got == nil {
		t.Fatal("trigger lost")
	}
	if evs, err := st.MemoryStore.EventsSince(ctx, "", 0, 10); err != nil || len(evs) != 0 {
		t.Fatalf("events = %v, want none written", evs)
	}
}

func TestBindThreadRefusesUnknownAndClosedTickets(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()

	if err := e.BindThread(ctx, "tk1", "", true, false); err != nil {
		t.Fatalf("empty thread id err = %v, want nil", err)
	}
	if err := e.BindThread(ctx, "nope", "th1", true, false); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("unknown ticket err = %v, want ErrTicketNotFound", err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketDone, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.BindThread(ctx, "tk1", "th1", true, false); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("closed ticket err = %v, want ErrTicketClosed", err)
	}
}

func TestBindThreadReopensAClosedThread(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveThread(ctx, &Thread{ID: "th1", Source: string(SourceThread), Closed: true, CloseOutcome: "resolved"}); err != nil {
		t.Fatal(err)
	}

	if err := e.BindThread(ctx, "tk1", "th1", true, false); err != nil {
		t.Fatalf("bind: %v", err)
	}
	th, err := st.GetThread(ctx, "th1")
	if err != nil {
		t.Fatal(err)
	}
	if th.Closed {
		t.Fatal("new work on a closed thread must reopen it")
	}
	if th.CloseOutcome != "" || th.OpenedBy != "tk1" {
		t.Fatalf("thread not reopened cleanly: %+v", th)
	}
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Focus != "th1" {
		t.Fatalf("focus = %q, want th1", tk.Focus)
	}
}

func TestBindThreadCreatesTheThreadItCannotFind(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, Focus: "th-other", CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}

	if err := e.BindThread(ctx, "tk1", "th1", false, true); err != nil {
		t.Fatalf("bind: %v", err)
	}
	th, err := st.GetThread(ctx, "th1")
	if err != nil || th == nil {
		t.Fatalf("thread not created: %v", err)
	}
	if th.Source != string(SourceThread) {
		t.Fatalf("thread source = %q", th.Source)
	}
	// A ticket that already has a focus keeps it.
	tk, err := st.GetTicket(ctx, "tk1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Focus != "th-other" {
		t.Fatalf("focus = %q, want the original", tk.Focus)
	}
}

func TestBindThreadSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		fail  []string
		setup func(t *testing.T, st *failingStore, c *clock)
	}{
		{
			name: "get ticket",
			fail: []string{"GetTicket"},
		},
		{
			name: "get thread",
			fail: []string{"GetThread"},
			setup: func(t *testing.T, st *failingStore, c *clock) {
				t.Helper()
				if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "save thread",
			fail: []string{"SaveThread"},
			setup: func(t *testing.T, st *failingStore, c *clock) {
				t.Helper()
				if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "save subscription",
			fail: []string{"SaveSubscription"},
			setup: func(t *testing.T, st *failingStore, c *clock) {
				t.Helper()
				if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "save ticket for focus",
			fail: []string{"SaveTicket"},
			setup: func(t *testing.T, st *failingStore, c *clock) {
				t.Helper()
				if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, c := newErrorEngine(t)
			if tc.setup != nil {
				tc.setup(t, st, c)
			}
			st.only(tc.fail...)
			if err := e.BindThread(ctx, "tk1", "th1", true, false); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

func TestUnsubscribeThreadDropsEverySubscription(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	for _, id := range []string{"tk1", "tk2"} {
		if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: id, At: c.now()}); err != nil {
			t.Fatal(err)
		}
	}

	if err := e.UnsubscribeThread(ctx, "th1"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	subs, err := st.ListSubscribers(ctx, "th1")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 0 {
		t.Fatalf("subs = %d, want 0", len(subs))
	}

	st.only("ListSubscribers")
	if err := e.UnsubscribeThread(ctx, "th1"); !errors.Is(err, errStoreDown) {
		t.Fatalf("list err = %v", err)
	}
}

func TestUnsubscribeThreadSurfacesADropFailure(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveSubscription(ctx, &Subscription{ThreadID: "th1", TicketID: "tk1", At: c.now()}); err != nil {
		t.Fatal(err)
	}
	st.only("DropSubscription")
	if err := e.UnsubscribeThread(ctx, "th1"); !errors.Is(err, errStoreDown) {
		t.Fatalf("err = %v, want the store failure", err)
	}
}

func TestHandbackRequeuesOnce(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTrigger(ctx, &Trigger{ID: "trg1", Source: string(SourceManual), TicketID: "tk1", Disposition: string(DispositionRouted)}); err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("not mine ", 40)
	if err := e.Handback(ctx, "tk1", "trg1", long); err != nil {
		t.Fatalf("handback: %v", err)
	}
	trig, err := st.GetTrigger(ctx, "trg1")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Handbacks != 1 || trig.TicketID != "" || trig.Disposition != string(DispositionPending) {
		t.Fatalf("trigger not requeued: %+v", trig)
	}
	if reason, _ := trig.Meta["handbackReason"].(string); len(reason) != 200 {
		t.Fatalf("reason length = %d, want the 200 character cap", len(reason))
	}
	if trig.Meta["handbackFrom"] != "tk1" {
		t.Fatalf("handbackFrom = %v", trig.Meta["handbackFrom"])
	}

	// A second hand-back is refused: one is allowed, two would ping-pong.
	if err := e.Handback(ctx, "tk1", "trg1", ""); !errors.Is(err, ErrHandbackExhausted) {
		t.Fatalf("second handback err = %v, want ErrHandbackExhausted", err)
	}
}

func TestHandbackRejectsUnknownIds(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	if err := e.Handback(ctx, "nope", "trg1", ""); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("unknown ticket err = %v", err)
	}
	if err := st.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.Handback(ctx, "tk1", "nope", ""); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("unknown trigger err = %v", err)
	}
}

func TestHandbackSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fail []string
	}{
		{name: "get ticket", fail: []string{"GetTicket"}},
		{name: "get trigger", fail: []string{"GetTrigger"}},
		{name: "save trigger", fail: []string{"SaveTrigger"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, c := newErrorEngine(t)
			if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketRunning, CreatedAt: c.now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.MemoryStore.SaveTrigger(ctx, &Trigger{ID: "trg1", Source: string(SourceManual)}); err != nil {
				t.Fatal(err)
			}
			st.only(tc.fail...)
			if err := e.Handback(ctx, "tk1", "trg1", "why"); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

func TestNextTicketClaimsTheOldestRunnable(t *testing.T) {
	e, st, c := newErrorEngine(t)
	ctx := context.Background()
	now := c.now()
	// Backoff keeps the blocked one out of this beat.
	if err := st.SaveTicket(ctx, &Ticket{ID: "later", State: TicketQueued, NextTryAt: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.NextTicket(ctx, now); err != nil {
		t.Fatalf("next ticket: %v", err)
	}
	idle, err := st.GetTicket(ctx, "later")
	if err != nil {
		t.Fatal(err)
	}
	if idle.State != TicketQueued {
		t.Fatalf("state = %q, a ticket in backoff must not be claimed", idle.State)
	}

	if err := st.SaveTicket(ctx, &Ticket{ID: "ready", State: TicketBlocked, NextTryAt: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tk, asg, err := e.NextTicket(ctx, now)
	if err != nil {
		t.Fatalf("next ticket: %v", err)
	}
	if tk == nil || asg == nil || tk.ID != "ready" {
		t.Fatalf("claimed %+v / %+v, want the blocked ticket", tk, asg)
	}
	if tk.State != TicketRunning || tk.Attempts != 1 {
		t.Fatalf("ticket not started: %+v", tk)
	}
	if asg.State != AssignmentStarted || asg.TicketID != "ready" {
		t.Fatalf("assignment = %+v", asg)
	}
	if _, _, err := e.NextTicket(ctx, now); err != nil {
		t.Fatalf("empty queue must not be an error: %v", err)
	}
}

func TestNextTicketSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fail []string
	}{
		{name: "list queued", fail: []string{"ListTickets:queued"}},
		{name: "list blocked", fail: []string{"ListTickets:blocked"}},
		{name: "save ticket", fail: []string{"SaveTicket"}},
		{name: "save assignment", fail: []string{"SaveAssignment"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, c := newErrorEngine(t)
			if err := st.MemoryStore.SaveTicket(ctx, &Ticket{ID: "tk1", State: TicketQueued, CreatedAt: c.now()}); err != nil {
				t.Fatal(err)
			}
			st.only(tc.fail...)
			if _, _, err := e.NextTicket(ctx, c.now()); !errors.Is(err, errStoreDown) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}
