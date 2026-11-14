package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Engine is the single writer of every router entity. All its methods serialise
// on one mutex, which is what makes "one writer" true in a single process: no
// caller ever observes a half applied decision.
//
// The methods are commands (they can be refused) or facts reported by an adapter
// (they cannot be refused, only recorded). Nothing here does I/O outside the
// store: launching work is the beat's job, after the state is committed.
type Engine struct {
	mu     sync.Mutex
	store  Store
	assess Assessor
	bus    *Bus
	opts   Options
}

// NewEngine builds an engine. A nil store becomes an empty in-memory one so a
// test can construct an engine in one line.
func NewEngine(o Options) *Engine {
	o.normalize()
	if o.Store == nil {
		o.Store = NewMemoryStore()
	}
	return &Engine{store: o.Store, assess: o.Assessor, bus: o.Bus, opts: o}
}

// Store exposes the persistence port, for the service facade.
func (e *Engine) Store() Store { return e.store }

// Bus exposes the event fan-out.
func (e *Engine) Bus() *Bus { return e.bus }

// Clock returns the engine's time source.
func (e *Engine) Clock() Clock { return e.opts.Clock }

// Resource returns the default worker a ticket runs on.
func (e *Engine) Resource() string { return e.opts.Resource }

func (e *Engine) now() time.Time { return e.opts.Clock() }

func (e *Engine) publish(ctx context.Context, kind, subject string, payload map[string]any) {
	ev := &Event{Kind: kind, Subject: subject, At: e.now(), Payload: payload}
	if err := e.store.AppendEvent(ctx, ev); err != nil {
		// An event is best effort: losing the audit frame must never fail the
		// command that produced it.
		slog.Warn("router: append event", "kind", kind, "subject", subject, "error", err)
	}
	if e.bus != nil {
		e.bus.Publish(ev)
	}
}

