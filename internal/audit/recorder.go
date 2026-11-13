package audit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"agenthub/internal/identity"
)

// Options tunes the writer.
type Options struct {
	// Queue bounds what may wait in memory. When it is full events are dropped
	// and counted rather than buffered further: an audit trail that applies
	// back pressure to the API it observes turns a slow database into an outage.
	Queue int
	// Batch is how many events one INSERT carries.
	Batch int
	// FlushEvery bounds how long an event may sit before it is written, so a
	// quiet instance still records promptly.
	FlushEvery time.Duration
	// Retention is how long a row is kept. Zero disables pruning.
	Retention time.Duration
	Logger    *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.Queue <= 0 {
		o.Queue = 8192
	}
	if o.Batch <= 0 {
		o.Batch = 64
	}
	if o.Batch > o.Queue {
		o.Batch = o.Queue
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 500 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Recorder is the single writer of the audit trail.
//
// One goroutine owns the sequence: it drains the queue, assigns prev_hash/hash
// and writes the batch. That is what keeps the chain linear without locking the
// insert path, and why the hash is computed here rather than by the database.
type Recorder struct {
	store Store
	opts  Options

	queue   chan Event
	dropped atomic.Uint64
	written atomic.Uint64
	failed  atomic.Uint64

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
	started  atomic.Bool

	// prev/prevID are owned by the writer goroutine.
	prev   string
	prevID int64

	mu sync.Mutex // guards prev during Verify/Prune on the calling goroutine
}

// New returns a recorder. It does not start the writer; Run does, and main
// starts it once the database is reachable.
func New(store Store, opts Options) *Recorder {
	opts = opts.withDefaults()
	return &Recorder{
		store: store,
		opts:  opts,
		queue: make(chan Event, opts.Queue),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Record enqueues one event. It never blocks and never returns an error.
func (r *Recorder) Record(ev Event) {
	if r == nil {
		return
	}
	select {
	case r.queue <- ev:
	default:
		// Counted instead of logged: a full queue would otherwise emit one log
		// line per request and turn a database hiccup into a log storm.
		r.dropped.Add(1)
	}
}

// Dropped returns how many events were discarded because the queue was full.
func (r *Recorder) Dropped() uint64 { return r.dropped.Load() }

// Written returns how many events reached the store.
func (r *Recorder) Written() uint64 { return r.written.Load() }

// Run is the writer loop. It resumes the chain from the newest stored row, so a
// restart continues the sequence instead of starting a second one.
func (r *Recorder) Run(ctx context.Context) {
	if r == nil || r.started.Swap(true) {
		return
	}
	defer close(r.done)

	if r.store != nil {
		sysCtx := identity.System()
		if hash, id, err := r.store.LastHash(sysCtx); err != nil {
			r.opts.Logger.Warn("audit: cannot read the chain tail; starting a fresh link", "error", err)
		} else {
			r.prev, r.prevID = hash, id
		}
	}

	ticker := time.NewTicker(r.opts.FlushEvery)
	defer ticker.Stop()

	batch := make([]Event, 0, r.opts.Batch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		r.append(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-r.stop:
			// Drain what is already queued: a shutdown must not lose the last
			// few hundred milliseconds of the trail.
			for {
				select {
				case ev := <-r.queue:
					batch = append(batch, ev)
					if len(batch) >= r.opts.Batch {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		case <-ctx.Done():
			flush()
			return
		case <-ticker.C:
			flush()
		case ev := <-r.queue:
			batch = append(batch, ev)
			if len(batch) >= r.opts.Batch {
				flush()
			}
		}
	}
}

// Close stops the writer and waits for the last batch.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	if r.started.Load() {
		<-r.done
	}
}

// append stamps the chain and writes the batch.
func (r *Recorder) append(batch []Event) {
	out := make([]Event, 0, len(batch))
	for _, ev := range batch {
		// PostgreSQL timestamptz keeps microseconds, not Go nanoseconds.
		// Canonical hashes the timestamp, so stamping a nanosecond value and
		// reading a microsecond one back makes Verify report the very first
		// row as tampered. Normalise once, before both the hash and the INSERT.
		ev.Time = ev.Time.UTC().Truncate(time.Microsecond)
		ev.PrevHash = r.prev
		ev.Hash = Hash(r.prev, ev)
		r.prev = ev.Hash
		out = append(out, ev)
	}
	if r.store == nil {
		r.written.Add(uint64(len(out)))
		return
	}
	r.mu.Lock()
	r.prevID += int64(len(out))
	r.mu.Unlock()
	if err := r.store.Append(identity.System(), out); err != nil {
		// The chain cannot be repaired after a failed write: the in-memory tail
		// has moved past rows that never landed. Record the failure and keep
		// going; /api/audit/verify reports the break, which is the honest
		// answer.
		r.failed.Add(uint64(len(out)))
		r.opts.Logger.Error("audit: append batch failed", "events", len(out), "error", err)
		return
	}
	r.written.Add(uint64(len(out)))
}

// Verify walks the trail in segments and checks the links. from/to bound the
// window; a zero time means "the whole table".
func (r *Recorder) Verify(ctx context.Context, from, to time.Time, pageSize int) (Verification, error) {
	if r.store == nil {
		return Verification{OK: true}, nil
	}
	if pageSize <= 0 {
		pageSize = 5000
	}
	// Reading the trail is an administrative act; the identity says so rather
	// than running with whatever scope the caller happened to have.
	ctx = identity.With(systemContextOf(ctx), identity.Identity{AuditRead: true, System: true})
	anchor, err := r.store.Anchor(ctx)
	if err != nil {
		return Verification{}, err
	}
	// A retained anchor only describes the head of the whole table. A walk
	// with a time window starts at an arbitrary row whose predecessor is merely
	// outside the window, not removed, so it must infer its own seed.
	seed := ""
	anchorSource := ""
	if from.IsZero() && anchor.Hash != "" {
		seed, anchorSource = anchor.Hash, AnchorRetention
	}
	var checked int64
	var afterID int64 = 0
	var anchored bool
	var anchorAt string
	for {
		rows, err := r.store.Segment(ctx, from, afterID, pageSize)
		if err != nil {
			return Verification{}, err
		}
		if len(rows) == 0 {
			break
		}
		if checked == 0 {
			if seed == "" && rows[0].PrevHash != "" {
				// No trusted anchor: a trail pruned before anchors existed, a
				// manual expiry, or a window starting mid-chain. The head names
				// its own predecessor; use it as the seed. Every row from here
				// on is still hash- and link-checked normally, so this cannot
				// hide tampering — it only stops a legitimate prune from being
				// reported as a break.
				seed, anchorSource = rows[0].PrevHash, AnchorInferred
			}
			if anchorSource != "" {
				anchored, anchorAt = true, rows[0].EventID
			}
		}
		prev, broken := VerifySegment(seed, rows)
		if broken != nil {
			broken.Checked += checked
			broken.Anchored, broken.AnchorAt, broken.AnchorSource = anchored, anchorAt, anchorSource
			return *broken, nil
		}
		seed = prev
		checked += int64(len(rows))
		// Segment returns ascending ids; the next page starts after the last one.
		afterID = rows[len(rows)-1].Seq
		if afterID == 0 || len(rows) < pageSize {
			break
		}
	}
	return Verification{
		Checked:      checked,
		OK:           true,
		Anchored:     anchored,
		AnchorAt:     anchorAt,
		AnchorSource: anchorSource,
	}, nil
}

// List answers a console query. The caller's context already carries the audit
// read identity, so the policies decide what is visible: this method does not
// climb to a wider scope than the request was granted.
func (r *Recorder) List(ctx context.Context, q Query) ([]Event, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.List(ctx, q)
}

// Stats returns the counters for the same filters as List.
func (r *Recorder) Stats(ctx context.Context, q Query) (Stats, error) {
	if r == nil || r.store == nil {
		return Stats{}, nil
	}
	return r.store.Stats(ctx, q)
}

// Get returns one event by its event id.
func (r *Recorder) Get(ctx context.Context, eventID string) (*Event, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.Get(ctx, eventID)
}

// Prune expires rows older than the retention window and returns how many went.
// The store records the removed tail as the chain anchor in the same
// transaction, so the surviving head keeps a verifiable predecessor.
func (r *Recorder) Prune(ctx context.Context) (int64, error) {
	if r.store == nil || r.opts.Retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-r.opts.Retention)
	ctx = identity.SystemWith(identity.Identity{Retention: true})
	return r.store.Prune(ctx, cutoff)
}

// systemContextOf drops cancellation but keeps values, so a verification started
// from a request that then disconnects still finishes.
func systemContextOf(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}
