package router

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"agenthub/internal/schedules"
)

// ServiceOptions wires the router into a running instance.
type ServiceOptions struct {
	// Store is required: it is where every router row lives.
	Store Store
	// Runner is the hub. Required for tickets to actually run; without it the
	// router still filters and records, which is what a read-only instance does.
	Runner TaskRunner
	// History supplies recent thread turns for the worker brief. Optional.
	History HistoryProvider
	// ThreadLink records a launched task in the conversation that caused it.
	// Optional; without it the console cannot cross-reference the two.
	ThreadLink ThreadLink
	// Generator is the model backend for the semantic layer. Nil keeps the
	// router on rules alone, which is the offline default.
	Generator Generator
	// Schedules is the schedule store the cron source reads. Nil disables cron.
	Schedules schedules.Store
	// Bus is the live event fan-out. A nil bus creates one.
	Bus *Bus
	// Clock defaults to time.Now.
	Clock Clock
	// Interval is the beat; the default is two seconds.
	Interval time.Duration
	// MaxParallel bounds concurrent workers; the default is eight, matching
	// AGENTHUB_MAX_PARALLEL.
	MaxParallel int
	// BatchMax caps one evaluation; the default is twenty.
	BatchMax int
	// Resource is the default worker for tickets no policy names one for.
	Resource string
	// MaxAttempts and RetryBase shape the retry ladder.
	MaxAttempts int
	RetryBase   time.Duration
	// TriggerTTL and PruneEvery control retention. Zero disables the sweep.
	TriggerTTL time.Duration
	PruneEvery time.Duration
}

// Service is the facade the HTTP layer talks to. It owns the beat, so a handler
// never starts or stops goroutines of its own.
type Service struct {
	engine    *Engine
	passes    *Passes
	exec      *HubExecutor
	bus       *Bus
	schedules schedules.Store
	engineSrc *CronSource

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// NewService builds the router. Nothing starts until Run is called, which keeps
// an embedded instance (tests, the e2e suite) in control of its own clock.
func NewService(o ServiceOptions) *Service {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.MaxParallel <= 0 {
		o.MaxParallel = 8
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	bus := o.Bus
	if bus == nil {
		bus = NewBus()
	}
	engine := NewEngine(Options{
		Store:       o.Store,
		Assessor:    NewLLMAssessor(o.Generator),
		Bus:         bus,
		Clock:       o.Clock,
		BatchMax:    o.BatchMax,
		Resource:    o.Resource,
		MaxAttempts: o.MaxAttempts,
		RetryBase:   o.RetryBase,
	})
	exec := NewHubExecutor(o.Runner, o.Store, o.History, o.ThreadLink)
	cron := NewCronSource(o.Schedules, engine)

	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		engine:    engine,
		exec:      exec,
		bus:       bus,
		schedules: o.Schedules,
		engineSrc: cron,
		passes: NewPasses(PassOptions{
			Engine:      engine,
			Executor:    exec,
			Schedules:   cron,
			Interval:    o.Interval,
			MaxParallel: o.MaxParallel,
			PruneEvery:  o.PruneEvery,
			TriggerTTL:  o.TriggerTTL,
			Clock:       o.Clock,
		}),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Run drives the beat until Stop is called or ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			s.Stop()
		case <-s.ctx.Done():
		}
	}()
	return s.passes.Run(s.ctx)
}

// Stop halts the beat and releases the event streams. It is safe to call twice,
// which matters because both a signal handler and a deferred close may fire.
func (s *Service) Stop() {
	s.once.Do(func() {
		s.cancel()
		// Wait for the loop to notice before closing the bus, so a pass cannot
		// publish into a closed channel.
		deadline := time.Now().Add(2 * time.Second)
		for s.passes.Live() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		s.bus.Close()
	})
}

// Engine exposes the single writer, for the rare caller that needs a command the
// facade does not wrap.
func (s *Service) Engine() *Engine { return s.engine }

// Bus exposes the event fan-out.
func (s *Service) Bus() *Bus { return s.bus }

// Kick asks for a pass now. The console uses it after an injection so the effect
// is visible without waiting for the next beat.
func (s *Service) Kick() { s.passes.Kick() }

// Live reports whether the beat is running.
func (s *Service) Live() bool { return s.passes.Live() }

// Stats merges the entity counts with the beat counters.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	st, err := s.engine.Stats(ctx, s.Live())
	if err != nil {
		return st, err
	}
	passes, lastAt, batch, lastErr := s.passes.Snapshot()
	st.Passes = passes
	st.LastPassAt = lastAt
	st.BatchSize = batch
	st.LastError = lastErr
	return st, nil
}

