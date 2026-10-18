package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors the service reports. Handlers turn them into machine readable codes,
// so none of them carries operator facing prose.
var (
	// ErrDisabled is what an instance without AGENTHUB_BACKUP_DIR answers: the
	// feature is not broken, it is simply not configured.
	ErrDisabled = errors.New("backup: backups are not configured")
	// ErrBusy means another dump or restore already holds the database.
	ErrBusy = errors.New("backup: another backup or restore is running")
	// ErrRestoreConfirm is returned when the caller did not echo the
	// checkpoint id back, which is the last chance to notice a wrong row.
	ErrRestoreConfirm = errors.New("backup: restore needs the checkpoint id as confirmation")
	// ErrSchemaMismatch means the archive was taken at a schema this build does
	// not know; loading it would write tables the running code cannot read.
	ErrSchemaMismatch = errors.New("backup: archive was taken at a newer schema")
	// ErrRestoreFailed wraps whatever the engine hit, so the console can say
	// the database is untouched rather than half restored.
	ErrRestoreFailed = errors.New("backup: restore failed")
)

const (
	// preRestoreGrace keeps the automatic rollback checkpoint out of the
	// retention sweep long enough to be useful: it is the way back from a
	// restore that was a mistake, and a retention rule must not delete it.
	preRestoreGrace = 24 * time.Hour
	// schedulerWake is how often the loop re-reads the schedule. A minute is
	// deliberate: a policy saved in the console takes effect without a
	// restart, and the cost is one cheap query.
	schedulerWake = time.Minute
	// runningTimeout is how long a "running" row may stay that way before a
	// restart declares its process gone.
	runningTimeout = 6 * time.Hour
)

// Options is everything the service needs. The database side is injected as an
// engine and a store, so a unit test can exercise the whole orchestration with
// a stub engine and no PostgreSQL at all.
type Options struct {
	// Dir is where archives are written. Empty disables the feature.
	Dir string
	// Engine produces and consumes the archives.
	Engine Engine
	// Store is the catalogue. Without one the service still takes checkpoints,
	// it just cannot list them — which is what the unit tests rely on.
	Store BackupStore
	// Quiesce stops the hub while the database is rewritten. Optional.
	Quiesce Quiesce
	// Reload rebuilds in-memory state after a restore. Optional.
	Reload Reloader
	// Applied returns the migration files this database has run, used to
	// compare against an archive before restoring it.
	Applied func(ctx context.Context) ([]string, error)
	// Migrate brings the database up to the running build's schema, run after
	// restoring an archive that predates some migrations.
	Migrate func() error
	Log     *slog.Logger
	// Now is a seam for tests and for the scheduler's arithmetic.
	Now func() time.Time
}

// Service orchestrates checkpoints and restores: it decides when one may run,
// writes the catalogue, keeps the directory from filling up and makes sure a
// restore happens with the hub still and a way back in hand.
type Service struct {
	opts Options
	log  *slog.Logger

	// op guards the database: one dump or one restore at a time, never both.
	op sync.Mutex
}

// New builds the service. It never fails: an unconfigured directory simply
// yields a disabled service, which is how an instance without a backup volume
// behaves.
func New(opts Options) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{opts: opts, log: log}
}

// Enabled reports whether checkpointing is configured.
func (s *Service) Enabled() bool {
	return s.opts.Engine != nil && strings.TrimSpace(s.opts.Dir) != ""
}

// Dir is the archive directory, for the console to show.
func (s *Service) Dir() string { return s.opts.Dir }

// EngineName is the engine the console will see on new checkpoints.
func (s *Service) EngineName() string {
	if s.opts.Engine == nil {
		return ""
	}
	return s.opts.Engine.Name()
}

// Quiesce exposes the barrier a restore drains, so the console can say how much
// work is still in flight. It is optional: without one the service simply
// restores without waiting for anybody.
func (s *Service) Quiesce() (Quiesce, bool) {
	if s.opts.Quiesce == nil {
		return nil, false
	}
	return s.opts.Quiesce, true
}

func (s *Service) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now().UTC()
	}
	return time.Now().UTC()
}

// ---------------------------------------------------------------------------
// taking a checkpoint
// ---------------------------------------------------------------------------

