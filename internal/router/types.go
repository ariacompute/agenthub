// Package router is agenthub's scheduler: the two layer split of the Super-agent
// architecture, adapted to a service that already owns a task state machine.
//
// The Router layer (this package) only filters, decides and dispatches: it never
// runs a resource itself. The Worker layer is an ordinary hub task, launched by
// HubExecutor through hub.Send / hub.Resume, so a scheduled run inherits the
// lifecycle, spans, artifacts, events and push notifications of every other run.
//
// Three disciplines hold the design together:
//
//   - A single writer. Trigger, Thread, Subscription, Ticket, Assignment and
//     Decision are only mutated by Engine, which serialises every command.
//   - State in the transaction, external work after the commit. Engine returns
//     intents; Passes drives them once the rows are written.
//   - Commands are imperative and can be refused (with a machine readable code);
//     what an executor reports back is a fact in the past tense.
package router

import (
	"errors"
	"strings"
	"time"
)

// Sentinel errors. Their text is the machine readable code the console maps onto
// localised copy, exactly like hub's persistence errors.
var (
	// ErrTicketNotFound is returned for an unknown ticket id.
	ErrTicketNotFound = errors.New("router_ticket_not_found")
	// ErrTicketClosed is returned when a command targets a ticket that already
	// ended. A closed ticket is immutable.
	ErrTicketClosed = errors.New("router_ticket_closed")
	// ErrHandbackExhausted is returned when a trigger is handed back twice. One
	// hand-back is allowed so a worker can say "not mine" without ping-ponging.
	ErrHandbackExhausted = errors.New("router_handback_exhausted")
	// ErrPolicyInvalid is returned for an unparsable policy.
	ErrPolicyInvalid = errors.New("router_policy_invalid")
	// ErrThreadNotFound is returned for an unknown thread id.
	ErrThreadNotFound = errors.New("router_thread_not_found")
	// ErrResourceUnset is returned when a ticket has no resource to run.
	ErrResourceUnset = errors.New("router_resource_unset")
)

// Source enumerates where a Trigger came from. The list is closed: a new source
// is a new integration, not a free form label.
type Source string

const (
	// SourceCron is a schedule whose next run came due.
	SourceCron Source = "cron"
	// SourceThread is a message posted into a collaboration thread.
	SourceThread Source = "thread"
	// SourceChannel is an inbound message from an external channel webhook.
	SourceChannel Source = "channel"
	// SourceManual is a human pressing "run now" or a script posting to the API.
	SourceManual Source = "manual"
)

// Sources lists every supported source, in display order.
var Sources = []Source{SourceCron, SourceThread, SourceChannel, SourceManual}

// NormalizeSource returns a valid source, or the empty string for anything else.
func NormalizeSource(s string) Source {
	for _, c := range Sources {
		if string(c) == s {
			return c
		}
	}
	return Source("")
}

// Disposition is what the router decided about a Trigger. Every trigger ends in
// exactly one of these; "pending" is the only transient value.
type Disposition string

const (
	// DispositionPending means the trigger is queued for the next evaluation.
	DispositionPending Disposition = "pending"
	// DispositionFiltered means the program layer dropped it before any model
	// call. This is the fast path and the reason most traffic is free.
	DispositionFiltered Disposition = "filtered"
	// DispositionRouted means it reached a ticket, either by subscription
	// pass-through or by an accept / attach decision.
	DispositionRouted Disposition = "routed"
	// DispositionIgnored means the evaluation looked at it and chose not to act.
	DispositionIgnored Disposition = "ignored"
	// DispositionStopped means it belongs to a ticket that already covers the
	// thread, so no new work was opened.
	DispositionStopped Disposition = "stopped"
	// DispositionUnavailable means nothing could decide: the semantic assessor
	// failed and the rules had no opinion. Never silently promoted to accept.
	DispositionUnavailable Disposition = "unavailable"
)