// Intake normalises and stores one trigger, then decides what to do with it
// before any evaluation runs.
//
// The order is deliberate. The row is inserted first so the dedup key is checked
// atomically: a redelivered webhook appends nothing and therefore wakes nothing.
// Only a fresh trigger fans out to subscribers, and only a trigger that did not
// reach a subscriber goes through the program layer.
func (e *Engine) Intake(ctx context.Context, in *Trigger) (*Trigger, bool, error) {
	if in == nil {
		return nil, false, ErrPolicyInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	t := *in
	if t.Source == "" {
		t.Source = string(SourceManual)
	}
	if NormalizeSource(t.Source) == "" {
		return nil, false, ErrPolicyInvalid
	}
	if t.ActorKind == "" {
		t.ActorKind = ActorSystem
	}
	if t.ActorKind != ActorHuman && t.ActorKind != ActorAgent && t.ActorKind != ActorSystem {
		t.ActorKind = ActorSystem
	}
	if t.ID == "" {
		t.ID = newID("trg")
	}
	if t.At.IsZero() {
		t.At = now
	}
	t.UpdatedAt = now
	if t.DueAt.IsZero() {
		t.DueAt = now
	}
	if t.DedupKey == "" {
		t.DedupKey = defaultDedupKey(&t)
	}
	if t.Disposition == "" {
		t.Disposition = string(DispositionPending)
	}

	fresh, err := e.store.AppendTrigger(ctx, &t)
	if err != nil {
		return nil, false, err
	}
	if !fresh {
		return nil, false, nil
	}

	delivered, _, ferr := e.fanOut(ctx, &t, now)
	if ferr != nil {
		return nil, true, ferr
	}

	if t.Disposition == string(DispositionPending) {
		p, perr := e.store.GetPolicy(ctx, t.Source)
		if perr != nil {
			return nil, true, perr
		}
		v := Filter(p, &t, now)
		switch {
		case !v.Admit:
			t.Disposition = string(DispositionFiltered)
			t.Meta = withMeta(t.Meta, "reason", v.Reason)
		case !v.Defer.IsZero():
			t.DueAt = v.Defer
			t.Meta = withMeta(t.Meta, "reason", v.Reason)
		default:
			t.DueAt = now
			t.Meta = withMeta(t.Meta, "reason", v.Reason)
		}
	}

	t.UpdatedAt = now
	if err := e.store.SaveTrigger(ctx, &t); err != nil {
		return nil, true, err
	}
	e.publish(ctx, EventTriggerReceived, t.ID, map[string]any{
		"source":      t.Source,
		"disposition": t.Disposition,
		"thread":      t.ThreadID,
		"actor":       t.ActorKind,
		"delivered":   delivered,
	})
	return &t, true, nil
}

// fanOut routes a thread trigger to every ticket subscribed to that thread.
//
// A message from a person wakes the subscribed tickets directly and never reaches
// the evaluation: this is the pass-through, and it is what keeps the router from
// being the bottleneck on an established thread. A message that only reaches
// context subscribers is delivered but still evaluated.
func (e *Engine) fanOut(ctx context.Context, t *Trigger, now time.Time) (int, bool, error) {
	if t.ThreadID == "" {
		return 0, false, nil
	}
	subs, err := e.store.ListSubscribers(ctx, t.ThreadID)
	if err != nil {
		return 0, false, err
	}
	delivered := 0
	human := false
	primary := ""
	for _, sub := range subs {
		tk, gerr := e.store.GetTicket(ctx, sub.TicketID)
		if gerr != nil {
			return delivered, human, gerr
		}
		if tk == nil || !tk.Open() {
			continue
		}
		delivered++
		if sub.Context {
			continue
		}
		if primary == "" {
			primary = tk.ID
		}
		if !t.Human() {
			continue
		}
		// An explicit @mention names the worker the person wants. A ticket that
		// already owns this thread may have been opened for a different one;
		// waking it unchanged would answer from the old worker. Re-target it
		// before wake saves the row, so the pass-through keeps one ticket per
		// conversation and still runs on the agent that was addressed.
		mentioned, _ := t.Meta["mention"].(bool)
		if resource := strings.TrimSpace(metaString(t.Meta, "resource")); mentioned && resource != "" && resource != tk.Resource {
			tk.Resource = resource
		}
		human = true
		e.wake(ctx, tk, t, now)
	}
	if primary != "" {
		t.TicketID = primary
	}
	if human {
		t.Disposition = string(DispositionRouted)
		t.DueAt = now
		t.Meta = withMeta(t.Meta, "reason", "subscription")
	}
	return delivered, human, nil
}

// wake makes a subscribed ticket pick up a human message. A run already in flight
// is not restarted: the follow-up is collected and the ticket requeues itself
// when that assignment reports.
func (e *Engine) wake(ctx context.Context, tk *Ticket, t *Trigger, now time.Time) {
	tk.WaitsForPerson = false
	tk.UpdatedAt = now
	if tk.State == TicketRunning {
		_ = e.store.SaveTicket(ctx, tk)
		return
	}
	tk.State = TicketQueued
	tk.NextTryAt = now
	tk.LastError = ""
	if tk.Focus == "" {
		tk.Focus = t.ThreadID
	}
	if err := e.store.SaveTicket(ctx, tk); err != nil {
		slog.Warn("router: wake ticket", "ticket", tk.ID, "error", err)
		return
	}
	e.publish(ctx, EventTicketResumed, tk.ID, map[string]any{"trigger": t.ID})
}

// BindThread subscribes a ticket to a thread. Binding a thread that was closed
// reopens it, because new work on it means it was not finished after all.
func (e *Engine) BindThread(ctx context.Context, ticketID, threadID string, own, contextOnly bool) error {
	if threadID == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bindThread(ctx, ticketID, threadID, own, contextOnly)
}

// bindThread is the body of BindThread, for callers that already hold the lock.
func (e *Engine) bindThread(ctx context.Context, ticketID, threadID string, own, contextOnly bool) error {
	tk, err := e.store.GetTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	if tk == nil {
		return ErrTicketNotFound
	}
	if !tk.Open() {
		return ErrTicketClosed
	}
	now := e.now()
	th, err := e.store.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	if th == nil {
		th = &Thread{ID: threadID, Source: string(SourceThread)}
	}
	if th.Closed {
		th.Closed = false
		th.CloseOutcome = ""
	}
	if own {
		th.OpenedBy = ticketID
	}
	th.UpdatedAt = now
	if err := e.store.SaveThread(ctx, th); err != nil {
		return err
	}
	sub := &Subscription{
		ThreadID: threadID,
		TicketID: ticketID,
		Own:      own,
		Context:  contextOnly,
		At:       now,
	}
	if err := e.store.SaveSubscription(ctx, sub); err != nil {
		return err
	}
	if tk.Focus == "" {
		tk.Focus = threadID
		tk.UpdatedAt = now
		if err := e.store.SaveTicket(ctx, tk); err != nil {
			return err
		}
	}
	e.publish(ctx, EventThreadSubscribe, threadID, map[string]any{
		"ticket": ticketID, "own": own, "context": contextOnly,
	})
	return nil
}

// UnsubscribeThread drops every subscription on a thread. The tickets that held
// them keep whatever they already received; they simply stop seeing new events.
func (e *Engine) UnsubscribeThread(ctx context.Context, threadID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	subs, err := e.store.ListSubscribers(ctx, threadID)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if err := e.store.DropSubscription(ctx, threadID, sub.TicketID); err != nil {
			return err
		}
	}
	e.publish(ctx, EventThreadDrop, threadID, map[string]any{"dropped": len(subs)})
	return nil
}

