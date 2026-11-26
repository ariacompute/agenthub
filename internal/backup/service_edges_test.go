package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the offline half of the backup coverage: the reachable arms of
// service.go, archive.go, crypto.go and schedule.go that the happy-path tests in
// service_test.go and archive_test.go never enter. The database arms live in
// restore_schema_test.go (isolated schema, exported API) and copy_test.go.
//
// The rule is the same everywhere: no seam is invented for a test. An arm that
// needs an injected failure is reached through a store or engine the test owns;
// an arm that would need the random source or the filesystem to fail in a way it
// cannot is left uncovered and listed in task.md instead of being faked.

// errBackupStore layers injected failures over memStore. Every hook is optional,
// so a case arms one and the rest of the catalogue keeps working.
type errBackupStore struct {
	*memStore
	createErr error
	// finishHook lets a case fail Finish for one row only: a restore writes the
	// target row after the rollback row, and the two must be told apart.
	finishHook func(*Checkpoint) error
	getErr     error
	listErr    error
	deleteErr  error
	lastErr    error
	loadErr    error
	saveErr    error
	markErr    error
}

func (s *errBackupStore) Create(ctx context.Context, c *Checkpoint) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.memStore.Create(ctx, c)
}

func (s *errBackupStore) Finish(ctx context.Context, c *Checkpoint) error {
	if s.finishHook != nil {
		if err := s.finishHook(c); err != nil {
			return err
		}
	}
	return s.memStore.Finish(ctx, c)
}

func (s *errBackupStore) Get(ctx context.Context, id string) (*Checkpoint, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memStore.Get(ctx, id)
}

func (s *errBackupStore) List(ctx context.Context, limit int) ([]Checkpoint, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memStore.List(ctx, limit)
}

func (s *errBackupStore) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memStore.Delete(ctx, id)
}

func (s *errBackupStore) LastSuccess(ctx context.Context) (*Checkpoint, error) {
	if s.lastErr != nil {
		return nil, s.lastErr
	}
	return s.memStore.LastSuccess(ctx)
}

func (s *errBackupStore) LoadSchedule(ctx context.Context) (*Schedule, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.memStore.LoadSchedule(ctx)
}

func (s *errBackupStore) SaveSchedule(ctx context.Context, sched Schedule, actor string) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.memStore.SaveSchedule(ctx, sched, actor)
}

func (s *errBackupStore) MarkInterrupted(ctx context.Context, olderThan time.Time) (int64, error) {
	if s.markErr != nil {
		return 0, s.markErr
	}
	return s.memStore.MarkInterrupted(ctx, olderThan)
}

type errQuiesce struct {
	drainErr error
	allowed  int
}

func (q *errQuiesce) Drain(context.Context) error { return q.drainErr }
func (q *errQuiesce) Allow()                      { q.allowed++ }
func (q *errQuiesce) ActiveTaskCount() int        { return 3 }

var errInjected = errors.New("injected failure")

func TestReloadGroupRunsInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	ctx := context.Background()

	var order []string
	first := ReloaderFunc(func(context.Context) error { order = append(order, "first"); return nil })
	second := ReloaderFunc(func(context.Context) error { order = append(order, "second"); return nil })
	third := ReloaderFunc(func(context.Context) error { order = append(order, "third"); return nil })

	// A nil member is a caller who had nothing to wire, not a panic.
	if err := Reloaders(first, nil, second).Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("order = %v, want first,second", order)
	}

	// A failure stops the group: rebuilding half of the in-memory state after a
	// restore is worse than rebuilding none of it.
	order = nil
	broken := ReloaderFunc(func(context.Context) error { return errInjected })
	err := Reloaders(first, broken, third).Reload(ctx)
	if !errors.Is(err, errInjected) {
		t.Fatalf("reload = %v, want the injected failure", err)
	}
	if strings.Join(order, ",") != "first" {
		t.Fatalf("order = %v, want the group to stop at the failure", order)
	}

	if err := Reloaders().Reload(ctx); err != nil {
		t.Fatalf("an empty group must reload nothing: %v", err)
	}
}

func TestServiceQuiesceAndClock(t *testing.T) {
	s := newTestService(t, &fakeEngine{name: "fake"}, &memStore{})
	if q, ok := s.Quiesce(); ok || q != nil {
		t.Fatal("a service without a barrier must report none")
	}

	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("UTC+7", 7*3600))
	s = New(Options{
		Dir:     t.TempDir(),
		Engine:  &fakeEngine{name: "fake"},
		Store:   &memStore{},
		Quiesce: &errQuiesce{},
		Now:     func() time.Time { return fixed },
	})
	q, ok := s.Quiesce()
	if !ok || q == nil {
		t.Fatal("a configured barrier must be reported")
	}
	// now() is UTC by contract: the catalogue stores UTC and the console renders
	// it in the reader's zone.
	if got := s.now(); got.Location() != time.UTC || !got.Equal(fixed) {
		t.Fatalf("now() = %v, want the injected instant in UTC", got)
	}
	if s.Dir() == "" {
		t.Fatal("Dir must report the archive directory")
	}
	if s.EngineName() != "fake" {
		t.Fatalf("engine name = %q", s.EngineName())
	}
	if name := New(Options{}).EngineName(); name != "" {
		t.Fatalf("engine name without an engine = %q, want empty", name)
	}
	if q.ActiveTaskCount() != 3 {
		t.Fatalf("active tasks = %d", q.ActiveTaskCount())
	}
}

