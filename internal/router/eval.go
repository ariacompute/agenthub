package router

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// EvaluateDue is one beat of the router layer: claim what is due, decide the
// whole batch in one call, then apply each verdict.
//
// The batch is the point. Under load the beat accumulates triggers while the
// previous decision is still running, and one evaluation covers all of them —
// which is why a burst costs less per decision than a drip. The batch id ties the
// decisions of one beat together so the console can show it.
func (e *Engine) EvaluateDue(ctx context.Context, now time.Time) (int, error) {
	// The claim and each apply are separate critical sections on purpose: a
	// semantic assessment can take seconds, and holding the writer lock for its
	// whole duration would block exactly the human messages the pass-through
	// exists to admit.
	batch, err := e.claim(ctx, now)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}

	batchID := newID("batch")
	e.publish(ctx, EventEvalStarted, batchID, map[string]any{"size": len(batch)})

	decisions := e.decide(ctx, batch)
	applied := 0
	for i, trig := range batch {
		if err := e.applyLocked(ctx, trig, decisions[i], batchID, now); err != nil {
			slog.Warn("router: apply decision", "trigger", trig.ID, "error", err)
			continue
		}
		applied++
	}
	return applied, nil
}

// claim takes the due batch under the writer lock.
func (e *Engine) claim(ctx context.Context, now time.Time) ([]*Trigger, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.ClaimDueTriggers(ctx, now, e.opts.BatchMax)
}

// applyLocked applies one verdict as a single commit.
func (e *Engine) applyLocked(ctx context.Context, t *Trigger, d Decision, batchID string, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.apply(ctx, t, d, batchID, now)
}

// decide runs the two level filter: the program layer settles what it can, and
// the semantic layer is consulted for the triggers it deliberately left open
// ("implicit"). A verdict from the model can only upgrade an open question — it
// can never overturn a rule that said no, because the rules encode the operator's
// boundary and a model must not widen it.
func (e *Engine) decide(ctx context.Context, batch []*Trigger) []Decision {
	out := make([]Decision, len(batch))
	for i, trig := range batch {
		out[i] = e.ruleDecision(ctx, trig)
	}
	if e.assess == nil {
		return out
	}
	res, err := e.assess.Assess(ctx, batch)
	if err != nil {
		// A failed assessment is not an outage: the rules already answered for
		// every trigger, so the beat simply loses the upgrade it hoped for.
		slog.Warn("router: semantic assessment failed, keeping the rule verdicts", "error", err)
		return out
	}
	for i := range out {
		if i >= len(res) || out[i].Reason != "implicit" {
			continue
		}
		d := res[i]
		kind := NormalizeDecisionKind(string(d.Kind))
		if kind == "" || kind == DecisionUnavailable {
			continue
		}
		d.Kind = string(kind)
		d.By = "model"
		if d.Reason == "" {
			d.Reason = "model"
		}
		out[i] = d
	}
	return out
}

// ruleDecision is the fast path's verdict for one trigger.
func (e *Engine) ruleDecision(ctx context.Context, t *Trigger) Decision {
	// A ticket that already owns the thread wins: relay to it when the message
	// is addressed at the router, otherwise leave it to that ticket.
	if t.ThreadID != "" {
		if tk := e.ownerOf(ctx, t.ThreadID); tk != nil && tk.ID != metaString(t.Meta, "handbackFrom") {
			if t.Human() || t.Explicit {
				return Decision{Kind: string(DecisionAttach), TicketID: tk.ID, By: "rule", Reason: "thread_owner"}
			}
			return Decision{Kind: string(DecisionStop), TicketID: tk.ID, By: "rule", Reason: "thread_owner"}
		}
	}
	switch Source(t.Source) {
	case SourceCron, SourceManual:
		// Both are explicit by construction: a schedule that came due and a
		// person pressing run are not noise.
		return Decision{Kind: string(DecisionAccept), By: "rule", Reason: "scheduled"}
	}
	if t.Explicit {
		return Decision{Kind: string(DecisionAccept), By: "rule", Reason: "explicit_request"}
	}
	if p, _ := e.store.GetPolicy(ctx, t.Source); p != nil && explicitOrKeyword(p, t) {
		return Decision{Kind: string(DecisionAccept), By: "rule", Reason: "keyword"}
	}
	// Nothing rules this in or out: that is exactly the case the semantic layer
	// exists for.
	return Decision{Kind: string(DecisionIgnore), By: "rule", Reason: "implicit"}
}