// Handback lets a worker say "this trigger is not mine". Each trigger may be
// handed back once; it then returns to the queue, and the evaluation is barred
// from attaching it to the same ticket again, which is what stops a ping-pong.
func (e *Engine) Handback(ctx context.Context, ticketID, triggerID, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	tk, err := e.store.GetTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	if tk == nil {
		return ErrTicketNotFound
	}
	trig, err := e.store.GetTrigger(ctx, triggerID)
	if err != nil {
		return err
	}
	if trig == nil {
		return ErrTicketNotFound
	}
	if trig.Handbacks >= 1 {
		return ErrHandbackExhausted
	}
	now := e.now()
	trig.Handbacks++
	trig.Disposition = string(DispositionPending)
	trig.TicketID = ""
	trig.DueAt = now
	trig.UpdatedAt = now
	trig.Meta = withMeta(trig.Meta, "handbackFrom", ticketID)
	if reason != "" {
		trig.Meta = withMeta(trig.Meta, "handbackReason", truncate(reason, 200))
	}
	if err := e.store.SaveTrigger(ctx, trig); err != nil {
		return err
	}
	e.publish(ctx, EventTriggerRequeued, trig.ID, map[string]any{"ticket": ticketID, "reason": reason})
	return nil
}

// NextTicket claims the oldest runnable ticket and opens an assignment for it.
// It returns nil when nothing is runnable, which is the common case.
func (e *Engine) NextTicket(ctx context.Context, now time.Time) (*Ticket, *Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Both states are runnable: queued work that never started, and work that
	// failed once and is waiting out its backoff.
	queued, err := e.store.ListTickets(ctx, TicketQuery{State: TicketQueued, Limit: 100})
	if err != nil {
		return nil, nil, err
	}
	blocked, err := e.store.ListTickets(ctx, TicketQuery{State: TicketBlocked, Limit: 100})
	if err != nil {
		return nil, nil, err
	}
	list := append(queued, blocked...)
	for _, tk := range list {
		if tk.NextTryAt.After(now) {
			continue
		}
		tk.State = TicketRunning
		tk.Attempts++
		tk.UpdatedAt = now
		if err := e.store.SaveTicket(ctx, tk); err != nil {
			return nil, nil, err
		}
		a := &Assignment{
			ID:        newID("asg"),
			TicketID:  tk.ID,
			State:     AssignmentStarted,
			TriggerID: tk.OriginTriggerID,
			StartedAt: now,
		}
		if err := e.store.SaveAssignment(ctx, a); err != nil {
			return nil, nil, err
		}
		e.publish(ctx, EventAssignmentStart, tk.ID, map[string]any{
			"assignment": a.ID, "attempt": tk.Attempts, "resource": tk.Resource,
		})
		return tk, a, nil
	}
	return nil, nil, nil
}

