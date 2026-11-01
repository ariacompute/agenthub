package sandbox

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Sweeper reclaims expired leases on a beat.
//
// It copies router.Passes on purpose: one pass at a time (so two passes never
// decide the same lease is expired), and a kick channel of size one (so a burst
// of kicks adds one extra pass instead of a queue of them). Reclaiming is
// housekeeping: a failure is logged and the next beat tries again, because
// propagating it would let a dead docker daemon fail healthy tasks.
type Sweeper struct {
	interval time.Duration
	sweep    func(context.Context) (int, error)
	clock    Clock
	log      *slog.Logger

	mu        sync.Mutex
	passes    int
	reclaimed int
	lastAt    time.Time
	lastErr   string

	kick    chan struct{}
	stop    chan struct{}
	once    sync.Once
	done    chan struct{}
	running bool
}

// SweeperOptions configures the beat.
type SweeperOptions struct {
	Interval time.Duration
	Clock    Clock
	Sweep    func(context.Context) (int, error)
	Log      *slog.Logger
}

// NewSweeper builds the beat.
func NewSweeper(opts SweeperOptions) *Sweeper {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Sweeper{
		interval: opts.Interval,
		sweep:    opts.Sweep,
		clock:    opts.Clock,
		log:      opts.Log,
		kick:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Run starts the beat and blocks until Stop. It is the Service's goroutine, and
// the only one this package starts.
func (s *Sweeper) Run(ctx context.Context) error {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer close(s.done)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stop:
			return nil
		case <-ticker.C:
			s.pass(ctx)
		case <-s.kick:
			s.pass(ctx)
		}
	}
}

// Stop ends the beat. It is safe to call twice and safe to call before Run.
func (s *Sweeper) Stop() {
	s.once.Do(func() {
		close(s.stop)
	})
}

// Kick asks for one extra pass. Non-blocking by design: the caller is a request
// handler and must never wait for housekeeping.
func (s *Sweeper) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Counters reports what the beat has done, for the console's water level.
func (s *Sweeper) Counters() (passes, reclaimed int, lastAt time.Time, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passes, s.reclaimed, s.lastAt, s.lastErr
}

func (s *Sweeper) pass(ctx context.Context) {
	// Single flight is structural: Run is the only goroutine that calls pass, and
	// it is sequential. A kick that arrives mid-pass is folded into the next
	// iteration rather than overlapping this one.
	s.mu.Lock()
	s.passes++
	s.lastAt = s.clock()
	s.mu.Unlock()

	if s.sweep != nil {
		n, err := s.sweep(ctx)
		s.mu.Lock()
		s.reclaimed += n
		if err != nil {
			s.lastErr = err.Error()
		} else {
			s.lastErr = ""
		}
		s.mu.Unlock()
		if err != nil && s.log != nil {
			s.log.Warn("sandbox sweep failed", "err", err.Error())
		}
	}
}

// Wait blocks until Run returned, which the shutdown path uses to make sure no
// pass is still touching a provider while the process exits.
func (s *Sweeper) Wait(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
	}
}