// Dispositions lists every disposition value.
var Dispositions = []Disposition{
	DispositionPending, DispositionFiltered, DispositionRouted,
	DispositionIgnored, DispositionStopped, DispositionUnavailable,
}

// Settled reports whether a disposition closes the trigger's life. stop and
// unavailable are deliberately not settled: the trigger is finished but nobody
// actually dealt with it, which is what the console surfaces.
func (d Disposition) Settled() bool {
	return d == DispositionRouted || d == DispositionIgnored || d == DispositionFiltered
}

// DecisionKind enumerates the verdicts of an evaluation.
type DecisionKind string

const (
	// DecisionAccept opens a new ticket.
	DecisionAccept DecisionKind = "accept"
	// DecisionAttach adds the trigger to an open ticket on the same thread.
	DecisionAttach DecisionKind = "attach"
	// DecisionStop leaves it to the ticket that already owns the thread.
	DecisionStop DecisionKind = "stop"
	// DecisionIgnore says the trigger does not warrant action.
	DecisionIgnore DecisionKind = "ignore"
	// DecisionUnavailable says no verdict could be reached.
	DecisionUnavailable DecisionKind = "unavailable"
)

// DecisionKinds lists every verdict.
var DecisionKinds = []DecisionKind{
	DecisionAccept, DecisionAttach, DecisionStop, DecisionIgnore, DecisionUnavailable,
}

// NormalizeDecisionKind returns a valid verdict, or the empty string.
func NormalizeDecisionKind(s string) DecisionKind {
	for _, k := range DecisionKinds {
		if string(k) == s {
			return k
		}
	}
	return DecisionKind("")
}

// Disposition maps a verdict onto the trigger's final state.
func (k DecisionKind) Disposition() Disposition {
	switch k {
	case DecisionAccept, DecisionAttach:
		return DispositionRouted
	case DecisionStop:
		return DispositionStopped
	case DecisionIgnore:
		return DispositionIgnored
	default:
		return DispositionUnavailable
	}
}

// Ticket states. "open" is anything that is not done, failed or canceled.
const (
	// TicketQueued waits for a free worker slot.
	TicketQueued = "queued"
	// TicketRunning has an assignment in flight.
	TicketRunning = "running"
	// TicketWaiting is parked in input-required until a human answers.
	TicketWaiting = "waiting"
	// TicketBlocked could not start and will be retried later.
	TicketBlocked = "blocked"
	// TicketIdle reported its work but still follows live threads. It waits for
	// the next event on them instead of relaunching itself, which is what makes
	// "the ticket ends when its thread closes, not when a worker says ready"
	// work without a busy loop.
	TicketIdle = "idle"
	// TicketDone finished successfully.
	TicketDone = "done"
	// TicketFailed gave up after the retry budget.
	TicketFailed = "failed"
	// TicketCanceled was stopped by a human.
	TicketCanceled = "canceled"
)

// TicketStates lists every ticket state, in display order.
var TicketStates = []string{
	TicketQueued, TicketRunning, TicketWaiting, TicketIdle, TicketBlocked,
	TicketDone, TicketFailed, TicketCanceled,
}

// Open reports whether the ticket still owns work. Only an open ticket accepts a
// follow-up or a command; a closed one is immutable.
func (t *Ticket) Open() bool {
	return t.State != TicketDone && t.State != TicketFailed && t.State != TicketCanceled
}

// Assignment states, reported by the executor in the past tense.
const (
	AssignmentStarted  = "started"
	AssignmentWaiting  = "waiting"
	AssignmentReported = "reported"
	AssignmentFailed   = "failed"
	AssignmentCanceled = "canceled"
)

// Policy modes for a source.
const (
	// PolicyOff makes the source produce no trigger at all.
	PolicyOff = "off"
	// PolicyCandidate lets program rules admit it to the evaluation.
	PolicyCandidate = "candidate"
	// PolicyRequestOnly only admits an explicit request (mention, /run, keyword).
	PolicyRequestOnly = "request_only"
)