func TestCheckpointDefaultsTheKindAndRefusesWhenBusy(t *testing.T) {
	store := &memStore{}
	s := newTestService(t, &fakeEngine{name: "fake", body: "rows\n"}, store)

	c, err := s.Checkpoint(context.Background(), "", "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if c.Kind != KindManual {
		t.Fatalf("kind = %q, want the manual default", c.Kind)
	}

	s.op.Lock()
	defer s.op.Unlock()
	if _, err := s.Checkpoint(context.Background(), KindManual, "", "tester"); !errors.Is(err, ErrBusy) {
		t.Fatalf("checkpoint while busy = %v, want ErrBusy", err)
	}
	// The restore shares the same lock, so two database rewrites can never
	// overlap.
	if _, err := s.Restore(context.Background(), c.ID, c.ID, "tester"); !errors.Is(err, ErrBusy) {
		t.Fatalf("restore while busy = %v, want ErrBusy", err)
	}
}

func TestCheckpointStoreFailures(t *testing.T) {
	dir := t.TempDir()
	store := &errBackupStore{memStore: &memStore{}, createErr: errInjected}
	s := New(Options{Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: store})

	if _, err := s.Checkpoint(context.Background(), KindManual, "", "tester"); !errors.Is(err, errInjected) {
		t.Fatalf("checkpoint with a failing catalogue = %v, want the injected failure", err)
	}
	// Nothing reached the disk: the row is written before the dump so a run that
	// never started is not mistaken for a finished one.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the archive directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a checkpoint that could not even be recorded left %d file(s) behind", len(entries))
	}

	// A row that cannot be updated is reported: the console would otherwise show
	// a checkpoint as still running forever.
	store.createErr = nil
	store.finishHook = func(c *Checkpoint) error { return errInjected }
	if c, err := s.Checkpoint(context.Background(), KindManual, "", "tester"); !errors.Is(err, errInjected) {
		t.Fatalf("checkpoint with a failing catalogue update = %v (row %+v)", err, c)
	}

	// Pruning is housekeeping: a policy that cannot be read must not turn a
	// checkpoint that is already on disk into a failure.
	store.finishHook = nil
	store.loadErr = errInjected
	if _, err := s.Checkpoint(context.Background(), KindManual, "", "tester"); err != nil {
		t.Fatalf("prune failure must not fail the checkpoint: %v", err)
	}
}

func TestCheckpointEncryptsAtRestAndDropsThePlaintext(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	dir := t.TempDir()
	s := New(Options{Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: &memStore{}, Key: key})

	c, err := s.Checkpoint(context.Background(), KindManual, "nightly", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !strings.HasSuffix(c.Path, ".enc") {
		t.Fatalf("path = %q, want an .enc file", c.Path)
	}
	if _, err := os.Stat(strings.TrimSuffix(c.Path, ".enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext archive is still on disk: %v", err)
	}
	sum, err := FileChecksum(c.Path)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if c.Checksum != sum {
		t.Fatalf("row checksum = %q, file checksum = %q", c.Checksum, sum)
	}
	// The companion manifest mirrors sha256sum so an operator can verify the
	// ciphertext with the tools they already have.
	hash, err := os.ReadFile(c.Path + ".sha256")
	if err != nil {
		t.Fatalf("read the hash manifest: %v", err)
	}
	if want := sum + "  " + filepath.Base(c.Path) + "\n"; string(hash) != want {
		t.Fatalf("hash manifest = %q, want %q", hash, want)
	}
	// An encrypted row is still downloadable: the service decrypts on the way out.
	if _, err := s.Path(context.Background(), c.ID); err != nil {
		t.Fatalf("path of an encrypted checkpoint: %v", err)
	}
}

func TestReadPathsWithoutACatalogue(t *testing.T) {
	ctx := context.Background()
	s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake"}})

	if rows, err := s.List(ctx); err != nil || rows != nil {
		t.Fatalf("list without a catalogue = %v, %v", rows, err)
	}
	if _, err := s.Get(ctx, "bkp-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get without a catalogue = %v, want ErrNotFound", err)
	}
	if _, err := s.Path(ctx, "bkp-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("path without a catalogue = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "bkp-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete without a catalogue = %v, want ErrNotFound", err)
	}
	if _, err := s.Restore(ctx, "bkp-1", "bkp-1", "tester"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore without a catalogue = %v, want ErrNotFound", err)
	}

	// The schedule falls back to the defaults nobody has overridden yet.
	sched, err := s.Schedule(ctx)
	if err != nil || sched.Mode != ModeOff {
		t.Fatalf("schedule without a catalogue = %+v, %v", sched, err)
	}
	valid := DefaultSchedule()
	valid.Mode = ModeInterval
	valid.Enabled = true
	valid.Interval = time.Hour
	if got, err := s.SaveSchedule(ctx, valid, "tester"); err != nil || got.Mode != ModeInterval {
		t.Fatalf("save without a catalogue = %+v, %v", got, err)
	}
	if err := s.SeedSchedule(ctx, valid, "tester"); err != nil {
		t.Fatalf("seed without a catalogue: %v", err)
	}
	if _, err := s.NextRun(ctx); !errors.Is(err, ErrNotScheduled) {
		t.Fatalf("next run of the default policy = %v, want ErrNotScheduled", err)
	}
}

