package router

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Passes is the beat: one goroutine that repeatedly asks the engine what is due
// and then launches whatever became runnable.
//
// Two properties matter more than the interval. Single flight: a pass never
// overlaps itself, because two passes writing tickets would break the single
// writer promise. Coalescing: a caller that wants a pass "now" pushes to a
// one-slot channel, so ten requests during a pass produce exactly one extra pass
// rather than ten.
type Passes struct {
	engine   *Engine
	exec     Executor
	src      *CronSource
	interval time.Duration
	kick     chan struct{}

	maxParallel int
	inflight    atomic.Int32
	wg          sync.WaitGroup

	pruneEvery time.Duration
	triggerTTL time.Duration
	lastPrune  time.Time

	mu        sync.Mutex
	stats     Stats
	running   bool
	stopped   bool
	clock     Clock
	stopGrace time.Duration
}

// PassOptions configures the beat.
type PassOptions struct {
	Engine   *Engine
	Executor Executor
	// Schedules scans cron schedules before each pass; nil disables it.
	Schedules *CronSource
	// Interval is the beat. The default is two seconds, which is what the
	// reference implementation uses.
	Interval time.Duration
	// MaxParallel bounds concurrent worker runs. A ticket beyond the bound stays
	// queued and is picked up by a later pass, so a burst never opens a hundred
	// goroutines.
	MaxParallel int
	// PruneEvery and TriggerTTL control the retention sweep. Zero disables it.
	PruneEvery time.Duration
	TriggerTTL time.Duration
	// Clock defaults to time.Now.
	Clock Clock
}

// NewPasses builds the beat.
func NewPasses(o PassOptions) *Passes {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.MaxParallel <= 0 {
		o.MaxParallel = 8
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Passes{
		engine:      o.Engine,
		exec:        o.Executor,
		src:         o.Schedules,
		interval:    o.Interval,
		kick:        make(chan struct{}, 1),
		maxParallel: o.MaxParallel,
		pruneEvery:  o.PruneEvery,
		triggerTTL:  o.TriggerTTL,
		clock:       o.Clock,
		stopGrace:   10 * time.Second,
	}
}

// Run drives the beat until ctx is cancelled. It returns nil on a clean stop: a
// cancelled context is a shutdown, not a failure.
func (p *Passes) Run(ctx context.Context) error {
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.running = false
		p.stopped = true
		p.mu.Unlock()
	}()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.drain()
			return nil
		case <-ticker.C:
		case <-p.kick:
		}
		p.pass(ctx)
	}
}

// Kick asks for a pass as soon as possible. It never blocks: if a pass is already
// pending, this request adds nothing.
func (p *Passes) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Live reports whether the loop is running.
func (p *Passes) Live() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// Snapshot returns the beat counters, merged into engine stats by Service.
func (p *Passes) Snapshot() (passes uint64, lastAt time.Time, batch int, lastErr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats.Passes, p.stats.LastPassAt, p.stats.BatchSize, p.stats.LastError
}

// pass is one beat, in the order the architecture prescribes: due sources first,
// then the evaluation, then the launches, then housekeeping.
func (p *Passes) pass(ctx context.Context) {
	now := p.clock()

	if p.src != nil {
		if _, err := p.src.Scan(ctx, now); err != nil {
			slog.Warn("router: cron scan", "error", err)
			p.record(0, err)
		}
	}

	applied, err := p.engine.EvaluateDue(ctx, now)
	if err != nil {
		slog.Error("router: evaluate due", "error", err)
	}
	p.record(applied, err)

	p.dispatch(ctx, now)

	if p.pruneEvery > 0 && now.Sub(p.lastPrune) >= p.pruneEvery {
		p.lastPrune = now
		if removed, perr := p.engine.Prune(ctx, now.Add(-p.triggerTTL), 5000); perr != nil {
			slog.Warn("router: prune", "error", perr)
		} else if removed > 0 {
			slog.Info("router: pruned expired history", "rows", removed)
		}
	}
}

// dispatch launches runnable tickets up to the parallelism bound. Each launch runs
// in its own goroutine so a slow worker never stretches the beat, and the ticket
// stays `running` until the executor reports a fact back.
func (p *Passes) dispatch(ctx context.Context, now time.Time) {
	if p.exec == nil {
		return
	}
	for int(p.inflight.Load()) < p.maxParallel {
		tk, a, err := p.engine.NextTicket(ctx, now)
		if err != nil {
			slog.Error("router: claim ticket", "error", err)
			return
		}
		if tk == nil {
			return
		}
		p.inflight.Add(1)
		p.wg.Add(1)
		go p.launch(ctx, tk, a)
	}
}

func (p *Passes) launch(ctx context.Context, tk *Ticket, a *Assignment) {
	defer func() {
		p.inflight.Add(-1)
		p.wg.Done()
		if rec := recover(); rec != nil {
			slog.Error("router: worker panicked", "ticket", tk.ID, "panic", rec)
			_ = p.engine.RecordAssignment(ctx, tk.ID, a, &AssignmentResult{
				State: AssignmentFailed, Error: "worker_panicked",
			})
		}
	}()

	res, err := p.exec.Launch(ctx, tk, a)
	if res == nil {
		res = &AssignmentResult{State: AssignmentFailed, Error: "executor returned nothing"}
	}
	if err != nil && res.Error == "" {
		res.Error = truncate(err.Error(), 400)
	}
	if rerr := p.engine.RecordAssignment(ctx, tk.ID, a, res); rerr != nil {
		slog.Error("router: record assignment", "ticket", tk.ID, "error", rerr)
	}
}

// drain waits for in-flight workers on shutdown, with a bound: a wedged adapter
// must not hold the process open forever.
func (p *Passes) drain() {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(p.stopGrace):
		slog.Warn("router: shutdown before every worker finished", "inflight", p.inflight.Load())
	}
}

func (p *Passes) record(batch int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats.Passes++
	p.stats.LastPassAt = p.clock()
	p.stats.BatchSize = batch
	if err != nil {
		p.stats.LastError = truncate(err.Error(), 200)
	} else {
		p.stats.LastError = ""
	}
}