// PolicyModes lists every policy mode.
var PolicyModes = []string{PolicyOff, PolicyCandidate, PolicyRequestOnly}

// NormalizePolicyMode returns a valid mode, or the empty string.
func NormalizePolicyMode(s string) string {
	for _, m := range PolicyModes {
		if m == s {
			return m
		}
	}
	return ""
}

// Actor kinds.
const (
	ActorHuman  = "human"
	ActorAgent  = "agent"
	ActorSystem = "system"
)

// Trigger is one inbound fact, stored once. Its disposition is the audit trail of
// what the router did with it, which is the whole point of the event driven
// design: noise is filtered and visible, not silent.
type Trigger struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	SourceID string `json:"sourceId,omitempty"`
	ThreadID string `json:"threadId,omitempty"`
	// ActorID and ActorKind are trustworthy: they come from the authenticated
	// principal or the verified webhook, never from the message body.
	ActorID   string `json:"actorId,omitempty"`
	ActorName string `json:"actorName,omitempty"`
	ActorKind string `json:"actorKind,omitempty"`
	// Kind is the source's own classification (request / message / reminder).
	Kind string `json:"kind,omitempty"`
	// Body is untrusted data. Never interpreted as an instruction.
	Body string `json:"body,omitempty"`
	// Explicit marks a request addressed at the router (mention, /run, keyword).
	Explicit bool           `json:"explicit"`
	Meta     map[string]any `json:"meta,omitempty"`
	// DedupKey is the idempotency key: a redelivered webhook or a replayed cron
	// tick appends nothing.
	DedupKey string `json:"-"`
	// Disposition, DecisionID and TicketID are the verdict.
	Disposition string `json:"disposition"`
	DecisionID  string `json:"decisionId,omitempty"`
	TicketID    string `json:"ticketId,omitempty"`
	// Handbacks counts how often a worker said "not mine". One is allowed.
	Handbacks int `json:"handbacks"`
	// DueAt is when the evaluation may consider it: now, or the end of the
	// quiet window the policy put it in.
	DueAt     time.Time `json:"dueAt"`
	At        time.Time `json:"at"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Human reports whether the trigger came from a person. A human message to a
// subscribed thread bypasses the evaluation entirely.
func (t *Trigger) Human() bool { return t.ActorKind == ActorHuman }

// Thread is the event stream a Trigger belongs to: one conversation, one pull
// request, one mail chain. The router owns only the routing state of a thread;
// the conversation itself lives in internal/threads.
type Thread struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Title    string `json:"title,omitempty"`
	OpenedBy string `json:"openedBy,omitempty"`
	// Closed threads no longer route work; a ticket following only closed
	// threads ends.
	Closed       bool      `json:"closed"`
	CloseOutcome string    `json:"closeOutcome,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Subscription binds a ticket to a thread. The relation is many-to-many: one
// ticket follows several threads and one thread is followed by several tickets,
// which is how two workers notice each other's work without talking directly.
type Subscription struct {
	ThreadID string `json:"threadId"`
	TicketID string `json:"ticketId"`
	// Own marks the thread this ticket opened. A ticket ends when every thread
	// it opened is closed.
	Own bool `json:"own"`
	// Context binds for observation only: the ticket sees the events but is not
	// woken by them and gains no follow-up strength.
	Context bool `json:"context"`
	// Closed records that the thread closed while the ticket still followed it.
	Closed bool      `json:"closed"`
	At     time.Time `json:"at"`
}