func TestPathAndDeleteGuards(t *testing.T) {
	ctx := context.Background()
	store := &memStore{}
	dir := t.TempDir()
	s := New(Options{Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: store})

	c, err := s.Checkpoint(ctx, KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if got, err := s.Path(ctx, c.ID); err != nil || got != c.Path {
		t.Fatalf("path = %q, %v; want %q", got, err, c.Path)
	}

	// A row that never finished has nothing to download.
	running := *c
	running.ID = "bkp-running"
	running.Status = StatusRunning
	if err := store.Create(ctx, &running); err != nil {
		t.Fatalf("seed running row: %v", err)
	}
	if _, err := s.Path(ctx, running.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("path of a running checkpoint = %v, want ErrNotFound", err)
	}

	// An archive that lost its file is reported, not handed over.
	lost := *c
	lost.ID = "bkp-lost"
	lost.Path = filepath.Join(dir, "gone.tar.gz")
	lost.Checksum = strings.Repeat("0", 64)
	if err := store.Create(ctx, &lost); err != nil {
		t.Fatalf("seed lost row: %v", err)
	}
	if _, err := s.Path(ctx, lost.ID); err == nil {
		t.Fatal("path of a missing archive must fail")
	}

	// A path outside the archive directory never becomes an rm target.
	outside := *c
	outside.ID = "bkp-outside"
	outside.Path = filepath.Join(t.TempDir(), "elsewhere.tar.gz")
	if err := store.Create(ctx, &outside); err != nil {
		t.Fatalf("seed outside row: %v", err)
	}
	if err := s.Delete(ctx, outside.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of an outside path = %v, want ErrNotFound", err)
	}

	// A file that cannot be removed is reported rather than silently skipped.
	// A non empty directory stands in for the permission failure, so the case
	// works for every user including root.
	blocked := *c
	blocked.ID = "bkp-blocked"
	blocked.Path = filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked.Path, "child"), 0o755); err != nil {
		t.Fatalf("stage the blocked archive: %v", err)
	}
	if err := store.Create(ctx, &blocked); err != nil {
		t.Fatalf("seed blocked row: %v", err)
	}
	if err := s.Delete(ctx, blocked.ID); err == nil {
		t.Fatal("delete must report an archive it cannot remove")
	}

	// The companion hash manifest is removed with the archive, and a manifest
	// that cannot go is reported too.
	hashBlocked := *c
	hashBlocked.ID = "bkp-hash-blocked"
	hashBlocked.Path = filepath.Join(dir, "hash-blocked.tar.gz")
	if err := os.WriteFile(hashBlocked.Path, []byte("archive"), 0o600); err != nil {
		t.Fatalf("stage the hash-blocked archive: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hashBlocked.Path+".sha256", "child"), 0o755); err != nil {
		t.Fatalf("stage the hash manifest: %v", err)
	}
	if err := store.Create(ctx, &hashBlocked); err != nil {
		t.Fatalf("seed hash-blocked row: %v", err)
	}
	if err := s.Delete(ctx, hashBlocked.ID); err == nil {
		t.Fatal("delete must report a hash manifest it cannot remove")
	}

	// A catalogue delete that fails is the last word.
	failing := &errBackupStore{memStore: store, deleteErr: errInjected}
	s2 := New(Options{Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: failing})
	if err := s2.Delete(ctx, c.ID); !errors.Is(err, errInjected) {
		t.Fatalf("delete with a failing catalogue = %v, want the injected failure", err)
	}

	// And a row without a path only needs the catalogue delete.
	noPath := *c
	noPath.ID = "bkp-no-path"
	noPath.Path = ""
	noPath.Checksum = ""
	if err := store.Create(ctx, &noPath); err != nil {
		t.Fatalf("seed pathless row: %v", err)
	}
	if err := s.Delete(ctx, noPath.ID); err != nil {
		t.Fatalf("delete of a pathless row: %v", err)
	}
}

