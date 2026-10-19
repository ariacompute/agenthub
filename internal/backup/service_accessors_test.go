package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

// The service exposes a few facts the console renders — which engine is in use,
// where the archives live, whether a restore will wait for the hub — and a
// download path that has to prove the file still matches the checksum it was
// written with.

func TestServiceReportsWhatTheConsoleShows(t *testing.T) {
	eng := &fakeEngine{name: "fake", body: "payload"}
	s := newTestService(t, eng, &memStore{})

	if !s.Enabled() {
		t.Fatal("a service with an engine and a directory must be enabled")
	}
	if got := s.EngineName(); got != "fake" {
		t.Fatalf("engine = %q, want fake", got)
	}
	if got := s.Dir(); got == "" {
		t.Fatal("the directory must be exposed for the console")
	}
	// No barrier was wired, so a restore must not claim it will wait.
	if q, ok := s.Quiesce(); ok || q != nil {
		t.Fatalf("quiesce = %v, %v", q, ok)
	}

	// Without an engine there is nothing to report, and nothing to run.
	empty := New(Options{Dir: t.TempDir()})
	if empty.Enabled() {
		t.Fatal("a service without an engine must be disabled")
	}
	if got := empty.EngineName(); got != "" {
		t.Fatalf("engine = %q, want empty", got)
	}
	if _, err := empty.Checkpoint(context.Background(), "", "", "op"); err != ErrDisabled {
		t.Fatalf("checkpoint = %v, want ErrDisabled", err)
	}
	if _, err := empty.Get(context.Background(), "bkp-x"); err != ErrDisabled {
		t.Fatalf("get = %v, want ErrDisabled", err)
	}
	if _, err := empty.Path(context.Background(), "bkp-x"); err != ErrDisabled {
		t.Fatalf("path = %v, want ErrDisabled", err)
	}
	if _, err := empty.List(context.Background()); err != ErrDisabled {
		t.Fatalf("list = %v, want ErrDisabled", err)
	}
	if err := empty.Delete(context.Background(), "bkp-x"); err != ErrDisabled {
		t.Fatalf("delete = %v, want ErrDisabled", err)
	}
	if _, err := empty.SaveSchedule(context.Background(), DefaultSchedule(), "op"); err != ErrDisabled {
		t.Fatalf("save schedule = %v, want ErrDisabled", err)
	}
	if _, err := empty.Restore(context.Background(), "bkp-x", "bkp-x", "op"); err != ErrDisabled {
		t.Fatalf("restore = %v, want ErrDisabled", err)
	}
}

// A download hands over the file only after the checksum still matches: an
// operator must never be given a silently corrupted archive.
func TestPathChecksTheArchiveBeforeHandingItOver(t *testing.T) {
	eng := &fakeEngine{name: "fake", body: "payload"}
	store := &memStore{}
	s := newTestService(t, eng, store)

	cp, err := s.Checkpoint(context.Background(), KindManual, "nightly", "op")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	got, err := s.Get(context.Background(), cp.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != cp.ID || got.Status != StatusOK {
		t.Fatalf("checkpoint = %+v", got)
	}
	path, err := s.Path(context.Background(), cp.ID)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if path != cp.Path {
		t.Fatalf("path = %q, want %q", path, cp.Path)
	}

	// Tamper with the archive: the checksum is now a lie.
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Path(context.Background(), cp.ID); err != ErrChecksumMismatch {
		t.Fatalf("tampered path = %v, want ErrChecksumMismatch", err)
	}

	// A checkpoint that never finished has no file to hand over.
	if _, err := s.Get(context.Background(), "bkp-missing"); err != ErrNotFound {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}
	if _, err := s.Path(context.Background(), "bkp-missing"); err != ErrNotFound {
		t.Fatalf("path missing = %v, want ErrNotFound", err)
	}
}

// A restore asks for the checkpoint's own id as the confirmation, which is the
// only thing standing between a click and a rewritten database.
func TestRestoreInsistsOnTheRightConfirmation(t *testing.T) {
	eng := &fakeEngine{name: "fake", body: "payload"}
	s := newTestService(t, eng, &memStore{})
	cp, err := s.Checkpoint(context.Background(), KindManual, "before", "op")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Whitespace around the id is tolerated; anything else is not.
	for _, confirm := range []string{"", "yes", "bkp-other", cp.ID + "-x"} {
		if _, err := s.Restore(context.Background(), cp.ID, confirm, "op"); err == nil {
			t.Fatalf("confirm %q must be refused", confirm)
		}
	}
	if _, err := s.Restore(context.Background(), "bkp-missing", "bkp-missing", "op"); err == nil {
		t.Fatal("restoring an unknown checkpoint must fail")
	}
}

// The archive directory is the only place a checkpoint may live: a path that
// escapes it is refused rather than deleted.
func TestDeleteRefusesAPathOutsideTheDirectory(t *testing.T) {
	eng := &fakeEngine{name: "fake", body: "payload"}
	store := &memStore{}
	s := newTestService(t, eng, store)
	cp, err := s.Checkpoint(context.Background(), KindManual, "nightly", "op")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Point the row at a file outside the directory.
	outside := filepath.Join(t.TempDir(), "elsewhere.tar.gz")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for _, row := range store.rows {
		if row.ID == cp.ID {
			row.Path = outside
		}
	}
	store.mu.Unlock()

	// The refusal is the point: nothing outside the directory may be removed,
	// whatever the row claims.
	if err := s.Delete(context.Background(), cp.ID); err == nil {
		t.Fatal("a checkpoint outside the directory must not be deleted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a file outside the archive directory was removed")
	}
}

// The schedule is the operator's decision: the environment may seed it once,
// and a restart must never overwrite what the console saved.
func TestScheduleIsSeededOnceAndValidated(t *testing.T) {
	eng := &fakeEngine{name: "fake", body: "payload"}
	store := &memStore{}
	s := newTestService(t, eng, store)

	// Nothing stored yet: the default policy is the answer.
	got, err := s.Schedule(context.Background())
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got.Mode != ModeOff {
		t.Fatalf("mode = %q, want off", got.Mode)
	}
	if _, err := s.NextRun(context.Background()); err != ErrNotScheduled {
		t.Fatalf("next run = %v, want ErrNotScheduled", err)
	}

	// Seeding writes it, but only the first time.
	interval := DefaultSchedule()
	interval.Mode, interval.Enabled, interval.Interval = ModeInterval, true, time.Hour
	if err := s.SeedSchedule(context.Background(), interval, "env"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err = s.Schedule(context.Background())
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got.Mode != ModeInterval {
		t.Fatalf("mode = %q, want interval after seeding", got.Mode)
	}
	// A later restart must not overwrite the operator's policy.
	daily := DefaultSchedule()
	daily.Mode, daily.Enabled, daily.DailyTime = ModeDaily, true, "03:00"
	if err := s.SeedSchedule(context.Background(), daily, "env"); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	got, err = s.Schedule(context.Background())
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got.Mode != ModeInterval {
		t.Fatalf("mode = %q, the second seed overwrote the first", got.Mode)
	}
	// A switch off is refused before it can be stored — a policy that cannot
	// run must not look like a saved one.
	if _, err := s.SaveSchedule(context.Background(), Schedule{Mode: "whenever"}, "op"); err == nil {
		t.Fatal("an invalid policy must be refused")
	}
	// The machine readable code for a disabled service is what the API emits.
	if code := httputil.AsError(ErrDisabled).Code; code == "" {
		t.Fatal("ErrDisabled must carry a code")
	}
}