// Ticket is one unit of work the router decided on. It is not a hub task: a task
// is one run, a ticket may need several (retry, a human answer, a follow-up).
type Ticket struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Goal     string `json:"goal"`
	OwnerID  string `json:"ownerId,omitempty"`
	State    string `json:"state"`
	Resource string `json:"resource,omitempty"`
	// TaskID is the hub task of the latest assignment, so the console can jump
	// straight to the trace.
	TaskID string `json:"taskId,omitempty"`
	// OriginTriggerID and BatchID say what opened it, which is the audit link
	// back from a ticket to the event that caused it.
	OriginTriggerID string `json:"originTriggerId,omitempty"`
	BatchID         string `json:"batchId,omitempty"`
	// Focus is the thread a follow-up should be routed to first.
	Focus     string    `json:"focus,omitempty"`
	Attempts  int       `json:"attempts"`
	NextTryAt time.Time `json:"nextTryAt,omitempty"`
	LastError string    `json:"lastError,omitempty"`
	// WaitsForPerson is set when the latest assignment parked in
	// input-required (or failed) and no human has spoken since. While it holds,
	// follow-ups are collected instead of relaunching the worker.
	WaitsForPerson bool       `json:"waitsForPerson"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	EndedAt        *time.Time `json:"endedAt,omitempty"`
}

// TicketDetail is a ticket with the state that explains it: what it follows, how
// often it ran and why it was decided.
type TicketDetail struct {
	*Ticket
	Subscriptions []*Subscription `json:"subscriptions"`
	Assignments   []*Assignment   `json:"assignments"`
	Decisions     []*Decision     `json:"decisions"`
	Origin        *Trigger        `json:"origin,omitempty"`
}

// Assignment is one run of a ticket. Every launch is a new row, so a retry never
// rewrites history.
type Assignment struct {
	ID       string `json:"id"`
	TicketID string `json:"ticketId"`
	State    string `json:"state"`
	TaskID   string `json:"taskId,omitempty"`
	// Note carries the executor's one line about the outcome.
	Note      string     `json:"note,omitempty"`
	Error     string     `json:"error,omitempty"`
	TriggerID string     `json:"triggerId,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// AssignmentResult is what the executor reports back after a run.
type AssignmentResult struct {
	State  string
	TaskID string
	Note   string
	Error  string
	// WaitsForPerson mirrors the task state: input-required parks the ticket.
	WaitsForPerson bool
}