func TestRestoreRefusesAnArchiveItCannotTrust(t *testing.T) {
	ctx := context.Background()
	store := &memStore{}
	s := New(Options{
		Dir:     t.TempDir(),
		Engine:  &fakeEngine{name: "fake", body: "rows\n"},
		Store:   store,
		Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
	})

	target, err := s.Checkpoint(ctx, KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// A row that is not a finished checkpoint is not a restore target.
	running := *target
	running.ID = "bkp-running"
	running.Status = StatusRunning
	if err := store.Create(ctx, &running); err != nil {
		t.Fatalf("seed running row: %v", err)
	}
	if _, err := s.Restore(ctx, running.ID, running.ID, "tester"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore of a running checkpoint = %v, want ErrNotFound", err)
	}

	// The archive on disk must still be the one that was written.
	lost := *target
	lost.ID = "bkp-lost"
	lost.Path = filepath.Join(t.TempDir(), "gone.tar.gz")
	if err := store.Create(ctx, &lost); err != nil {
		t.Fatalf("seed lost row: %v", err)
	}
	if _, err := s.Restore(ctx, lost.ID, lost.ID, "tester"); err == nil {
		t.Fatal("restore of a missing archive must fail")
	}

	// Bytes that are not a checkpoint are refused before anything is written.
	junkPath := filepath.Join(t.TempDir(), "junk.tar.gz")
	if err := os.WriteFile(junkPath, []byte("not a gzip stream"), 0o600); err != nil {
		t.Fatalf("stage junk: %v", err)
	}
	junk := *target
	junk.ID = "bkp-junk"
	junk.Path = junkPath
	junk.Checksum = ""
	if err := store.Create(ctx, &junk); err != nil {
		t.Fatalf("seed junk row: %v", err)
	}
	if _, err := s.Restore(ctx, junk.ID, junk.ID, "tester"); !errors.Is(err, ErrArchive) {
		t.Fatalf("restore of a junk archive = %v, want ErrArchive", err)
	}

	// An encrypted row needs the key that wrote it.
	encPath := junkPath + ".enc"
	if err := os.WriteFile(encPath, []byte("short"), 0o600); err != nil {
		t.Fatalf("stage the encrypted payload: %v", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	encrypted := New(Options{
		Dir:     t.TempDir(),
		Engine:  &fakeEngine{name: "fake", body: "rows\n"},
		Store:   store,
		Key:     key,
		Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
	})
	enc := *target
	enc.ID = "bkp-encrypted-junk"
	enc.Path = encPath
	enc.Checksum = ""
	if err := store.Create(ctx, &enc); err != nil {
		t.Fatalf("seed encrypted row: %v", err)
	}
	if _, err := encrypted.Restore(ctx, enc.ID, enc.ID, "tester"); err == nil {
		t.Fatal("restore of an undecryptable archive must fail")
	}

	// An archive that knows a migration this build does not is the dangerous
	// direction and must be refused.
	ahead := New(Options{
		Dir:     t.TempDir(),
		Engine:  &fakeEngine{name: "fake", body: "rows\n"},
		Store:   store,
		Applied: func(context.Context) ([]string, error) { return []string{"002_tasks.sql"}, nil },
	})
	if _, err := ahead.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("restore of an archive from a newer build = %v, want ErrSchemaMismatch", err)
	}

	// A build that cannot report its own migrations cannot judge the archive.
	blind := New(Options{
		Dir:     t.TempDir(),
		Engine:  &fakeEngine{name: "fake", body: "rows\n"},
		Store:   store,
		Applied: func(context.Context) ([]string, error) { return nil, errInjected },
	})
	if _, err := blind.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, errInjected) {
		t.Fatalf("restore with an unreadable schema = %v, want the injected failure", err)
	}
}

func TestRestoreWithoutAnAppliedHookKeepsGoing(t *testing.T) {
	ctx := context.Background()
	engine := &fakeEngine{name: "fake", body: "rows\n"}
	s := New(Options{Dir: t.TempDir(), Engine: engine, Store: &memStore{}})

	target, err := s.Checkpoint(ctx, KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Booting without the migration list is a supported configuration: the
	// archive is then trusted as it stands.
	if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); err != nil {
		t.Fatalf("restore without an applied hook: %v", err)
	}
}

func TestRestoreFailureArms(t *testing.T) {
	ctx := context.Background()

	t.Run("rollback checkpoint fails", func(t *testing.T) {
		engine := &fakeEngine{name: "fake", body: "rows\n"}
		s := newTestService(t, engine, &memStore{})
		target, err := s.Checkpoint(ctx, KindManual, "", "tester")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		// The way back is part of the operation: without it the restore is
		// refused before anything is drained.
		engine.fail = ErrEngineFailed
		if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, ErrRestoreFailed) {
			t.Fatalf("restore = %v, want ErrRestoreFailed", err)
		}
	})

	t.Run("drain fails", func(t *testing.T) {
		engine := &fakeEngine{name: "fake", body: "rows\n"}
		quiesce := &errQuiesce{drainErr: errInjected}
		s := New(Options{Dir: t.TempDir(), Engine: engine, Store: &memStore{}, Quiesce: quiesce,
			Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil }})
		target, err := s.Checkpoint(ctx, KindManual, "", "tester")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, ErrRestoreFailed) {
			t.Fatalf("restore = %v, want ErrRestoreFailed", err)
		}
		// The barrier was never lowered, so there is nothing to lift.
		if quiesce.allowed != 0 {
			t.Fatalf("Allow called %d times after a failed drain, want none", quiesce.allowed)
		}
	})

	t.Run("migrate after restore fails", func(t *testing.T) {
		s := New(Options{
			Dir:     t.TempDir(),
			Engine:  &fakeEngine{name: "fake", body: "rows\n"},
			Store:   &memStore{},
			Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
			Migrate: func() error { return errInjected },
		})
		target, err := s.Checkpoint(ctx, KindManual, "", "tester")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, ErrRestoreFailed) {
			t.Fatalf("restore = %v, want ErrRestoreFailed", err)
		}
	})

	t.Run("reload after restore fails", func(t *testing.T) {
		s := New(Options{
			Dir:     t.TempDir(),
			Engine:  &fakeEngine{name: "fake", body: "rows\n"},
			Store:   &memStore{},
			Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
			Reload:  ReloaderFunc(func(context.Context) error { return errInjected }),
		})
		target, err := s.Checkpoint(ctx, KindManual, "", "tester")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, ErrRestoreFailed) {
			t.Fatalf("restore = %v, want ErrRestoreFailed", err)
		}
	})

	t.Run("marking the target restored fails", func(t *testing.T) {
		store := &errBackupStore{memStore: &memStore{}}
		s := New(Options{
			Dir:     t.TempDir(),
			Engine:  &fakeEngine{name: "fake", body: "rows\n"},
			Store:   store,
			Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
		})
		target, err := s.Checkpoint(ctx, KindManual, "", "tester")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		// Only the manual row fails to be updated: the rollback row must still be
		// written or the restore would stop earlier, at the rollback step.
		store.finishHook = func(c *Checkpoint) error {
			if c.Kind == KindManual {
				return errInjected
			}
			return nil
		}
		if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); !errors.Is(err, errInjected) {
			t.Fatalf("restore = %v, want the injected failure", err)
		}
	})

	t.Run("the payload is missing from the archive", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "manifest-only.tar.gz")
		// A readable manifest with no data entry: the restore may proceed past
		// the schema check, so this is the arm that proves the payload itself is
		// verified before the engine is handed anything.
		writeManifestOnlyArchive(t, path, Manifest{
			Version:   archiveVersion,
			Engine:    "fake",
			Kind:      KindManual,
			CreatedAt: time.Now().UTC(),
			Tables:    []string{"resources"},
			RowCounts: map[string]int64{"resources": 1},
			Schema:    []string{"001_resources.sql"},
		})
		sum, err := FileChecksum(path)
		if err != nil {
			t.Fatalf("checksum: %v", err)
		}
		store := &memStore{}
		row := &Checkpoint{
			ID: "bkp-manifest-only", Kind: KindManual, Engine: "fake", Status: StatusOK,
			Path: path, Checksum: sum, CreatedAt: time.Now().UTC(),
		}
		if err := store.Create(ctx, row); err != nil {
			t.Fatalf("seed: %v", err)
		}
		s := New(Options{
			Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: store,
			Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil },
		})
		if _, err := s.Restore(ctx, row.ID, row.ID, "tester"); !errors.Is(err, ErrRestoreFailed) {
			t.Fatalf("restore = %v, want ErrRestoreFailed", err)
		}
	})
}

