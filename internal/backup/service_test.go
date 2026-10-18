package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// test doubles
// ---------------------------------------------------------------------------

// memStore is the catalogue without a database: enough to prove the service's
// bookkeeping, ordering and retention arithmetic.
type memStore struct {
	mu        sync.Mutex
	rows      []*Checkpoint
	sched     *Schedule
	interrupt int64
}

func (m *memStore) Create(ctx context.Context, c *Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *c
	m.rows = append(m.rows, &clone)
	return nil
}

func (m *memStore) Finish(ctx context.Context, c *Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.rows {
		if row.ID == c.ID {
			*row = *c
			return nil
		}
	}
	return ErrNotFound
}

func (m *memStore) Get(ctx context.Context, id string) (*Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.rows {
		if row.ID == id {
			clone := *row
			return &clone, nil
		}
	}
	return nil, ErrNotFound
}

func (m *memStore) List(ctx context.Context, limit int) ([]Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Checkpoint, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, row := range m.rows {
		if row.ID == id {
			m.rows = append(m.rows[:i], m.rows[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (m *memStore) LastSuccess(ctx context.Context) (*Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.rows {
		if row.Status == StatusOK {
			clone := *row
			return &clone, nil
		}
	}
	return nil, nil
}

func (m *memStore) MarkInterrupted(ctx context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, row := range m.rows {
		if row.Status == StatusRunning && row.CreatedAt.Before(olderThan) {
			row.Status = StatusInterrupted
			n++
		}
	}
	m.interrupt = n
	return n, nil
}

func (m *memStore) LoadSchedule(ctx context.Context) (*Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sched == nil {
		return nil, nil
	}
	clone := *m.sched
	return &clone, nil
}

func (m *memStore) SaveSchedule(ctx context.Context, s Schedule, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := s
	m.sched = &clone
	return nil
}

// fakeEngine writes a payload it can read back, and records what it was asked
// to do so the test can prove the ordering of a restore.
type fakeEngine struct {
	name       string
	body       string
	fail       error
	restoreErr error
	events     []string
}

func (e *fakeEngine) Name() string { return e.name }

func (e *fakeEngine) Dump(ctx context.Context, w io.Writer) (DumpResult, error) {
	e.events = append(e.events, "dump")
	if e.fail != nil {
		return DumpResult{}, e.fail
	}
	if _, err := io.WriteString(w, e.body); err != nil {
		return DumpResult{}, err
	}
	return DumpResult{
		Tables:    []string{"resources"},
		RowCounts: map[string]int64{"resources": 2},
		Schema:    []string{"001_resources.sql"},
	}, nil
}

func (e *fakeEngine) Restore(ctx context.Context, r io.Reader, m Manifest) error {
	e.events = append(e.events, "restore")
	if _, err := io.ReadAll(r); err != nil {
		return err
	}
	return e.restoreErr
}

// fakeQuiesce records the drain/allow pair, which is the only proof that a
// restore held the hub still and let it go again.
type fakeQuiesce struct {
	drained bool
	allowed int
	active  int
}

func (q *fakeQuiesce) Drain(ctx context.Context) error { q.drained = true; return nil }
func (q *fakeQuiesce) Allow()                          { q.allowed++ }
func (q *fakeQuiesce) ActiveTaskCount() int            { return q.active }

type fakeReload struct{ calls int }

func (r *fakeReload) Reload(ctx context.Context) error { r.calls++; return nil }

// ---------------------------------------------------------------------------
// service tests
// ---------------------------------------------------------------------------

func newTestService(t *testing.T, engine Engine, store BackupStore) *Service {
	t.Helper()
	return New(Options{
		Dir:    t.TempDir(),
		Engine: engine,
		Store:  store,
		Applied: func(ctx context.Context) ([]string, error) {
			return []string{"001_resources.sql", "007_backups.sql"}, nil
		},
	})
}

func TestServiceDisabledWithoutADirectory(t *testing.T) {
	s := New(Options{Engine: &fakeEngine{name: "fake"}})
	if s.Enabled() {
		t.Fatal("a service without a directory must be disabled")
	}
	ctx := context.Background()
	if _, err := s.Checkpoint(ctx, KindManual, "", "tester"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("checkpoint = %v, want ErrDisabled", err)
	}
	if _, err := s.List(ctx); !errors.Is(err, ErrDisabled) {
		t.Fatalf("list = %v, want ErrDisabled", err)
	}
	if _, err := s.Restore(ctx, "bkp-1", "bkp-1", "tester"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("restore = %v, want ErrDisabled", err)
	}
	if err := s.Delete(ctx, "bkp-1"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("delete = %v, want ErrDisabled", err)
	}
	// Running the scheduler on a disabled service is a no-op, not a failure.
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestServiceCheckpointRecordsTheArchive(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\nrows\n"}
	store := &memStore{}
	s := newTestService(t, engine, store)

	c, err := s.Checkpoint(context.Background(), KindManual, "before the upgrade", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if c.Status != StatusOK || c.Kind != KindManual || c.Engine != "fake" {
		t.Fatalf("checkpoint = %+v", c)
	}
	if c.RowCount != 2 || c.TableCount != 1 {
		t.Fatalf("rows = %d tables = %d, want 2 and 1", c.RowCount, c.TableCount)
	}
	if c.ErrorCode != "" || c.FinishedAt == nil {
		t.Fatalf("a finished checkpoint carries no error code: %+v", c)
	}
	if _, err := os.Stat(c.Path); err != nil {
		t.Fatalf("archive on disk: %v", err)
	}
	// The row's checksum covers the file, and the manifest's covers the
	// payload: both must be present and different.
	m, err := ReadManifest(c.Path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if m.Checksum == "" || c.Checksum == "" || m.Checksum == c.Checksum {
		t.Fatalf("payload checksum %q and file checksum %q must both be set and differ", m.Checksum, c.Checksum)
	}
	rows, err := s.List(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %d rows (%v)", len(rows), err)
	}
}

func TestServiceCheckpointFailureLeavesNoArchive(t *testing.T) {
	engine := &fakeEngine{name: "fake", fail: ErrEngineFailed}
	s := newTestService(t, engine, &memStore{})

	c, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if !errors.Is(err, ErrEngineFailed) {
		t.Fatalf("checkpoint = %v, want the engine failure", err)
	}
	if c == nil || c.Status != StatusFailed {
		t.Fatalf("the row must record the failure: %+v", c)
	}
	if c.ErrorCode != "backup_engine_failed" {
		t.Fatalf("error code = %q, want backup_engine_failed", c.ErrorCode)
	}
	// A failed dump must not leave a file that looks like a checkpoint.
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the backup directory holds %d entries after a failure", len(entries))
	}
}

func TestServiceRestoreInsistsOnConfirmation(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n"}
	store := &memStore{}
	s := newTestService(t, engine, store)

	c, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	ctx := context.Background()
	if _, err := s.Restore(ctx, c.ID, "", "tester"); !errors.Is(err, ErrRestoreConfirm) {
		t.Fatalf("restore without confirmation = %v, want ErrRestoreConfirm", err)
	}
	if _, err := s.Restore(ctx, c.ID, "some-other-id", "tester"); !errors.Is(err, ErrRestoreConfirm) {
		t.Fatalf("restore with the wrong confirmation = %v, want ErrRestoreConfirm", err)
	}
	if _, err := s.Restore(ctx, "bkp-missing", "bkp-missing", "tester"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore of a missing checkpoint = %v, want ErrNotFound", err)
	}
	if len(engine.events) != 1 || engine.events[0] != "dump" {
		t.Fatalf("engine calls = %v, want only the dump", engine.events)
	}
}

func TestServiceRestoreRunsTheGuardrailsInOrder(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\nrows\n"}
	store := &memStore{}
	quiesce := &fakeQuiesce{}
	reload := &fakeReload{}
	s := New(Options{
		Dir:     t.TempDir(),
		Engine:  engine,
		Store:   store,
		Quiesce: quiesce,
		Reload:  reload,
		Applied: func(ctx context.Context) ([]string, error) {
			return []string{"001_resources.sql", "007_backups.sql"}, nil
		},
	})

	target, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := s.Restore(context.Background(), target.ID, target.ID, "tester"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// dump (the target) → dump (the rollback point) → restore: the way back is
	// taken before the database is touched.
	want := []string{"dump", "dump", "restore"}
	if len(engine.events) != len(want) {
		t.Fatalf("engine calls = %v, want %v", engine.events, want)
	}
	for i := range want {
		if engine.events[i] != want[i] {
			t.Fatalf("engine call %d = %q, want %q", i, engine.events[i], want[i])
		}
	}
	if !quiesce.drained {
		t.Fatal("the hub must be drained before the database is rewritten")
	}
	if quiesce.allowed != 1 {
		t.Fatalf("Allow called %d times, want exactly once", quiesce.allowed)
	}
	if reload.calls != 1 {
		t.Fatalf("reload called %d times, want once", reload.calls)
	}

	// The rollback checkpoint is in the catalogue, and the target is marked as
	// the one that was restored.
	rows, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var rollback, restored *Checkpoint
	for i := range rows {
		switch {
		case rows[i].Kind == KindPreRestore:
			rollback = &rows[i]
		case rows[i].ID == target.ID:
			restored = &rows[i]
		}
	}
	if rollback == nil {
		t.Fatal("the pre-restore rollback checkpoint must be in the catalogue")
	}
	if restored == nil || restored.RestoredAt == nil || restored.RestoredBy != "tester" {
		t.Fatalf("the restored checkpoint must be marked: %+v", restored)
	}
}

func TestServiceRestoreReleasesTheHubWhenItFails(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n", restoreErr: ErrEngineFailed}
	store := &memStore{}
	quiesce := &fakeQuiesce{}
	reload := &fakeReload{}
	s := New(Options{
		Dir:     t.TempDir(),
		Engine:  engine,
		Store:   store,
		Quiesce: quiesce,
		Reload:  reload,
		Applied: func(ctx context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
	})
	target, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	if _, err := s.Restore(context.Background(), target.ID, target.ID, "tester"); !errors.Is(err, ErrRestoreFailed) {
		t.Fatalf("restore = %v, want ErrRestoreFailed", err)
	}
	// The hub must answer again even though the restore failed, and the
	// rollback checkpoint taken before the attempt is still on disk.
	if !quiesce.drained {
		t.Fatal("the restore must have drained the hub before it failed")
	}
	if quiesce.allowed != 1 {
		t.Fatalf("Allow called %d times after a failure, want once", quiesce.allowed)
	}
	if reload.calls != 0 {
		t.Fatalf("reload called %d times after a failure, want none", reload.calls)
	}
	rows, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var rollback bool
	for _, row := range rows {
		if row.Kind == KindPreRestore {
			rollback = true
		}
	}
	if !rollback {
		t.Fatal("the rollback checkpoint must survive a failed restore")
	}
}

// A corrupted archive is refused before the hub is touched at all: there is
// nothing to drain and nothing to release.
func TestServiceRestoreChecksTheArchiveBeforeTouchingTheHub(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n"}
	quiesce := &fakeQuiesce{}
	s := New(Options{
		Dir:     t.TempDir(),
		Engine:  engine,
		Store:   &memStore{},
		Quiesce: quiesce,
		Applied: func(ctx context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
	})
	target, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := os.WriteFile(target.Path, []byte("not the archive"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := s.Restore(context.Background(), target.ID, target.ID, "tester"); err == nil {
		t.Fatal("restoring a corrupted archive must fail")
	}
	if quiesce.drained {
		t.Fatal("an archive that fails verification must not drain the hub")
	}
	if len(engine.events) != 1 {
		t.Fatalf("engine calls = %v, want only the dump", engine.events)
	}
}

func TestServiceRestoreRefusesANewerSchema(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n"}
	store := &memStore{}
	s := New(Options{
		Dir:    t.TempDir(),
		Engine: engine,
		Store:  store,
		// The archive knows 009, this build only knows up to 007.
		Applied: func(ctx context.Context) ([]string, error) {
			return []string{"001_resources.sql", "007_backups.sql"}, nil
		},
	})
	c, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Rewrite the manifest inside the archive with a schema this build will
	// not recognise.
	m, err := ReadManifest(c.Path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m.Schema = []string{"001_resources.sql", "009_something_new.sql"}
	stage, err := newStageDump(t.TempDir(), "rewrite")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := stage.Write([]byte("resources\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	payload, size, err := stage.Reader()
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, err := Pack(c.Path, payload, size, m); err != nil {
		t.Fatalf("repack: %v", err)
	}
	_ = stage.Close()
	// The catalogue row's file checksum changed with the rewrite; refresh it so
	// the guard under test is the schema, not the checksum.
	sum, err := FileChecksum(c.Path)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	c.Checksum = sum
	if err := store.Finish(context.Background(), c); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if _, err := s.Restore(context.Background(), c.ID, c.ID, "tester"); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("restore = %v, want ErrSchemaMismatch", err)
	}
}

func TestServicePruneKeepsTheNewestAndTheRollback(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n"}
	store := &memStore{}
	s := newTestService(t, engine, store)
	// Keep one, so everything older is a candidate.
	if _, err := s.SaveSchedule(context.Background(), Schedule{
		Mode: ModeOff, Enabled: false, KeepLast: 1, MaxAgeDays: 1,
		DailyTime: "03:00", Timezone: "UTC",
	}, "tester"); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	ctx := context.Background()
	old, err := s.Checkpoint(ctx, KindManual, "old", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Backdate both rows: one is a plain manual checkpoint, the other the
	// rollback point from a restore.
	store.mu.Lock()
	for _, row := range store.rows {
		row.CreatedAt = row.CreatedAt.Add(-72 * time.Hour)
	}
	store.mu.Unlock()
	_ = old

	rollback, err := s.Checkpoint(ctx, KindPreRestore, "rollback", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := s.Checkpoint(ctx, KindManual, "newest", "tester"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// Pruning rides along with every checkpoint, so the stale row is already
	// gone before Prune is called directly.
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	kept := map[string]bool{}
	for _, row := range rows {
		kept[row.ID] = true
	}
	if !kept[rollback.ID] {
		t.Fatal("a rollback checkpoint inside its grace period must survive pruning")
	}
	if kept[old.ID] {
		t.Fatal("a checkpoint outside the retention window must be pruned")
	}
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pruning a checkpoint must remove its archive")
	}
	// And pruning again changes nothing, which is what the scheduler's
	// housekeeping relies on.
	removed, err := s.Prune(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("prune removed %d checkpoints on a second pass, want 0", removed)
	}
}

func TestServiceDeleteRefusesAPathOutsideTheDirectory(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n"}
	store := &memStore{}
	s := newTestService(t, engine, store)
	c, err := s.Checkpoint(context.Background(), KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// A row whose path was tampered with must not become an arbitrary delete.
	store.mu.Lock()
	store.rows[0].Path = filepath.Join(t.TempDir(), "elsewhere.tar.gz")
	store.mu.Unlock()
	if err := s.Delete(context.Background(), c.ID); err == nil {
		t.Fatal("deleting a checkpoint whose archive escaped the directory must fail")
	}
}

func TestServiceScheduleIsValidatedBeforeItIsStored(t *testing.T) {
	s := newTestService(t, &fakeEngine{name: "fake"}, &memStore{})
	ctx := context.Background()

	bad := Schedule{Mode: ModeCron, Enabled: true, CronExpr: "* * * *", KeepLast: 3, MaxAgeDays: 7}
	if _, err := s.SaveSchedule(ctx, bad, "tester"); !errors.Is(err, ErrCron) {
		t.Fatalf("save an invalid cron = %v, want ErrCron", err)
	}
	good := Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour, KeepLast: 3, MaxAgeDays: 7,
		DailyTime: "03:00", Timezone: "UTC"}
	stored, err := s.SaveSchedule(ctx, good, "tester")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if stored.Mode != ModeInterval {
		t.Fatalf("stored mode = %q", stored.Mode)
	}
	// Nothing has run yet, so an interval schedule is due immediately.
	next, err := s.NextRun(ctx)
	if err != nil {
		t.Fatalf("next run: %v", err)
	}
	if next.After(s.now().Add(time.Second)) {
		t.Fatalf("next run = %v, want it due now", next)
	}
	// Turning it off leaves nothing scheduled.
	if _, err := s.SaveSchedule(ctx, Schedule{Mode: ModeOff, KeepLast: 3, MaxAgeDays: 7}, "tester"); err != nil {
		t.Fatalf("save off: %v", err)
	}
	if _, err := s.NextRun(ctx); !errors.Is(err, ErrNotScheduled) {
		t.Fatalf("next run of an off schedule = %v, want ErrNotScheduled", err)
	}
}

func TestServiceSchedulerTakesOneCheckpointAndStops(t *testing.T) {
	engine := &fakeEngine{name: "fake", body: "resources\n"}
	store := &memStore{}
	s := newTestService(t, engine, store)
	if _, err := s.SaveSchedule(context.Background(), Schedule{
		Mode: ModeInterval, Enabled: true, Interval: time.Hour, KeepLast: 5, MaxAgeDays: 7,
		DailyTime: "03:00", Timezone: "UTC",
	}, "tester"); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// runDue runs once at start-up; give it a moment, then stop the loop.
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := s.List(context.Background())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(rows) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the scheduler did not stop when its context was cancelled")
	}

	rows, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != KindSchedule {
		t.Fatalf("the scheduler produced %+v, want one scheduled checkpoint", rows)
	}
}