// RecordAssignment writes what the executor reported and moves the ticket
// accordingly. This is a fact, not a command: it is always recorded.
func (e *Engine) RecordAssignment(ctx context.Context, ticketID string, a *Assignment, res *AssignmentResult) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	if a != nil {
		end := now
		a.EndedAt = &end
		if res.State != "" {
			a.State = res.State
		}
		a.TaskID = res.TaskID
		a.Note = truncate(res.Note, 400)
		a.Error = truncate(res.Error, 400)
		if err := e.store.SaveAssignment(ctx, a); err != nil {
			return err
		}
	}

	tk, err := e.store.GetTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	if tk == nil {
		return ErrTicketNotFound
	}
	tk.UpdatedAt = now
	if res.TaskID != "" {
		tk.TaskID = res.TaskID
	}

	switch res.State {
	case AssignmentWaiting:
		tk.State = TicketWaiting
		tk.WaitsForPerson = true
	case AssignmentReported:
		tk.WaitsForPerson = false
		if e.hasFollowUp(ctx, tk, a) {
			tk.State = TicketQueued
			tk.NextTryAt = now
			e.publish(ctx, EventTicketResumed, tk.ID, map[string]any{"reason": "follow_up"})
			break
		}
		if e.followsDone(ctx, tk.ID) {
			return e.endTicket(ctx, tk, TicketDone)
		}
		// Open work is over but the ticket still follows live threads: it goes
		// idle and waits for the next event instead of relaunching itself.
		tk.State = TicketIdle
	case AssignmentFailed:
		tk.LastError = truncate(res.Error, 400)
		if tk.Attempts >= e.maxAttempts() {
			return e.endTicket(ctx, tk, TicketFailed)
		}
		tk.State = TicketBlocked
		tk.NextTryAt = now.Add(e.backoff(tk.Attempts))
	case AssignmentCanceled:
		return e.endTicket(ctx, tk, TicketCanceled)
	default:
		tk.State = TicketIdle
	}
	if err := e.store.SaveTicket(ctx, tk); err != nil {
		return err
	}
	e.publish(ctx, EventAssignmentEnd, tk.ID, map[string]any{"state": res.State, "error": res.Error})
	return nil
}

// CancelTicket stops a ticket and its thread ownership.
func (e *Engine) CancelTicket(ctx context.Context, ticketID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	tk, err := e.store.GetTicket(ctx, ticketID)
	if err != nil {
		return err
	}
	if tk == nil {
		return ErrTicketNotFound
	}
	if !tk.Open() {
		return ErrTicketClosed
	}
	return e.endTicket(ctx, tk, TicketCanceled)
}

// CloseThread records that a thread is finished and ends every ticket whose open
// work depended on it. A ticket ends here, not when a worker says it is ready:
// the conversation is the source of truth.
func (e *Engine) CloseThread(ctx context.Context, threadID, outcome string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	th, err := e.store.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	if th == nil {
		return ErrThreadNotFound
	}
	now := e.now()
	th.Closed = true
	th.CloseOutcome = outcome
	th.UpdatedAt = now
	if err := e.store.SaveThread(ctx, th); err != nil {
		return err
	}

	subs, err := e.store.ListSubscribers(ctx, threadID)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		sub.Closed = true
		if err := e.store.SaveSubscription(ctx, sub); err != nil {
			return err
		}
		tk, gerr := e.store.GetTicket(ctx, sub.TicketID)
		if gerr != nil {
			return gerr
		}
		if tk == nil || !tk.Open() {
			continue
		}
		if !e.followsDone(ctx, tk.ID) {
			continue
		}
		if err := e.endTicket(ctx, tk, TicketDone); err != nil {
			return err
		}
	}
	return nil
}