// writeManifestOnlyArchive writes a gzip'd tar that carries a manifest but no
// payload — the shape a truncated transfer leaves behind. Pack cannot produce it
// (it always writes the data entry), so the archive is built by hand.
func writeManifestOnlyArchive(t *testing.T, path string, m Manifest) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the archive: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal the manifest: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: manifestEntry, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now(),
	}); err != nil {
		t.Fatalf("write the manifest header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	for _, closer := range []io.Closer{tw, gz, file} {
		if err := closer.Close(); err != nil {
			t.Fatalf("close the archive: %v", err)
		}
	}
}

func TestPruneRetentionArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	if n, err := New(Options{}).Prune(ctx); err != nil || n != 0 {
		t.Fatalf("prune without a catalogue = %d, %v", n, err)
	}

	store := &errBackupStore{memStore: &memStore{}}
	s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: store,
		Now: func() time.Time { return now }})

	store.loadErr = errInjected
	if _, err := s.Prune(ctx); !errors.Is(err, errInjected) {
		t.Fatalf("prune with an unreadable policy = %v, want the injected failure", err)
	}
	store.loadErr = nil
	store.listErr = errInjected
	if _, err := s.Prune(ctx); !errors.Is(err, errInjected) {
		t.Fatalf("prune with an unreadable catalogue = %v, want the injected failure", err)
	}
	store.listErr = nil

	// A policy that keeps one row: everything older goes, the newest stays.
	if err := store.SaveSchedule(ctx, Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour,
		KeepLast: 1, MaxAgeDays: 1}, "tester"); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.tar.gz")
	for i, row := range []Checkpoint{
		{ID: "newest", Kind: KindManual, Status: StatusOK, CreatedAt: now.Add(-time.Minute)},
		{ID: "old-ok", Kind: KindManual, Status: StatusOK, CreatedAt: now.AddDate(0, 0, -30)},
		{ID: "old-blocked", Kind: KindManual, Status: StatusOK, Path: outside, CreatedAt: now.AddDate(0, 0, -31)},
		{ID: "still-running", Kind: KindManual, Status: StatusRunning, CreatedAt: now.AddDate(0, 0, -40)},
		{ID: "recent-rollback", Kind: KindPreRestore, Status: StatusOK, CreatedAt: now.Add(-time.Minute)},
	} {
		row := row
		if err := store.Create(ctx, &row); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	removed, err := s.Prune(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	// old-ok is deleted; old-blocked is outside the directory so its delete
	// fails and is only warned about; the running row is not swept at all.
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	rows, err := store.List(ctx, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) != 4 {
		t.Fatalf("rows after prune = %v, want the newest, the rollback, the running row and the blocked one", ids)
	}
}

func TestRunDueStates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)

	newService := func(store BackupStore, engine Engine) *Service {
		return New(Options{Dir: t.TempDir(), Engine: engine, Store: store, Now: func() time.Time { return now }})
	}

	t.Run("nothing scheduled", func(t *testing.T) {
		store := &memStore{}
		if err := newService(store, &fakeEngine{name: "fake", body: "rows\n"}).runDue(ctx); err != nil {
			t.Fatalf("runDue = %v", err)
		}
		if len(store.rows) != 0 {
			t.Fatalf("a policy that is off took %d checkpoint(s)", len(store.rows))
		}
	})

	t.Run("policy cannot be read", func(t *testing.T) {
		store := &errBackupStore{memStore: &memStore{}, loadErr: errInjected}
		// A broken policy is logged, not retried forever: the scheduler keeps
		// running so the next save is picked up.
		if err := newService(store, &fakeEngine{name: "fake", body: "rows\n"}).runDue(ctx); err != nil {
			t.Fatalf("runDue = %v", err)
		}
	})

	t.Run("due now", func(t *testing.T) {
		store := &memStore{sched: &Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour,
			KeepLast: 3, MaxAgeDays: 7}}
		if err := newService(store, &fakeEngine{name: "fake", body: "rows\n"}).runDue(ctx); err != nil {
			t.Fatalf("runDue = %v", err)
		}
		if len(store.rows) != 1 || store.rows[0].Kind != KindSchedule {
			t.Fatalf("rows = %+v, want one scheduled checkpoint", store.rows)
		}
	})

	t.Run("not due yet", func(t *testing.T) {
		last := now.Add(-time.Minute)
		store := &memStore{
			sched: &Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour, KeepLast: 3, MaxAgeDays: 7},
			rows:  []*Checkpoint{{ID: "last", Kind: KindManual, Status: StatusOK, CreatedAt: last}},
		}
		if err := newService(store, &fakeEngine{name: "fake", body: "rows\n"}).runDue(ctx); err != nil {
			t.Fatalf("runDue = %v", err)
		}
		if len(store.rows) != 1 {
			t.Fatalf("rows = %d, want the existing one only", len(store.rows))
		}
	})

	t.Run("checkpoint fails", func(t *testing.T) {
		store := &memStore{sched: &Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour,
			KeepLast: 3, MaxAgeDays: 7}}
		err := newService(store, &fakeEngine{name: "fake", fail: ErrEngineFailed}).runDue(ctx)
		if !errors.Is(err, ErrEngineFailed) {
			t.Fatalf("runDue = %v, want the engine failure to surface", err)
		}
	})

	t.Run("already busy is not a failure", func(t *testing.T) {
		s := newService(&memStore{sched: &Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour,
			KeepLast: 3, MaxAgeDays: 7}}, &fakeEngine{name: "fake", body: "rows\n"})
		s.op.Lock()
		defer s.op.Unlock()
		// The scheduler waking up while a manual checkpoint runs is normal.
		if err := s.runDue(ctx); err != nil {
			t.Fatalf("runDue while busy = %v", err)
		}
	})
}