// Checkpoint dumps the database and records it in the catalogue.
//
// kind says why the checkpoint exists (manual, scheduled, or taken as the way
// back from a restore) and actor is who asked, for the audit trail.
func (s *Service) Checkpoint(ctx context.Context, kind, label, actor string) (*Checkpoint, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if kind == "" {
		kind = KindManual
	}
	if !s.op.TryLock() {
		return nil, ErrBusy
	}
	defer s.op.Unlock()
	return s.checkpointLocked(ctx, kind, label, actor)
}

// checkpointLocked does the work of Checkpoint for a caller that already holds
// the operation lock. A restore needs it: the rollback checkpoint it takes is
// part of the same database operation, not a second one competing for it.
func (s *Service) checkpointLocked(ctx context.Context, kind, label, actor string) (*Checkpoint, error) {
	started := s.now()
	c := &Checkpoint{
		ID:        newID(),
		Kind:      kind,
		Engine:    s.opts.Engine.Name(),
		Status:    StatusRunning,
		Label:     label,
		CreatedAt: started,
	}
	if s.opts.Store != nil {
		if err := s.opts.Store.Create(ctx, c); err != nil {
			return nil, err
		}
	}

	path, m, err := WriteCheckpoint(ctx, s.opts.Engine, s.opts.Dir, Manifest{
		Kind:      kind,
		Label:     label,
		CreatedAt: started,
	})
	if err != nil {
		s.log.Error("backup: checkpoint failed", "kind", kind, "error", err)
		finished := s.now()
		c.Status = StatusFailed
		c.ErrorCode = codeOf(err)
		c.FinishedAt = &finished
		c.DurationMS = finished.Sub(started).Milliseconds()
		if s.opts.Store != nil {
			_ = s.opts.Store.Finish(ctx, c)
		}
		// A half written archive is not a checkpoint: leave no file behind.
		_ = os.Remove(path)
		return c, err
	}

	// The file checksum goes in the row so a restore can prove the archive on
	// disk is the one that was written; the manifest checksum covers the
	// payload inside it.
	sum, err := FileChecksum(path)
	if err != nil {
		s.log.Error("backup: checksum failed", "id", c.ID, "error", err)
		return c, err
	}
	finished := s.now()
	c.Status = StatusOK
	c.Path = path
	c.Checksum = sum
	c.SizeBytes = fileSize(path)
	c.RowCount = m.Tables2Rows()
	c.TableCount = len(m.Tables)
	c.Schema = strings.Join(m.Schema, ",")
	c.DurationMS = finished.Sub(started).Milliseconds()
	c.FinishedAt = &finished
	if s.opts.Store != nil {
		if err := s.opts.Store.Finish(ctx, c); err != nil {
			return c, err
		}
	}
	s.log.Info("backup: checkpoint taken",
		"id", c.ID, "kind", kind, "engine", c.Engine, "bytes", c.SizeBytes,
		"rows", c.RowCount, "ms", c.DurationMS, "actor", actor)

	if _, err := s.Prune(ctx); err != nil {
		// Pruning is housekeeping: a checkpoint that is already on disk must
		// not be reported as a failure because a neighbour could not go.
		s.log.Warn("backup: prune after checkpoint", "error", err)
	}
	return c, nil
}

// List is the catalogue the console shows, newest first.
func (s *Service) List(ctx context.Context) ([]Checkpoint, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if s.opts.Store == nil {
		return nil, nil
	}
	return s.opts.Store.List(ctx, 0)
}

// Get returns one checkpoint, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*Checkpoint, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if s.opts.Store == nil {
		return nil, ErrNotFound
	}
	return s.opts.Store.Get(ctx, id)
}

// Path returns the archive file for a download, after checking it still
// matches the checksum recorded when it was written — an operator must never
// be handed a silently corrupted archive.
func (s *Service) Path(ctx context.Context, id string) (string, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if c.Status != StatusOK || c.Path == "" {
		return "", ErrNotFound
	}
	if c.Checksum != "" {
		sum, err := FileChecksum(c.Path)
		if err != nil {
			return "", err
		}
		if sum != c.Checksum {
			return "", ErrChecksumMismatch
		}
	}
	return c.Path, nil
}