// endTicket closes a ticket, returning its unread follow-ups to the evaluation.
func (e *Engine) endTicket(ctx context.Context, tk *Ticket, state string) error {
	now := e.now()
	tk.State = state
	tk.WaitsForPerson = false
	tk.UpdatedAt = now
	tk.EndedAt = &now
	if err := e.store.SaveTicket(ctx, tk); err != nil {
		return err
	}
	// Follow-ups that arrived after the last run finished belong to nobody now
	// and return to the queue. This only applies to a ticket that finished its
	// work: a ticket a person cancelled or one that failed for good must not
	// reopen the very thing that was stopped.
	rows, err := e.store.ListTriggers(ctx, TriggerQuery{TicketID: tk.ID, Limit: 200})
	if err == nil && state == TicketDone {
		cutoff := now
		if rows := e.lastAssignmentEnd(ctx, tk.ID); !rows.IsZero() {
			cutoff = rows
		}
		for _, trig := range rows {
			if trig.Disposition != string(DispositionRouted) || trig.Handbacks >= 1 {
				continue
			}
			// Strictly after: a trigger that arrived at the same instant the run
			// ended was already part of that run, and treating it as unread would
			// reopen the ticket forever. Two clocks with the same resolution make
			// that coincidence common, not rare.
			if !trig.At.After(cutoff) {
				continue
			}
			trig.Disposition = string(DispositionPending)
			trig.TicketID = ""
			trig.DueAt = now
			trig.UpdatedAt = now
			if err := e.store.SaveTrigger(ctx, trig); err != nil {
				return err
			}
			e.publish(ctx, EventTriggerRequeued, trig.ID, map[string]any{"ticket": tk.ID, "reason": "ticket_ended"})
		}
	}
	e.publish(ctx, EventTicketEnded, tk.ID, map[string]any{"state": state})
	return nil
}

// hasFollowUp reports whether a trigger arrived at the ticket while the given
// assignment was running. That is what makes the ticket run again instead of
// going idle.
func (e *Engine) hasFollowUp(ctx context.Context, tk *Ticket, a *Assignment) bool {
	if a == nil {
		return false
	}
	rows, err := e.store.ListTriggers(ctx, TriggerQuery{TicketID: tk.ID, Limit: 50})
	if err != nil {
		return false
	}
	for _, trig := range rows {
		if trig.At.After(a.StartedAt) && trig.Disposition == string(DispositionRouted) {
			return true
		}
	}
	return false
}

func (e *Engine) lastAssignmentEnd(ctx context.Context, ticketID string) time.Time {
	rows, err := e.store.ListAssignments(ctx, ticketID, 1)
	if err != nil || len(rows) == 0 || rows[0].EndedAt == nil {
		return time.Time{}
	}
	return *rows[0].EndedAt
}

// followsDone applies the closing rule: a ticket that opened threads ends when
// all of them are closed; a ticket that opened none ends when everything it
// followed is closed; a ticket with no subscriptions ends as soon as its work is
// reported.
func (e *Engine) followsDone(ctx context.Context, ticketID string) bool {
	subs, err := e.store.ListSubscriptions(ctx, ticketID)
	if err != nil || len(subs) == 0 {
		return true
	}
	owns := false
	for _, sub := range subs {
		if sub.Own {
			owns = true
			break
		}
	}
	for _, sub := range subs {
		if !sub.Own && owns {
			continue
		}
		th, gerr := e.store.GetThread(ctx, sub.ThreadID)
		if gerr != nil {
			return false
		}
		if th != nil && !th.Closed {
			return false
		}
	}
	return true
}

func (e *Engine) maxAttempts() int {
	if e.opts.MaxAttempts > 0 {
		return e.opts.MaxAttempts
	}
	return 3
}

func (e *Engine) backoff(attempt int) time.Duration {
	base := e.opts.RetryBase
	if base <= 0 {
		base = 5 * time.Second
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d > 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return d
}

// Prune drops triggers, decisions and events older than before.
func (e *Engine) Prune(ctx context.Context, before time.Time, limit int) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.Prune(ctx, before, limit)
}

// ListTriggers returns the trigger stream, newest first.
func (e *Engine) ListTriggers(ctx context.Context, q TriggerQuery) ([]*Trigger, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.ListTriggers(ctx, q)
}