func TestRunClosesInterruptedRows(t *testing.T) {
	// Run loops until the context ends; a context that is already done makes it
	// take one pass and return, which is all the test needs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("the catalogue cannot be updated", func(t *testing.T) {
		store := &errBackupStore{memStore: &memStore{}, markErr: errInjected}
		s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake"}, Store: store})
		if err := s.Run(ctx); err != nil {
			t.Fatalf("run: %v", err)
		}
	})

	t.Run("a stale running row is closed", func(t *testing.T) {
		store := &memStore{rows: []*Checkpoint{{
			ID: "stale", Kind: KindManual, Status: StatusRunning,
			CreatedAt: time.Now().UTC().Add(-24 * time.Hour),
		}}}
		s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake"}, Store: store})
		if err := s.Run(ctx); err != nil {
			t.Fatalf("run: %v", err)
		}
		if store.interrupt != 1 {
			t.Fatalf("interrupted rows = %d, want 1", store.interrupt)
		}
	})
}

func TestWriteArchiveHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := writeArchiveHash(path); err != nil {
		t.Fatalf("write hash: %v", err)
	}
	body, err := os.ReadFile(path + ".sha256")
	if err != nil {
		t.Fatalf("read hash: %v", err)
	}
	sum, err := FileChecksum(path)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if string(body) != sum+"  archive.tar.gz\n" {
		t.Fatalf("hash file = %q", body)
	}

	// A missing archive has no hash to write.
	if err := writeArchiveHash(filepath.Join(dir, "gone.tar.gz")); err == nil {
		t.Fatal("write hash of a missing archive must fail")
	}
}

