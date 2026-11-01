package router

import (
	"context"
	"time"
)

// Store persists the router's state. The port is defined here, in the domain
// package, and implemented twice: in memory (memory.go, what the unit tests run
// on) and over PostgreSQL (internal/store). Both must behave the same, which is
// what the integration test pins.
//
// There is one writer — Engine — so the port carries plain reads and writes
// rather than conditional updates: nothing else competes for the rows.
type Store interface {
	// AppendTrigger stores one trigger and reports whether it was fresh. A
	// repeated DedupKey returns false and stores nothing, which makes a
	// redelivered webhook or a replayed cron tick a no-op.
	AppendTrigger(ctx context.Context, t *Trigger) (bool, error)
	// ClaimDueTriggers returns the pending triggers whose DueAt has passed,
	// oldest first, and marks them claimed by moving their DueAt one interval
	// beyond now so a second pass cannot pick them up twice.
	ClaimDueTriggers(ctx context.Context, now time.Time, limit int) ([]*Trigger, error)
	SaveTrigger(ctx context.Context, t *Trigger) error
	GetTrigger(ctx context.Context, id string) (*Trigger, error)
	ListTriggers(ctx context.Context, q TriggerQuery) ([]*Trigger, error)
	// CountTriggers returns how many triggers are still pending, for the water
	// level the console shows.
	CountTriggers(ctx context.Context, disposition string) (int, error)

	SaveThread(ctx context.Context, th *Thread) error
	GetThread(ctx context.Context, id string) (*Thread, error)

	SaveSubscription(ctx context.Context, s *Subscription) error
	DropSubscription(ctx context.Context, threadID, ticketID string) error
	ListSubscribers(ctx context.Context, threadID string) ([]*Subscription, error)
	ListSubscriptions(ctx context.Context, ticketID string) ([]*Subscription, error)
	CountSubscriptions(ctx context.Context) (int, error)

	SaveTicket(ctx context.Context, t *Ticket) error
	GetTicket(ctx context.Context, id string) (*Ticket, error)
	ListTickets(ctx context.Context, q TicketQuery) ([]*Ticket, error)
	// CountTickets returns how many tickets are in one state.
	CountTickets(ctx context.Context, state string) (int, error)

	SaveAssignment(ctx context.Context, a *Assignment) error
	ListAssignments(ctx context.Context, ticketID string, limit int) ([]*Assignment, error)

	AppendDecision(ctx context.Context, d *Decision) error
	ListDecisions(ctx context.Context, q TriggerQuery) ([]*Decision, error)

	GetPolicy(ctx context.Context, source string) (*Policy, error)
	ListPolicies(ctx context.Context) ([]*Policy, error)
	SavePolicy(ctx context.Context, p *Policy) error

	AppendEvent(ctx context.Context, e *Event) error
	EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*Event, error)
	MaxSeq(ctx context.Context) (uint64, error)
	// Prune drops triggers, decisions and events older than before, at most
	// limit rows per call so a long backlog never holds one huge transaction.
	// Tickets and their assignments are kept: they are the work record.
	Prune(ctx context.Context, before time.Time, limit int) (int, error)
}

// Assessor decides, for one batch of candidate triggers, whether each is worth
// acting on. The returned slice must have the same length as the batch; a
// shorter answer is a bug in the implementation and Engine falls back to rules
// for the missing tail rather than losing triggers.
//
// The batch is the batching effect the architecture relies on: one call covers
// everything a beat accumulated, so a burst costs less per decision than a drip.
type Assessor interface {
	Assess(ctx context.Context, batch []*Trigger) ([]Decision, error)
}

// ThreadLink records a launched task against the conversation that caused it, so
// the console's thread view can follow the reply the worker produced. It is a
// function rather than an interface on purpose: the thread store's method returns
// its own message type, and importing that package here would buy nothing.
type ThreadLink func(ctx context.Context, threadID, taskID, resourceID string) error

// Executor is the Worker layer boundary. It launches a ticket as a hub task,
// feeds a human answer back into a parked one, and cancels it. It only reports
// facts: it never speaks to a user directly, which is what keeps a dozen
// concurrent workers from all replying at once.
type Executor interface {
	Launch(ctx context.Context, t *Ticket, a *Assignment) (*AssignmentResult, error)
	Input(ctx context.Context, t *Ticket, msg string) (*AssignmentResult, error)
	Cancel(ctx context.Context, t *Ticket) error
}

// Clock is the time source, injectable so a test can drive the beat without
// sleeping.
type Clock func() time.Time

// Options configures an Engine.
type Options struct {
	// Store is required.
	Store Store
	// Assessor is optional: without one every candidate falls back to the rules.
	Assessor Assessor
	// Bus is optional: without one no domain event is published.
	Bus *Bus
	// Clock defaults to time.Now.
	Clock Clock
	// BatchMax caps how many triggers one evaluation looks at. The default is
	// 20: enough to benefit from batching, small enough that one bad batch
	// cannot stall the queue.
	BatchMax int
	// Resource is the worker a ticket runs on when its policy names none.
	Resource string
	// ClaimLease is how far ahead a claimed trigger's DueAt is moved so a
	// concurrent pass cannot take it twice. The default is one minute.
	ClaimLease time.Duration
	// MaxAttempts is how often a failing ticket is retried before it is closed
	// as failed. The default is 3.
	MaxAttempts int
	// RetryBase is the first backoff step; it doubles per attempt. The default
	// is five seconds.
	RetryBase time.Duration
}

func (o *Options) normalize() {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.BatchMax <= 0 {
		o.BatchMax = 20
	}
	if o.ClaimLease <= 0 {
		o.ClaimLease = time.Minute
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 5 * time.Second
	}
}

// BodyLimit is how much of an untrusted body is ever quoted into a prompt. The
// rest stays behind the trigger id: spilling is cheaper than a diluted window.
const BodyLimit = 500

// MessageLimit is how many recent thread messages a brief inlines.
const MessageLimit = 5
