package backup

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is reported when a checkpoint id does not exist. A deleted
// archive and one that never existed look the same to the console on purpose:
// the id is all it has, and telling them apart would leak what was there.
var ErrNotFound = errors.New("backup: checkpoint not found")

// BackupStore is the catalogue the service keeps: the checkpoint rows and the
// single schedule row. Declared here and implemented by internal/store, so this
// package stays free of GORM — the same inversion the registry and the hub use.
type BackupStore interface {
	// Create inserts a checkpoint row before the dump starts, so a run that
	// never finishes is still visible in the console.
	Create(ctx context.Context, c *Checkpoint) error
	// Finish records how a run ended: status, size, checksum, duration and the
	// machine readable code when it failed.
	Finish(ctx context.Context, c *Checkpoint) error
	Get(ctx context.Context, id string) (*Checkpoint, error)
	// List returns the catalogue, newest first. limit <= 0 means every row.
	List(ctx context.Context, limit int) ([]Checkpoint, error)
	Delete(ctx context.Context, id string) error
	// LastSuccess is the most recent successful checkpoint, which anchors the
	// interval schedule so a restart does not shorten the period.
	LastSuccess(ctx context.Context) (*Checkpoint, error)
	// MarkInterrupted closes rows that were left running by a process that is
	// gone: a backup cannot survive its own process, so a "running" row from a
	// previous boot is a crash, not work in progress.
	MarkInterrupted(ctx context.Context, olderThan time.Time) (int64, error)
	// LoadSchedule reads the policy. A nil result with no error means the
	// console has never saved one and the defaults apply.
	LoadSchedule(ctx context.Context) (*Schedule, error)
	SaveSchedule(ctx context.Context, s Schedule, actor string) error
}

// quiesce keeps the hub still while the database is rewritten.
type Quiesce interface {
	// Drain refuses new work and waits for what is already running to finish.
	Drain(ctx context.Context) error
	// Allow lifts the barrier again, also when a restore failed.
	Allow()
	// ActiveTaskCount is what the console shows while it waits.
	ActiveTaskCount() int
}

// Reloader rebuilds in-memory state from the database after a restore.
type Reloader interface {
	Reload(ctx context.Context) error
}

// Reloaders combines several reload steps into one, so the wiring in
// cmd/agenthub-server can rebuild the registry, the hub and the event cursor
// without this package knowing any of them exist.
func Reloaders(items ...Reloader) Reloader {
	return reloadGroup(items)
}

// ReloaderFunc adapts a plain function to Reloader, the way http.HandlerFunc
// adapts a function to http.Handler. It keeps the wiring in cmd a one-liner.
type ReloaderFunc func(ctx context.Context) error

// Reload calls the function.
func (f ReloaderFunc) Reload(ctx context.Context) error { return f(ctx) }

type reloadGroup []Reloader

func (g reloadGroup) Reload(ctx context.Context) error {
	for _, item := range g {
		if item == nil {
			continue
		}
		if err := item.Reload(ctx); err != nil {
			return err
		}
	}
	return nil
}