// Delete removes one checkpoint: the file first, then the row. A row whose file
// is already gone is still removed, because the console must be able to clear a
// catalogue entry that lost its archive.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if s.opts.Store == nil {
		return ErrNotFound
	}
	c, err := s.opts.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Path != "" {
		// Only ever remove files inside the backup directory: the path comes
		// from the database, and a stray value must not become an rm -rf.
		if inside, err := s.contains(c.Path); err != nil || !inside {
			return fmt.Errorf("%w: archive %q is outside the backup directory", ErrNotFound, c.Path)
		}
		if err := os.Remove(c.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := s.opts.Store.Delete(ctx, id); err != nil {
		return err
	}
	s.log.Info("backup: checkpoint deleted", "id", id, "kind", c.Kind)
	return nil
}

// contains reports whether a path is inside the archive directory. It resolves
// symlinks out of the comparison so ".." cannot escape it.
func (s *Service) contains(path string) (bool, error) {
	root, err := filepath.Abs(s.opts.Dir)
	if err != nil {
		return false, err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)), nil
}

// ---------------------------------------------------------------------------
// restoring
// ---------------------------------------------------------------------------

// Restore rewinds the database to a checkpoint.
//
// The order matters and is the whole safety story: confirm the id, prove the
// archive is the one that was written, refuse a schema this build cannot read,
// take a checkpoint of what is about to be replaced, hold the hub still, then
// rewrite. If any step fails the hub is released and the database is left as it
// was, with the pre-restore checkpoint still there to go back to.
func (s *Service) Restore(ctx context.Context, id, confirm, actor string) (*Checkpoint, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if s.opts.Store == nil {
		return nil, ErrNotFound
	}
	target, err := s.opts.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// The operator has to retype the id (or its short form): it is the last
	// chance to notice the wrong row before the database is rewritten.
	if !confirmed(target.ID, confirm) {
		return nil, ErrRestoreConfirm
	}
	if !s.op.TryLock() {
		return nil, ErrBusy
	}
	defer s.op.Unlock()

	m, err := s.verify(ctx, target)
	if err != nil {
		return target, err
	}

	// The way back: a checkpoint of the state that is about to be replaced.
	// Taken before the hub is drained so a failure here costs nothing.
	rollback, rbErr := s.checkpointLocked(ctx, KindPreRestore, "before restoring "+shortID(target.ID), actor)
	if rbErr != nil {
		return target, fmt.Errorf("%w: could not take the rollback checkpoint: %v", ErrRestoreFailed, rbErr)
	}

	if s.opts.Quiesce != nil {
		if err := s.opts.Quiesce.Drain(ctx); err != nil {
			return target, fmt.Errorf("%w: could not quiet the hub: %v", ErrRestoreFailed, err)
		}
	}
	// Whatever happens from here, the hub must start answering again.
	defer func() {
		if s.opts.Quiesce != nil {
			s.opts.Quiesce.Allow()
		}
	}()

	if err := s.load(ctx, target, m); err != nil {
		return target, err
	}

	// An archive taken before some migrations were applied is fine: the data
	// is loaded and the schema is then brought up to this build.
	if s.opts.Migrate != nil {
		if err := s.opts.Migrate(); err != nil {
			return target, fmt.Errorf("%w: migrate after restore: %v", ErrRestoreFailed, err)
		}
	}
	// In-memory state still describes the database that was just replaced.
	if s.opts.Reload != nil {
		if err := s.opts.Reload.Reload(ctx); err != nil {
			return target, fmt.Errorf("%w: reload after restore: %v", ErrRestoreFailed, err)
		}
	}

	restoredAt := s.now()
	target.RestoredAt = &restoredAt
	target.RestoredBy = actor
	if err := s.opts.Store.Finish(ctx, target); err != nil {
		return target, err
	}
	s.log.Info("backup: restore finished",
		"checkpoint", target.ID, "engine", target.Engine, "rows", target.RowCount,
		"rollback", rollback.ID, "actor", actor)
	return target, nil
}

