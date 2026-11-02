package router

import (
	"context"
	"log/slog"
	"time"

	"agenthub/internal/identity"
	"agenthub/internal/schedules"
)

// CronSource turns due schedules into triggers. It is one source among four: a
// schedule is not special, it is simply the only source that arrives on its own
// rather than because somebody said something.
//
// The scan is throttled because listing every schedule on a two second beat is
// wasteful: the crank interval is the resolution at which a cron expression can
// fire anyway, since the parser has minute granularity.
type CronSource struct {
	Schedules schedules.Store
	Engine    *Engine
	// Interval is how often the schedule table is read. The default is 15s.
	Interval time.Duration
	// Clock defaults to the engine's clock.
	Clock Clock

	lastScan time.Time
}

// NewCronSource builds the source. A nil store means an instance with no
// scheduler store, which simply never fires anything.
func NewCronSource(store schedules.Store, e *Engine) *CronSource {
	return &CronSource{Schedules: store, Engine: e, Interval: 15 * time.Second}
}

// Scan emits one trigger per schedule that came due and advances the row.
//
// The scan runs under the process identity, not under whoever happens to own
// the beat's context: it reads every owner's due rows on purpose, and it writes
// them back as the instance (advancing next_run, marking a broken expression).
// Row level security has no way to know that from an empty owner id alone, so
// the identity is stated here, in the one place that needs it.
func (c *CronSource) Scan(ctx context.Context, now time.Time) (int, error) {
	if c == nil || c.Schedules == nil || c.Engine == nil {
		return 0, nil
	}
	if c.Interval > 0 && !c.lastScan.IsZero() && now.Sub(c.lastScan) < c.Interval {
		return 0, nil
	}
	c.lastScan = now
	ctx = identity.With(ctx, identity.Identity{System: true})

	list, err := c.Schedules.List(ctx, "")
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, s := range list {
		if s == nil || !s.Enabled || s.Trigger != string(schedules.TriggerCron) || s.CronExpr == "" {
			continue
		}
		due, derr := schedules.NextRun(s.CronExpr, now)
		if derr != nil {
			// A broken expression is the operator's; record it once per scan and
			// leave the row enabled so fixing it takes effect immediately.
			slog.Warn("router: schedule has an unusable cron expression", "schedule", s.ID, "expr", s.CronExpr, "error", derr)
			if s.LastStatus != "invalid_cron" {
				s.LastStatus = "invalid_cron"
				// The row is written back under its own owner: the scan acts as
				// the instance, but it never rewrites ownership.
				if _, uerr := c.Schedules.Update(ctx, s, s.OwnerID); uerr != nil {
					slog.Warn("router: mark schedule invalid", "schedule", s.ID, "error", uerr)
				}
			}
			continue
		}
		if s.NextRun != "" {
			if at, perr := time.Parse(time.RFC3339, s.NextRun); perr == nil && at.After(now) {
				// Stored next run is still in the future: nothing to do.
				continue
			}
		}
		trig := &Trigger{
			Source:    string(SourceCron),
			SourceID:  s.ID,
			ActorID:   s.OwnerID,
			ActorKind: ActorSystem,
			Kind:      "reminder",
			Body:      s.Goal,
			Explicit:  true,
			At:        now,
			DueAt:     now,
			Meta:      map[string]any{"goal": s.Goal, "schedule": s.ID, "cron": s.CronExpr},
			// The due minute is the identity of this firing: a restart inside the
			// same minute must not open a second ticket.
			DedupKey: "cron:" + s.ID + ":" + now.UTC().Format("200601021504"),
		}
		if _, fresh, ierr := c.Engine.Intake(ctx, trig); ierr != nil {
			slog.Warn("router: intake cron trigger", "schedule", s.ID, "error", ierr)
			continue
		} else if !fresh {
			continue
		}
		fired++
		s.LastRunAt = now.UTC().Format(time.RFC3339)
		s.LastStatus = "triggered"
		s.NextRun = due.UTC().Format(time.RFC3339)
		if _, uerr := c.Schedules.Update(ctx, s, s.OwnerID); uerr != nil {
			slog.Warn("router: advance schedule", "schedule", s.ID, "error", uerr)
		}
	}
	return fired, nil
}
