package router

import (
	"context"
	"log/slog"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/identity"
)

// TaskRunner is the slice of the hub the executor needs. The hub's *Hub value
// satisfies it as it stands, which is the point: the router never imports hub, so
// the dependency stays one way and a test can swap in a stub.
type TaskRunner interface {
	Send(ctx context.Context, params a2a.MessageSendParams) (*a2a.Task, error)
	Resume(ctx context.Context, params a2a.TaskResumeParams) (*a2a.Task, error)
	Cancel(ctx context.Context, id string) (*a2a.Task, error)
}

// ownedRunner is the optional half: a runner that can attribute a task to an
// actor. It is asserted rather than required so the interface above stays the
// minimum a caller must implement, and a stub in a test does not have to grow a
// method it does not care about.
type ownedRunner interface {
	SendAs(ctx context.Context, ownerID string, params a2a.MessageSendParams) (*a2a.Task, error)
}

// HubExecutor is the Worker layer. It turns a ticket into an ordinary hub task:
// the run then inherits the state machine, spans, artifacts, events and push
// notifications of every other task, and a human approval works through the same
// input-required park the console already shows.
//
// It never talks to a user. Everything it learns goes back onto the ticket, and
// the router decides what the person sees — which is what keeps ten concurrent
// workers from ten concurrent replies.
type HubExecutor struct {
	hub     TaskRunner
	store   Store
	history HistoryProvider
	link    ThreadLink
}

// NewHubExecutor builds the executor. history is optional and only enriches the
// brief with recent thread context; link is optional and only makes the run
// visible in the conversation it came from.
func NewHubExecutor(hub TaskRunner, store Store, history HistoryProvider, link ThreadLink) *HubExecutor {
	return &HubExecutor{hub: hub, store: store, history: history, link: link}
}

// Launch runs the ticket as a new task.
func (x *HubExecutor) Launch(ctx context.Context, t *Ticket, a *Assignment) (*AssignmentResult, error) {
	if strings.TrimSpace(t.Resource) == "" {
		return &AssignmentResult{State: AssignmentFailed, Error: ErrResourceUnset.Error()}, ErrResourceUnset
	}
	trig := &Trigger{ID: t.OriginTriggerID, Source: string(SourceManual), At: t.CreatedAt}
	if x.store != nil && t.OriginTriggerID != "" {
		if found, err := x.store.GetTrigger(ctx, t.OriginTriggerID); err == nil && found != nil {
			trig = found
		}
	}
	if a != nil {
		a.TriggerID = trig.ID
	}
	msg := a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(Brief(ctx, t, trig, x.history)))
	msg.Metadata = map[string]any{"ticket": t.ID, "router": true}
	// Attribute the run to the ticket's actor when the hub can take an owner. The
	// assertion keeps TaskRunner as small as it was — a test stub does not have to
	// grow a method it does not care about — while production gets lease reuse:
	// without an owner every ticket step lands in the anonymous path and opens its
	// own container.
	var (
		task *a2a.Task
		err  error
	)
	// The row scope travels with the call: the router launches tasks from a
	// background beat that has no HTTP request behind it, so without an explicit
	// identity the row level security policies would refuse the insert.
	if t.OwnerID != "" {
		ctx = identity.With(ctx, identity.Identity{OwnerID: t.OwnerID})
	} else {
		ctx = identity.With(ctx, identity.Identity{System: true})
	}
	// The ticket's target is named on the params metadata: that is where the hub
	// resolves the run resource. Without it the hub falls back to the first agent,
	// so a ticket routed to a specific worker would run on whatever agent sorted
	// first instead.
	params := a2a.MessageSendParams{Message: msg, Metadata: map[string]any{"resourceId": t.Resource}}
	if owned, ok := x.hub.(ownedRunner); ok && t.OwnerID != "" {
		task, err = owned.SendAs(ctx, t.OwnerID, params)
	} else {
		task, err = x.hub.Send(ctx, params)
	}
	if err == nil && task != nil && x.link != nil && t.Focus != "" {
		if lerr := x.link(ctx, t.Focus, task.ID, t.Resource); lerr != nil {
			// A missing link costs the console a cross-reference; it must not
			// cost the worker its run.
			slog.Warn("router: link task to thread", "ticket", t.ID, "task", task.ID, "error", lerr)
		}
	}
	return resultOf(task, err), err
}

// Input feeds a human answer into a parked ticket. The answer becomes the new
// input of the run; the task resumes through the same path the console uses.
func (x *HubExecutor) Input(ctx context.Context, t *Ticket, msg string) (*AssignmentResult, error) {
	if t.TaskID == "" {
		return &AssignmentResult{State: AssignmentFailed, Error: "no_task"}, nil
	}
	answer := a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(ReplyBrief(t, msg)))
	answer.Metadata = map[string]any{"ticket": t.ID, "router": true}
	task, err := x.hub.Resume(ctx, a2a.TaskResumeParams{ID: t.TaskID, Message: answer})
	return resultOf(task, err), err
}

// Cancel stops the ticket's latest task.
func (x *HubExecutor) Cancel(ctx context.Context, t *Ticket) error {
	if t.TaskID == "" {
		return nil
	}
	_, err := x.hub.Cancel(ctx, t.TaskID)
	return err
}

// resultOf maps the A2A task state onto what the ticket needs to know.
func resultOf(task *a2a.Task, err error) *AssignmentResult {
	if err != nil {
		return &AssignmentResult{State: AssignmentFailed, Error: truncate(err.Error(), 400)}
	}
	if task == nil {
		return &AssignmentResult{State: AssignmentFailed, Error: "no_task"}
	}
	res := &AssignmentResult{TaskID: task.ID}
	switch task.Status.State {
	case a2a.TaskStateCompleted:
		res.State = AssignmentReported
	case a2a.TaskStateInputRequired:
		res.State = AssignmentWaiting
		res.WaitsForPerson = true
		if task.Status.Message != nil {
			res.Note = truncate(task.Status.Message.Text(), 400)
		}
	case a2a.TaskStateCanceled:
		res.State = AssignmentCanceled
	case a2a.TaskStateFailed:
		res.State = AssignmentFailed
		if task.Status.Message != nil {
			res.Error = truncate(task.Status.Message.Text(), 400)
		}
	default:
		// working / submitted handed back synchronously means the run finished
		// without a terminal state; treat it as reported rather than losing it.
		res.State = AssignmentReported
	}
	return res
}