// GetTrigger returns one trigger, or nil.
func (e *Engine) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.GetTrigger(ctx, id)
}

// ListTickets returns tickets, newest first.
func (e *Engine) ListTickets(ctx context.Context, q TicketQuery) ([]*Ticket, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.ListTickets(ctx, q)
}

// GetTicket returns a ticket with everything that explains it.
func (e *Engine) GetTicket(ctx context.Context, id string) (*TicketDetail, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	tk, err := e.store.GetTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	if tk == nil {
		return nil, ErrTicketNotFound
	}
	subs, err := e.store.ListSubscriptions(ctx, id)
	if err != nil {
		return nil, err
	}
	assignments, err := e.store.ListAssignments(ctx, id, 50)
	if err != nil {
		return nil, err
	}
	decisions, err := e.store.ListDecisions(ctx, TriggerQuery{TicketID: id, Limit: 50})
	if err != nil {
		return nil, err
	}
	detail := &TicketDetail{
		Ticket:        tk,
		Subscriptions: subs,
		Assignments:   assignments,
		Decisions:     decisions,
	}
	if tk.OriginTriggerID != "" {
		if origin, oerr := e.store.GetTrigger(ctx, tk.OriginTriggerID); oerr == nil {
			detail.Origin = origin
		}
	}
	return detail, nil
}

// Policy returns one source's program layer configuration.
func (e *Engine) Policy(ctx context.Context, source string) (*Policy, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.GetPolicy(ctx, source)
}

// Policies returns every policy, seeded with the defaults for sources an older
// database has no row for.
func (e *Engine) Policies(ctx context.Context) ([]*Policy, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	stored, err := e.store.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(stored))
	for _, p := range stored {
		seen[p.Source] = true
	}
	out := stored
	for _, d := range DefaultPolicies() {
		if !seen[d.Source] {
			out = append(out, d)
		}
	}
	return out, nil
}

// SavePolicy validates and stores one policy, then announces it so a running
// console picks the change up without a refresh.
func (e *Engine) SavePolicy(ctx context.Context, p *Policy) (*Policy, error) {
	if p == nil {
		return nil, ErrPolicyInvalid
	}
	cp := *p
	cp.Normalize()
	if err := cp.Validate(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cp.UpdatedAt = e.now()
	if err := e.store.SavePolicy(ctx, &cp); err != nil {
		return nil, err
	}
	e.publish(ctx, EventPolicyChanged, cp.Source, map[string]any{"mode": cp.Mode})
	return &cp, nil
}

// Stats is the water level the console shows above the trigger stream.
func (e *Engine) Stats(ctx context.Context, live bool) (Stats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var s Stats
	var err error
	if s.Pending, err = e.store.CountTriggers(ctx, string(DispositionPending)); err != nil {
		return s, err
	}
	if s.Queued, err = e.store.CountTickets(ctx, TicketQueued); err != nil {
		return s, err
	}
	if s.RunningTickets, err = e.store.CountTickets(ctx, TicketRunning); err != nil {
		return s, err
	}
	if s.Waiting, err = e.store.CountTickets(ctx, TicketWaiting); err != nil {
		return s, err
	}
	if s.Subscribers, err = e.store.CountSubscriptions(ctx); err != nil {
		return s, err
	}
	s.Live = live
	if e.bus != nil {
		s.Streams = e.bus.SubscriberCount()
	}
	return s, nil
}

// EventsSince replays committed frames after seq.
func (e *Engine) EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.EventsSince(ctx, subject, since, limit)
}

// newID returns a short random identifier with a readable prefix, so a log line
// says whether it is talking about a trigger or a ticket.
func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf)
}

func defaultDedupKey(t *Trigger) string {
	if t.SourceID != "" {
		return t.Source + ":" + t.SourceID + ":" + t.At.UTC().Format(time.RFC3339)
	}
	return t.Source + ":" + t.ID
}

func withMeta(m map[string]any, key string, value any) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	m[key] = value
	return m
}