// Decision is the verdict recorded for one trigger, with the reason, so the
// console can answer "why did the router do that".
type Decision struct {
	ID        string `json:"id"`
	TriggerID string `json:"triggerId"`
	BatchID   string `json:"batchId,omitempty"`
	Kind      string `json:"kind"`
	TicketID  string `json:"ticketId,omitempty"`
	// Resource is an optional worker suggestion; the policy and the instance
	// default are consulted first, so a model can never pick an arbitrary
	// endpoint on its own.
	Resource string `json:"resource,omitempty"`
	// By is "rule" or "model": which layer produced the verdict.
	By     string    `json:"by"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// Event kinds. A domain event is a fact already committed, not a command.
const (
	EventTriggerReceived = "trigger.received"
	EventTriggerRequeued = "trigger.requeued"
	EventEvalStarted     = "eval.started"
	EventDecision        = "decision.recorded"
	EventTicketCreated   = "ticket.created"
	EventTicketResumed   = "ticket.resumed"
	EventTicketEnded     = "ticket.ended"
	EventAssignmentStart = "assignment.started"
	EventAssignmentEnd   = "assignment.ended"
	EventThreadSubscribe = "thread.subscribed"
	EventThreadDrop      = "thread.unsubscribed"
	EventPolicyChanged   = "policy.changed"
	EventPassFinished    = "pass.finished"
)

// Event is one frame the console can replay after a reconnect.
type Event struct {
	Seq uint64 `json:"seq"`
	// Kind is the event name; Subject is the trigger / ticket / thread id.
	Kind    string         `json:"kind"`
	Subject string         `json:"subject,omitempty"`
	At      time.Time      `json:"at"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Stats is the water level the console renders above the trigger stream.
type Stats struct {
	// Pending is the queue that the next pass will look at.
	Pending int `json:"pending"`
	// Queued / RunningTickets / Waiting are the ticket states that mean work in
	// hand. RunningTickets is a count, Live is the switch: they are separate
	// fields on purpose, because "the loop is on" and "three tickets run" are
	// different questions.
	Queued         int `json:"queued"`
	RunningTickets int `json:"runningTickets"`
	Waiting        int `json:"waiting"`
	// BatchSize is how many triggers the last pass evaluated in one call, the
	// batching effect of the router layer made observable.
	BatchSize int `json:"batchSize"`
	// Passes counts completed beats and LastPassAt says when.
	Passes      uint64    `json:"passes"`
	LastPassAt  time.Time `json:"lastPassAt"`
	LastError   string    `json:"lastError,omitempty"`
	Subscribers int       `json:"subscribers"`
	// Streams is how many consoles are following the event stream.
	Streams int `json:"streams"`
	// Live says whether the beat loop is running (the feature switch).
	Live bool `json:"live"`
}

// TriggerQuery filters a trigger listing.
type TriggerQuery struct {
	// Source, Disposition and ThreadID are exact matches; empty means "any".
	Source      string
	Disposition string
	ThreadID    string
	TicketID    string
	Limit       int
}

// Normalize clamps the limit into the range the API accepts.
func (q *TriggerQuery) Normalize() {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
}

// TicketQuery filters a ticket listing.
type TicketQuery struct {
	State  string
	Open   bool
	Limit  int
	Offset int
}

// Normalize clamps the limit into the range the API accepts.
func (q *TicketQuery) Normalize() {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
}

// Policy is the program layer's configuration for one source. It is the natural
// language free half of the two level filter: an operator turns a source off, or
// narrows it to explicit requests, without editing code.
type Policy struct {
	Source string `json:"source"`
	Mode   string `json:"mode"`
	// Allow is an actor allowlist; empty means everyone.
	Allow []string `json:"allow,omitempty"`
	// Keywords admit a trigger that is not explicitly addressed.
	Keywords []string `json:"keywords,omitempty"`
	// QuietStart and QuietEnd are minutes since midnight, in the server's
	// location. Quiet triggers are deferred, never dropped. -1 disables it.
	QuietStart int `json:"quietStart"`
	QuietEnd   int `json:"quietEnd"`
	// Resource is the default worker for tickets opened from this source.
	Resource  string    `json:"resource,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate rejects a policy the engine cannot honour. An invalid policy is a 400,
// never a silent fallback to "everything on".
func (p *Policy) Validate() error {
	if NormalizeSource(p.Source) == "" {
		return ErrPolicyInvalid
	}
	if NormalizePolicyMode(p.Mode) == "" {
		return ErrPolicyInvalid
	}
	for _, v := range []int{p.QuietStart, p.QuietEnd} {
		if v < -1 || v > 24*60-1 {
			return ErrPolicyInvalid
		}
	}
	if (p.QuietStart == -1) != (p.QuietEnd == -1) {
		return ErrPolicyInvalid
	}
	return nil
}

// Normalize trims and de-duplicates the list fields.
func (p *Policy) Normalize() {
	p.Source = string(NormalizeSource(p.Source))
	p.Allow = normalizeList(p.Allow)
	p.Keywords = normalizeList(p.Keywords)
	p.Resource = strings.TrimSpace(p.Resource)
}

func normalizeList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

// DefaultPolicies is the policy set a fresh instance runs with: every source is
// a candidate, nothing is quiet, no allowlist. Turning a source off is a
// deliberate operator decision, so the default has to be open — an empty
// scheduler would look broken.
func DefaultPolicies() []*Policy {
	out := make([]*Policy, 0, len(Sources))
	for _, s := range Sources {
		out = append(out, &Policy{Source: string(s), Mode: PolicyCandidate, QuietStart: -1, QuietEnd: -1})
	}
	return out
}