// Inject admits one trigger from outside: a manual run, a thread message or a
// channel webhook. The second return says whether it was fresh; a redelivery is
// not an error.
func (s *Service) Inject(ctx context.Context, t *Trigger) (*Trigger, bool, error) {
	out, fresh, err := s.engine.Intake(ctx, t)
	if err != nil {
		return nil, false, err
	}
	if fresh {
		s.passes.Kick()
	}
	return out, fresh, nil
}

// RunSchedule fires a schedule immediately, whatever its cron expression says.
//
// ownerID is the caller's: a manual firing is an act on somebody's goal, so the
// row is looked up inside that owner's scope. Before this parameter existed any
// signed in user could fire another account's goal and the resulting ticket was
// recorded under the victim's actor id.
func (s *Service) RunSchedule(ctx context.Context, id, ownerID string) (*Trigger, error) {
	if s.schedules == nil {
		return nil, ErrPolicyInvalid
	}
	sc, err := s.schedules.Get(ctx, id, ownerID)
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, ErrTicketNotFound
	}
	now := s.engine.now()
	trig := &Trigger{
		Source:    string(SourceManual),
		SourceID:  sc.ID,
		ActorID:   sc.OwnerID,
		ActorKind: ActorSystem,
		Kind:      "reminder",
		Body:      sc.Goal,
		Explicit:  true,
		At:        now,
		DueAt:     now,
		Meta:      map[string]any{"goal": sc.Goal, "schedule": sc.ID, "manual": true},
		DedupKey:  "manual:" + sc.ID + ":" + now.UTC().Format("20060102150405"),
	}
	out, _, err := s.engine.Intake(ctx, trig)
	if err != nil {
		return nil, err
	}
	s.passes.Kick()
	return out, nil
}

// ListTriggers returns the recent trigger stream.
func (s *Service) ListTriggers(ctx context.Context, q TriggerQuery) ([]*Trigger, error) {
	return s.engine.ListTriggers(ctx, q)
}

// GetTrigger returns one trigger.
func (s *Service) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	return s.engine.GetTrigger(ctx, id)
}

// ListTickets returns tickets, newest first.
func (s *Service) ListTickets(ctx context.Context, q TicketQuery) ([]*Ticket, error) {
	return s.engine.ListTickets(ctx, q)
}

// GetTicket returns a ticket with its subscriptions, runs and decisions.
func (s *Service) GetTicket(ctx context.Context, id string) (*TicketDetail, error) {
	return s.engine.GetTicket(ctx, id)
}

// CancelTicket stops a ticket and cancels the task behind it.
func (s *Service) CancelTicket(ctx context.Context, id string) error {
	tk, err := s.engine.Store().GetTicket(ctx, id)
	if err != nil {
		return err
	}
	if tk == nil {
		return ErrTicketNotFound
	}
	if tk.TaskID != "" && s.exec != nil {
		if cerr := s.exec.Cancel(ctx, tk); cerr != nil {
			slog.Warn("router: cancel task", "ticket", id, "task", tk.TaskID, "error", cerr)
		}
	}
	return s.engine.CancelTicket(ctx, id)
}

// Handback returns a trigger to the queue.
func (s *Service) Handback(ctx context.Context, ticketID, triggerID, reason string) error {
	err := s.engine.Handback(ctx, ticketID, triggerID, reason)
	if err == nil {
		s.passes.Kick()
	}
	return err
}

// UnsubscribeThread drops every subscription on a thread.
func (s *Service) UnsubscribeThread(ctx context.Context, threadID string) error {
	return s.engine.UnsubscribeThread(ctx, threadID)
}

// BindThread subscribes a ticket to a thread.
func (s *Service) BindThread(ctx context.Context, ticketID, threadID string, own, contextOnly bool) error {
	return s.engine.BindThread(ctx, ticketID, threadID, own, contextOnly)
}

// CloseThread records that a conversation ended.
func (s *Service) CloseThread(ctx context.Context, threadID, outcome string) error {
	return s.engine.CloseThread(ctx, threadID, outcome)
}

// Policies lists the program layer configuration.
func (s *Service) Policies(ctx context.Context) ([]*Policy, error) {
	return s.engine.Policies(ctx)
}

// SavePolicy stores one source's policy.
func (s *Service) SavePolicy(ctx context.Context, p *Policy) (*Policy, error) {
	return s.engine.SavePolicy(ctx, p)
}

// Subscribe opens a live event stream. Replay is served separately, from the
// store, so a reconnecting client can catch up without the bus remembering.
func (s *Service) Subscribe(subject string, buffer int) (<-chan Event, func()) {
	return s.bus.Subscribe(subject, buffer)
}

// EventsSince replays committed frames.
func (s *Service) EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*Event, error) {
	return s.engine.EventsSince(ctx, subject, since, limit)
}

// MaxSeq is the highest persisted sequence, so a client can tell whether a replay
// is complete.
func (s *Service) MaxSeq(ctx context.Context) (uint64, error) {
	return s.engine.Store().MaxSeq(ctx)
}