// apply writes one verdict and its consequences.
func (e *Engine) apply(ctx context.Context, t *Trigger, d Decision, batchID string, now time.Time) error {
	if d.ID == "" {
		d.ID = newID("dec")
	}
	d.TriggerID = t.ID
	d.BatchID = batchID
	d.At = now
	if d.By == "" {
		d.By = "rule"
	}

	kind := NormalizeDecisionKind(d.Kind)
	switch kind {
	case DecisionAccept:
		tk, err := e.createTicket(ctx, t, d, batchID, now)
		if err != nil {
			return err
		}
		d.TicketID = tk.ID
	case DecisionAttach:
		tk, err := e.store.GetTicket(ctx, d.TicketID)
		if err != nil {
			return err
		}
		if tk == nil || !tk.Open() {
			// The owner vanished between the claim and the apply: open a new
			// ticket rather than dropping work on the floor.
			tk, err = e.createTicket(ctx, t, d, batchID, now)
			if err != nil {
				return err
			}
			d.TicketID = tk.ID
			kind = DecisionAccept
		} else {
			if t.ThreadID != "" {
				if err := e.BindThreadLocked(ctx, tk.ID, t.ThreadID, false, !t.Human()); err != nil {
					return err
				}
			}
			if t.Human() {
				e.wake(ctx, tk, t, now)
			}
		}
	case DecisionStop, DecisionIgnore, DecisionUnavailable:
		// Nothing to do beyond recording the verdict.
	default:
		return ErrPolicyInvalid
	}

	t.Disposition = string(kind.Disposition())
	t.DecisionID = d.ID
	t.TicketID = d.TicketID
	t.UpdatedAt = now
	if err := e.store.SaveTrigger(ctx, t); err != nil {
		return err
	}
	if err := e.store.AppendDecision(ctx, &d); err != nil {
		return err
	}
	payload := map[string]any{"kind": string(kind), "by": d.By, "reason": d.Reason}
	if d.TicketID != "" {
		payload["ticket"] = d.TicketID
	}
	e.publish(ctx, EventDecision, t.ID, payload)
	return nil
}

// createTicket opens a ticket for a trigger and subscribes it to the trigger's
// thread. A ticket that opened a thread is the one that ends when it closes.
func (e *Engine) createTicket(ctx context.Context, t *Trigger, d Decision, batchID string, now time.Time) (*Ticket, error) {
	// Resolution order: the verdict, then the conversation's own preference (a
	// mention names an agent), then the source policy, then the instance default.
	// A model may suggest a worker but never invent one that is not registered
	// somewhere in this chain.
	resource := strings.TrimSpace(metaString(t.Meta, "resource"))
	if resource == "" {
		resource = strings.TrimSpace(d.Resource)
	}
	if resource == "" {
		if p, _ := e.store.GetPolicy(ctx, t.Source); p != nil {
			resource = strings.TrimSpace(p.Resource)
		}
	}
	if resource == "" {
		resource = e.opts.Resource
	}
	tk := &Ticket{
		ID:              newID("tkt"),
		Title:           titleOf(t),
		Goal:            goalOf(t),
		OwnerID:         t.ActorID,
		State:           TicketQueued,
		Resource:        resource,
		OriginTriggerID: t.ID,
		BatchID:         batchID,
		Focus:           t.ThreadID,
		CreatedAt:       now,
		UpdatedAt:       now,
		NextTryAt:       now,
	}
	if err := e.store.SaveTicket(ctx, tk); err != nil {
		return nil, err
	}
	if t.ThreadID != "" {
		if err := e.BindThreadLocked(ctx, tk.ID, t.ThreadID, true, false); err != nil {
			return nil, err
		}
	}
	e.publish(ctx, EventTicketCreated, tk.ID, map[string]any{
		"trigger": t.ID, "goal": tk.Goal, "resource": tk.Resource, "batch": batchID,
	})
	return tk, nil
}

// BindThreadLocked is BindThread for a caller that already holds the engine lock.
func (e *Engine) BindThreadLocked(ctx context.Context, ticketID, threadID string, own, contextOnly bool) error {
	return e.bindThread(ctx, ticketID, threadID, own, contextOnly)
}

// ownerOf returns the open ticket that owns a thread, if any.
func (e *Engine) ownerOf(ctx context.Context, threadID string) *Ticket {
	subs, err := e.store.ListSubscribers(ctx, threadID)
	if err != nil {
		return nil
	}
	for _, sub := range subs {
		if !sub.Own || sub.Context {
			continue
		}
		tk, gerr := e.store.GetTicket(ctx, sub.TicketID)
		if gerr == nil && tk != nil && tk.Open() {
			return tk
		}
	}
	return nil
}

// titleOf is the one line the console shows in a ticket list.
func titleOf(t *Trigger) string {
	if v := strings.TrimSpace(metaString(t.Meta, "goal")); v != "" {
		return truncate(v, 80)
	}
	if v := strings.TrimSpace(t.Body); v != "" {
		return truncate(v, 80)
	}
	if t.SourceID != "" {
		return truncate(t.Source+" "+t.SourceID, 80)
	}
	return truncate(t.Source+" trigger", 80)
}

// goalOf is what the worker is asked to do. A cron trigger carries its schedule
// goal in the metadata; anything else falls back to the body.
func goalOf(t *Trigger) string {
	if v := strings.TrimSpace(metaString(t.Meta, "goal")); v != "" {
		return v
	}
	if v := strings.TrimSpace(t.Body); v != "" {
		return v
	}
	return titleOf(t)
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