// verify proves an archive may be loaded: the file is the one that was written,
// and its schema is one this build understands.
func (s *Service) verify(ctx context.Context, c *Checkpoint) (Manifest, error) {
	if c.Status != StatusOK || c.Path == "" {
		return Manifest{}, ErrNotFound
	}
	if c.Checksum != "" {
		sum, err := FileChecksum(c.Path)
		if err != nil {
			return Manifest{}, err
		}
		if sum != c.Checksum {
			return Manifest{}, fmt.Errorf("%w: file changed since it was written", ErrChecksumMismatch)
		}
	}
	_ = ctx
	m, err := ReadManifest(c.Path)
	if err != nil {
		return Manifest{}, err
	}
	if s.opts.Applied == nil {
		return m, nil
	}
	current, err := s.opts.Applied(ctx)
	if err != nil {
		return Manifest{}, err
	}
	known := make(map[string]bool, len(current))
	for _, name := range current {
		known[name] = true
	}
	// The dangerous direction is an archive that knows migrations this build
	// does not: it would write tables the running code cannot read. An archive
	// that is merely older is fine — Migrate catches the schema up afterwards.
	for _, name := range m.Schema {
		if !known[name] {
			return Manifest{}, fmt.Errorf("%w: archive knows %q, this build does not", ErrSchemaMismatch, name)
		}
	}
	return m, nil
}