func TestArchiveHelpers(t *testing.T) {
	// Rows is the sum the console shows next to a checkpoint.
	if got := (DumpResult{RowCounts: map[string]int64{"a": 3, "b": 4}}).Rows(); got != 7 {
		t.Fatalf("Rows = %d, want 7", got)
	}
	if got := (DumpResult{}).Rows(); got != 0 {
		t.Fatalf("Rows of an empty dump = %d", got)
	}

	if got := shortHex("  abcdef0123456789  "); got != "abcdef012345" {
		t.Fatalf("shortHex = %q", got)
	}
	if got := shortHex(" short "); got != "short" {
		t.Fatalf("shortHex of a short sum = %q", got)
	}
	if got := shortID("  abc  "); got != "abc" {
		t.Fatalf("shortID = %q", got)
	}
	if got := shortID("0123456789abcdef"); got != "89abcdef" {
		t.Fatalf("shortID of a long id = %q, want the last eight characters", got)
	}

	// A staged dump reports its size and rewinds for packing.
	dir := t.TempDir()
	stage, err := newStageDump(dir, "payload")
	if err != nil {
		t.Fatalf("new stage: %v", err)
	}
	if _, err := stage.Write([]byte("payload")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if size, err := stage.Size(); err != nil || size != int64(len("payload")) {
		t.Fatalf("size = %d, %v", size, err)
	}
	reader, size, err := stage.Reader()
	if err != nil || size != int64(len("payload")) {
		t.Fatalf("reader = %v, %d, %v", reader, size, err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "payload" {
		t.Fatalf("reader body = %q, %v", body, err)
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(stage.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("close left the staged file behind: %v", err)
	}

	// A directory that cannot be created is reported, not ignored.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the blocker: %v", err)
	}
	if _, err := newStageDump(blocked, "payload"); err == nil {
		t.Fatal("staging under a regular file must fail")
	}

	// A closed stage cannot answer for its size, and saying so beats reporting
	// zero: a zero-length payload would produce an archive that restores nothing.
	closed, err := newStageDump(t.TempDir(), "payload")
	if err != nil {
		t.Fatalf("new stage: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := closed.Size(); err == nil {
		t.Fatal("Size of a closed stage must fail")
	}
	if _, _, err := closed.Reader(); err == nil {
		t.Fatal("Reader of a closed stage must fail")
	}
}

func TestSelectEngineFallsBackWhenTheClientIsMissing(t *testing.T) {
	original := lookPath
	t.Cleanup(func() { lookPath = original })
	lookPath = func(string) (string, error) { return "", errInjected }

	// Asking for the client by name is asking for the client: a machine that
	// does not have it must say so rather than silently dump through COPY.
	if _, err := SelectEngine("postgres://user@host/db", EnginePgDump); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("select pg_dump without the binary = %v, want ErrEngineUnavailable", err)
	}
	// Being willing to use it is different: auto falls back to the COPY engine.
	engine, err := SelectEngine("postgres://user@host/db", "")
	if err != nil || engine.Name() != EngineNative {
		t.Fatalf("auto selection = %v, %v; want the native engine", engine, err)
	}
	if engine, err := SelectEngine("postgres://user@host/db", EngineNative); err != nil || engine.Name() != EngineNative {
		t.Fatalf("explicit native selection = %v, %v", engine, err)
	}
}

func TestCryptoEdges(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}

	if err := EncryptFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out.enc"), key); err == nil {
		t.Fatal("encrypting a missing file must fail")
	}
	src := filepath.Join(dir, "plain")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := EncryptFile(src, filepath.Join(dir, "no-such-dir", "out.enc"), key); err == nil {
		t.Fatal("encrypting into a missing directory must fail")
	}

	// A ciphertext shorter than its nonce is refused before the AEAD sees it.
	short := filepath.Join(dir, "short.enc")
	if err := os.WriteFile(short, []byte("abc"), 0o600); err != nil {
		t.Fatalf("stage the short ciphertext: %v", err)
	}
	dst := filepath.Join(dir, "short.out")
	if err := DecryptFile(short, dst, key); err == nil {
		t.Fatal("decrypting a truncated ciphertext must fail")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed decrypt wrote %s", dst)
	}

	// A wrong key fails the AEAD tag instead of returning garbage.
	enc := filepath.Join(dir, "good.enc")
	if err := EncryptFile(src, enc, key); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	other := make([]byte, 32)
	if _, err := rand.Read(other); err != nil {
		t.Fatalf("second key: %v", err)
	}
	if err := DecryptFile(enc, filepath.Join(dir, "wrong.out"), other); err == nil {
		t.Fatal("decrypting with the wrong key must fail")
	}

	// And the round trip still works.
	round := filepath.Join(dir, "round.out")
	if err := DecryptFile(enc, round, key); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	got, err := os.ReadFile(round)
	if err != nil || string(got) != "payload" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
}

func TestScheduleEdges(t *testing.T) {
	for _, tc := range []struct {
		value        string
		hour, minute int
		ok           bool
	}{
		{"03:05", 3, 5, true},
		{"3:5", 3, 5, true},
		{"23:59", 23, 59, true},
		{"24:00", 0, 0, false},
		{"03:60", 0, 0, false},
		{"03", 0, 0, false},
		{"x:00", 0, 0, false},
		{"03:x", 0, 0, false},
		{"", 0, 0, false},
	} {
		hour, minute, err := ParseDailyTime(tc.value)
		if tc.ok {
			if err != nil || hour != tc.hour || minute != tc.minute {
				t.Fatalf("ParseDailyTime(%q) = %d, %d, %v", tc.value, hour, minute, err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidTime) {
			t.Fatalf("ParseDailyTime(%q) = %v, want ErrInvalidTime", tc.value, err)
		}
	}

	from := time.Date(2026, 7, 8, 9, 10, 0, 0, time.UTC)
	last := from.Add(-10 * time.Minute)
	for _, tc := range []struct {
		name  string
		sched Schedule
		last  *time.Time
		want  error
	}{
		{"disabled", Schedule{Mode: ModeInterval, Interval: time.Hour}, nil, ErrNotScheduled},
		{"off mode", Schedule{Enabled: true, Mode: ModeOff}, nil, ErrNotScheduled},
		{"unknown zone", Schedule{Enabled: true, Mode: ModeInterval, Interval: time.Hour, Timezone: "Mars/Olympus"}, nil, ErrTimezone},
		{"interval too short", Schedule{Enabled: true, Mode: ModeInterval, Interval: time.Second}, nil, ErrInterval},
		{"daily time invalid", Schedule{Enabled: true, Mode: ModeDaily, DailyTime: "99:99"}, nil, ErrInvalidTime},
		{"cron empty", Schedule{Enabled: true, Mode: ModeCron}, nil, ErrCron},
		{"cron invalid", Schedule{Enabled: true, Mode: ModeCron, CronExpr: "every day"}, nil, ErrCron},
		{"unknown mode", Schedule{Enabled: true, Mode: "hourly"}, nil, ErrInvalidMode},
	} {
		if _, err := tc.sched.Next(from, tc.last); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	// The interval anchors on the last successful run, so a restart does not
	// shorten the period.
	sched := Schedule{Enabled: true, Mode: ModeInterval, Interval: time.Hour}
	if next, err := sched.Next(from, nil); err != nil || !next.Equal(from) {
		t.Fatalf("first run = %v, %v; want now", next, err)
	}
	if next, err := sched.Next(from, &last); err != nil || !next.Equal(last.Add(time.Hour)) {
		t.Fatalf("next run = %v, %v; want the interval anchored on the last run", next, err)
	}
	longAgo := from.Add(-72 * time.Hour)
	if next, err := sched.Next(from, &longAgo); err != nil || !next.Equal(from) {
		t.Fatalf("after a long downtime = %v, %v; want one catch-up run now", next, err)
	}

	// A daily policy rolls over to tomorrow once today's slot has passed.
	daily := Schedule{Enabled: true, Mode: ModeDaily, DailyTime: "03:00"}
	next, err := daily.Next(from, nil)
	if err != nil {
		t.Fatalf("daily next: %v", err)
	}
	if next.Hour() != 3 || next.Minute() != 0 || !next.After(from) {
		t.Fatalf("daily next = %v", next)
	}

	// A cron policy matches the calendar.
	cron := Schedule{Enabled: true, Mode: ModeCron, CronExpr: "0 3 * * *"}
	next, err = cron.Next(from, nil)
	if err != nil {
		t.Fatalf("cron next: %v", err)
	}
	if next.Hour() != 3 || next.Minute() != 0 {
		t.Fatalf("cron next = %v", next)
	}

	// Validate rejects every field the console could get wrong.
	for _, tc := range []struct {
		name  string
		sched Schedule
		want  error
	}{
		{"mode", Schedule{Mode: "weekly", KeepLast: 1, MaxAgeDays: 1}, ErrInvalidMode},
		{"keep", Schedule{Mode: ModeOff, KeepLast: 0, MaxAgeDays: 1}, ErrKeepLast},
		{"age", Schedule{Mode: ModeOff, KeepLast: 1, MaxAgeDays: 0}, ErrMaxAge},
		{"zone", Schedule{Mode: ModeOff, KeepLast: 1, MaxAgeDays: 1, Timezone: "Nowhere/Land"}, ErrTimezone},
		{"interval", Schedule{Mode: ModeInterval, KeepLast: 1, MaxAgeDays: 1, Interval: time.Second}, ErrInterval},
		{"daily", Schedule{Mode: ModeDaily, KeepLast: 1, MaxAgeDays: 1, DailyTime: "noon"}, ErrInvalidTime},
		{"cron", Schedule{Mode: ModeCron, KeepLast: 1, MaxAgeDays: 1}, ErrCron},
	} {
		if err := tc.sched.Validate(); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Validate = %v, want %v", tc.name, err, tc.want)
		}
	}

	// Cron field helpers: an out-of-range value is simply not in the set, and a
	// subset match uses either day field.
	if (cronField{}).has(64) || (cronField{}).has(-1) {
		t.Fatal("a cron field must not hold values outside 0..63")
	}
	expr, err := ParseCron("0 0 1 * 1")
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	monday := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) // a Monday
	if !expr.matchDay(monday) {
		t.Fatal("a day matching either the day-of-month or the weekday must match")
	}
	for _, bad := range []string{"", "1 2 3 4", "1 2 3 4 5 6", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "a * * * *", "* * * * a", "- * * * *", "1,,2 * * * *"} {
		if _, err := ParseCron(bad); !errors.Is(err, ErrCron) {
			t.Fatalf("ParseCron(%q) = %v, want ErrCron", bad, err)
		}
	}
}