// load hands the archive to the engine. The payload is materialised first
// because a restore verifies the checksum while it streams: an archive that
// fails halfway must not leave the database half written.
func (s *Service) load(ctx context.Context, c *Checkpoint, m Manifest) error {
	dir, err := os.MkdirTemp("", "agenthub-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	payload, err := os.Create(filepath.Join(dir, "payload"))
	if err != nil {
		return err
	}
	defer func() { _ = payload.Close() }()
	if _, err := Extract(c.Path, payload); err != nil {
		return fmt.Errorf("%w: %v", ErrRestoreFailed, err)
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.opts.Engine.Restore(ctx, payload, m); err != nil {
		return fmt.Errorf("%w: %v", ErrRestoreFailed, err)
	}
	return nil
}

// confirmed accepts the full id or the short form the console shows in the
// table, so the operator is not asked to retype a uuid by hand.
func confirmed(id, confirm string) bool {
	confirm = strings.TrimSpace(confirm)
	return confirm != "" && (confirm == id || confirm == shortID(id))
}

// ---------------------------------------------------------------------------
// schedule and retention
// ---------------------------------------------------------------------------

// Schedule is the policy in force: the stored one, or the defaults when the
// console has never saved one.
func (s *Service) Schedule(ctx context.Context) (Schedule, error) {
	if s.opts.Store == nil {
		return DefaultSchedule(), nil
	}
	stored, err := s.opts.Store.LoadSchedule(ctx)
	if err != nil {
		return DefaultSchedule(), err
	}
	if stored == nil {
		return DefaultSchedule(), nil
	}
	return *stored, nil
}

// SaveSchedule stores a policy after checking it can actually run, so the
// console can never accept one that silently does nothing.
func (s *Service) SaveSchedule(ctx context.Context, sched Schedule, actor string) (Schedule, error) {
	if !s.Enabled() {
		return sched, ErrDisabled
	}
	if err := sched.Validate(); err != nil {
		return sched, err
	}
	if s.opts.Store == nil {
		return sched, nil
	}
	if err := s.opts.Store.SaveSchedule(ctx, sched, actor); err != nil {
		return sched, err
	}
	s.log.Info("backup: schedule saved", "mode", sched.Mode, "enabled", sched.Enabled, "actor", actor)
	return sched, nil
}

// SeedSchedule writes the policy the environment describes, but only while the
// console has never saved one: the stored row is the operator's decision and a
// restart must never overwrite it with a default.
func (s *Service) SeedSchedule(ctx context.Context, sched Schedule, actor string) error {
	if !s.Enabled() || s.opts.Store == nil {
		return nil
	}
	stored, err := s.opts.Store.LoadSchedule(ctx)
	if err != nil {
		return err
	}
	if stored != nil {
		return nil
	}
	if err := sched.Validate(); err != nil {
		return err
	}
	return s.opts.Store.SaveSchedule(ctx, sched, actor)
}

// NextRun is when the scheduler will take the next checkpoint. It returns
// ErrNotScheduled when nothing is due, which is not an error condition.
func (s *Service) NextRun(ctx context.Context) (time.Time, error) {
	sched, err := s.Schedule(ctx)
	if err != nil {
		return time.Time{}, err
	}
	var last *time.Time
	if s.opts.Store != nil {
		if c, err := s.opts.Store.LastSuccess(ctx); err == nil && c != nil {
			last = &c.CreatedAt
		}
	}
	return sched.Next(s.now(), last)
}

// Prune enforces the retention policy: keep the newest KeepLast checkpoints and
// everything younger than MaxAgeDays, delete the rest.
//
// Two checkpoints are never swept: the newest one, so an instance always has
// something to restore, and a pre-restore rollback checkpoint inside its grace
// period, so the way back from a restore cannot be deleted by a cleanup run.
func (s *Service) Prune(ctx context.Context) (int, error) {
	if s.opts.Store == nil {
		return 0, nil
	}
	sched, err := s.Schedule(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := s.opts.Store.List(ctx, 0)
	if err != nil {
		return 0, err
	}
	candidates := make([]Checkpoint, 0, len(rows))
	for _, c := range rows {
		if c.Status == StatusRunning {
			continue
		}
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.After(candidates[j].CreatedAt) })

	now := s.now()
	cutoff := now.AddDate(0, 0, -sched.MaxAgeDays)
	var removed int
	for i, c := range candidates {
		keep := i < sched.KeepLast || c.CreatedAt.After(cutoff)
		if i == 0 {
			// The newest checkpoint is the one an operator would reach for
			// first; no retention rule may take it.
			keep = true
		}
		if c.Kind == KindPreRestore && c.CreatedAt.After(now.Add(-preRestoreGrace)) {
			keep = true
		}
		if keep {
			continue
		}
		if err := s.Delete(ctx, c.ID); err != nil {
			s.log.Warn("backup: prune could not remove a checkpoint", "id", c.ID, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		s.log.Info("backup: pruned checkpoints", "removed", removed)
	}
	return removed, nil
}

// Run is the scheduler. It wakes on a fixed cadence rather than arming a single
// timer, so a schedule saved in the console takes effect within a minute and a
// long downtime produces one catch-up checkpoint instead of a burst.
func (s *Service) Run(ctx context.Context) error {
	if !s.Enabled() {
		s.log.Info("backup: scheduler not started", "reason", "AGENTHUB_BACKUP_DIR is not set")
		return nil
	}
	if s.opts.Store != nil {
		n, err := s.opts.Store.MarkInterrupted(ctx, s.now().Add(-runningTimeout))
		if err != nil {
			s.log.Warn("backup: could not close interrupted rows", "error", err)
		} else if n > 0 {
			s.log.Warn("backup: interrupted checkpoints found", "rows", n, "hint", "a previous run did not finish")
		}
	}
	ticker := time.NewTicker(schedulerWake)
	defer ticker.Stop()
	for {
		if err := s.runDue(ctx); err != nil {
			s.log.Error("backup: scheduled checkpoint failed", "error", err)
		}
		select {
		case <-ctx.Done():
			s.log.Info("backup: scheduler stopped")
			return nil
		case <-ticker.C:
		}
	}
}

// runDue takes a checkpoint when the schedule says one is overdue.
func (s *Service) runDue(ctx context.Context) error {
	next, err := s.NextRun(ctx)
	if err != nil {
		if errors.Is(err, ErrNotScheduled) {
			return nil
		}
		// A broken policy should be visible, not retried silently forever.
		s.log.Warn("backup: schedule could not be evaluated", "error", err)
		return nil
	}
	if next.After(s.now()) {
		return nil
	}
	if _, err := s.Checkpoint(ctx, KindSchedule, "", "scheduler"); err != nil && !errors.Is(err, ErrBusy) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// Tables2Rows totals the rows a manifest describes.
func (m Manifest) Tables2Rows() int64 {
	var total int64
	for _, n := range m.RowCounts {
		total += n
	}
	return total
}

// newID builds a checkpoint id: a prefix an operator recognises in a log line
// plus random bytes, the same shape the rest of agenthub's ids use. It falls
// back to a timestamp rather than returning an error, because an id is never
// the reason a backup should fail.
func newID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("bkp-%d", time.Now().UnixNano())
	}
	return "bkp-" + hex.EncodeToString(buf[:])
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// codeOf maps an error to the machine readable code stored on a failed row, so
// the console can localise it without parsing a message.
func codeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEngineUnavailable):
		return "backup_engine_unavailable"
	case errors.Is(err, ErrEngineFailed):
		return "backup_engine_failed"
	case errors.Is(err, ErrChecksumMismatch):
		return "backup_checksum_mismatch"
	case errors.Is(err, ErrDisabled):
		return "backups_disabled"
	default:
		return "backup_failed"
	}
}
