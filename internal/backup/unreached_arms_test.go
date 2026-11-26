package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file closes the last failure arms of the archive, service, crypto and
// engine layers that the happy-path and edge suites never enter. Every case
// drives an arm the code already has; no seam is invented for a test.

// failReader is a payload source that breaks mid-copy, which is what a dump
// engine hitting a dead connection looks like to Pack.
type failReader struct{ err error }

func (r failReader) Read([]byte) (int, error) { return 0, r.err }

// failWriter is a destination that refuses the payload, standing in for a full
// restore volume.
type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

// archiveEntry is one member of a hand-built archive: the format only defines
// two names, and the cases below need to violate that on purpose.
type archiveEntry struct {
	name string
	body []byte
}

// writeRawArchive writes a gzip'd tar with the entries in the order given. Pack
// cannot produce a malformed archive, so the refusals are exercised against
// bytes assembled here.
func writeRawArchive(t *testing.T, path string, entries ...archiveEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the archive: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body))}); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatalf("write %q: %v", e.name, err)
		}
	}
	for _, closer := range []io.Closer{tw, gw, f} {
		if err := closer.Close(); err != nil {
			t.Fatalf("close the archive: %v", err)
		}
	}
}

func TestPackReportsFailureArms(t *testing.T) {
	dir := t.TempDir()

	// A parent that is a regular file cannot hold an archive.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the blocker: %v", err)
	}
	if _, err := Pack(filepath.Join(blocker, "a.tar.gz"), strings.NewReader("x"), 1, Manifest{}); err == nil {
		t.Fatal("packing under a regular file must fail")
	}

	// A destination that is a directory cannot be opened for writing.
	if _, err := Pack(dir, strings.NewReader("x"), 1, Manifest{}); err == nil {
		t.Fatal("packing onto a directory must fail")
	}

	// A payload that breaks mid-copy fails the pack and leaves no half written
	// archive behind: a truncated file would look like a restore candidate.
	dst := filepath.Join(dir, "partial.tar.gz")
	if _, err := Pack(dst, failReader{err: errInjected}, 1, Manifest{}); !errors.Is(err, errInjected) {
		t.Fatalf("pack with a failing payload = %v, want the injected failure", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed pack left %s behind: %v", dst, err)
	}
}

func TestArchiveHelpersReportUnusablePaths(t *testing.T) {
	// A payload that is shorter than the size the tar header promises would make
	// the manifest describe bytes that are not in the archive.
	dst := filepath.Join(t.TempDir(), "short.tar.gz")
	if _, err := Pack(dst, strings.NewReader("rows"), 4096, Manifest{Kind: KindManual, CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("packing a payload shorter than its declared size must fail")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a short payload left %s behind: %v", dst, err)
	}

	// Hashing a directory is not a checksum, and saying so beats returning the
	// hash of an empty stream.
	if _, err := FileChecksum(t.TempDir()); err == nil {
		t.Fatal("the checksum of a directory must fail")
	}

	// A read-only directory cannot hold the staged payload.
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatalf("stage the read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if stage, err := newStageDump(locked, "payload"); err == nil {
		_ = stage.Close()
		t.Fatal("staging in a read-only directory must fail")
	}
}

func TestWriteCheckpointFailureArms(t *testing.T) {
	ctx := context.Background()
	engine := &fakeEngine{name: "fake", body: "rows\n"}

	// The staging directory cannot be created under a regular file.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the blocker: %v", err)
	}
	if _, _, err := WriteCheckpoint(ctx, engine, blocker, Manifest{}); err == nil {
		t.Fatal("staging under a regular file must fail")
	}

	// A zero CreatedAt is filled in rather than producing a 0001 stamp.
	m := Manifest{}
	path, filled, err := WriteCheckpoint(ctx, engine, dir, m)
	if err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if filled.CreatedAt.IsZero() {
		t.Fatal("the manifest must carry a creation time")
	}
	if !strings.Contains(filepath.Base(path), filled.CreatedAt.UTC().Format("20060102T150405Z")) {
		t.Fatalf("archive name %q does not carry the creation time", filepath.Base(path))
	}

	// The archive name is already taken by a directory, so packing fails after a
	// successful dump.
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	name := checkpointFileName(at, KindManual, "taken")
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("stage the name collision: %v", err)
	}
	if _, _, err := WriteCheckpoint(ctx, engine, dir, Manifest{Kind: KindManual, Label: "taken", CreatedAt: at}); err == nil {
		t.Fatal("packing onto a directory that holds the archive name must fail")
	}
}

func TestReadManifestRefusesAMalformedManifest(t *testing.T) {
	dir := t.TempDir()

	// Bytes that are not JSON at all.
	broken := filepath.Join(dir, "broken.tar.gz")
	writeRawArchive(t, broken, archiveEntry{name: manifestEntry, body: []byte("{not json")})
	if _, err := ReadManifest(broken); !errors.Is(err, ErrArchive) {
		t.Fatalf("read manifest of invalid JSON = %v, want ErrArchive", err)
	}

	// A manifest from a format this build does not implement.
	ahead := filepath.Join(dir, "ahead.tar.gz")
	writeRawArchive(t, ahead, archiveEntry{name: manifestEntry, body: []byte(`{"version":2}`)})
	if _, err := ReadManifest(ahead); !errors.Is(err, ErrArchive) {
		t.Fatalf("read manifest of an unknown version = %v, want ErrArchive", err)
	}

	// An archive with a payload but no manifest cannot be judged at all.
	payloadOnly := filepath.Join(dir, "payload-only.tar.gz")
	writeRawArchive(t, payloadOnly, archiveEntry{name: dataEntry, body: []byte("rows\n")})
	if _, err := ReadManifest(payloadOnly); !errors.Is(err, ErrArchive) {
		t.Fatalf("read manifest of a payload-only archive = %v, want ErrArchive", err)
	}

	// A gzip stream whose contents are not a tar archive.
	notTar := filepath.Join(dir, "not-tar.tar.gz")
	f, err := os.Create(notTar)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte("this is not a tar stream at all, only gzip")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := ReadManifest(notTar); !errors.Is(err, ErrArchive) {
		t.Fatalf("read manifest of a non-tar gzip = %v, want ErrArchive", err)
	}
}

func TestExtractReportsAFailingDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.tar.gz")
	if _, err := Pack(path, strings.NewReader("rows\n"), 5, Manifest{Kind: KindManual, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("pack: %v", err)
	}
	// A full restore volume must surface as an error, not a short write that the
	// checksum would then blame on the archive.
	if _, err := Extract(path, failWriter{err: errInjected}); !errors.Is(err, errInjected) {
		t.Fatalf("extract into a failing destination = %v, want the injected failure", err)
	}
}

func TestCryptoReportsTheRemainingFailureArms(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}

	// Decrypting a file that is not there is reported before anything is
	// written.
	if err := DecryptFile(filepath.Join(dir, "missing.enc"), filepath.Join(dir, "out"), key); err == nil {
		t.Fatal("decrypting a missing file must fail")
	}

	// A destination that accepts the descriptor but refuses the bytes (a full
	// device) must fail the encrypt instead of reporting success. /dev/full is a
	// Linux fixture; elsewhere the arm stays untested rather than faked.
	if _, err := os.Stat("/dev/full"); err == nil {
		src := filepath.Join(dir, "plain")
		if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
			t.Fatalf("stage: %v", err)
		}
		if err := EncryptFile(src, "/dev/full", key); err == nil {
			t.Fatal("writing the nonce to a full device must fail")
		}
	}
}

func TestSelectEngineInstallsTheClientWhenItIsPresent(t *testing.T) {
	original := lookPath
	t.Cleanup(func() { lookPath = original })
	lookPath = func(string) (string, error) { return "/bin/true", nil }

	engine, err := SelectEngine("postgres://user@host/db", EnginePgDump)
	if err != nil {
		t.Fatalf("select pg_dump with the client present: %v", err)
	}
	if engine.Name() != EnginePgDump {
		t.Fatalf("engine = %q, want %q", engine.Name(), EnginePgDump)
	}

	// pg_dump without pg_restore is a half installed client: the engine must say
	// so rather than fail at restore time.
	lookPath = func(name string) (string, error) {
		if name == "pg_restore" {
			return "", errInjected
		}
		return "/bin/true", nil
	}
	if _, err := SelectEngine("postgres://user@host/db", EnginePgDump); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("select pg_dump without pg_restore = %v, want ErrEngineUnavailable", err)
	}
}

func TestCodeOfMapsEveryFailureClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"engine unavailable", ErrEngineUnavailable, "backup_engine_unavailable"},
		{"table guarded", wrapErr(ErrTableGuarded), "backup_table_guarded"},
		{"engine failed", ErrEngineFailed, "backup_engine_failed"},
		{"checksum", wrapErr(ErrChecksumMismatch), "backup_checksum_mismatch"},
		{"disabled", ErrDisabled, "backups_disabled"},
		{"other", errInjected, "backup_failed"},
	} {
		if got := codeOf(tc.err); got != tc.want {
			t.Errorf("%s: codeOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// wrapErr proves the mapping follows the chain rather than comparing values.
func wrapErr(err error) error { return errors.Join(errInjected, err) }

func TestCheckpointEncryptFailureArms(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	t.Run("ciphertext cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		// EncryptFile writes next to the plaintext archive; a directory holding
		// that exact name makes the open fail.
		name := checkpointFileName(at, KindManual, "nightly")
		if err := os.MkdirAll(filepath.Join(dir, name+".enc"), 0o755); err != nil {
			t.Fatalf("stage the name collision: %v", err)
		}
		store := &memStore{}
		s := New(Options{Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: store, Key: key,
			Now: func() time.Time { return at }})

		c, err := s.Checkpoint(ctx, KindManual, "nightly", "tester")
		if err == nil {
			t.Fatal("a checkpoint that cannot be encrypted must fail")
		}
		if c == nil || c.Status != StatusFailed || c.ErrorCode != "backup_encrypt_failed" {
			t.Fatalf("row = %+v, want a failed row with the encrypt code", c)
		}
		// The plaintext archive must not survive a failed encryption: it would be
		// a checkpoint nobody agreed to keep in the clear. (The directory that
		// forced the failure is the test's own fixture, not a leftover.)
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the plaintext archive was left behind after a failed encryption")
		}
	})

	t.Run("the companion hash cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		name := checkpointFileName(at, KindManual, "nightly")
		if err := os.MkdirAll(filepath.Join(dir, name+".enc.sha256"), 0o755); err != nil {
			t.Fatalf("stage the hash collision: %v", err)
		}
		store := &memStore{}
		s := New(Options{Dir: dir, Engine: &fakeEngine{name: "fake", body: "rows\n"}, Store: store, Key: key,
			Now: func() time.Time { return at }})

		// The hash manifest is a convenience for operators: failing to write it
		// is a warning, not a failed checkpoint.
		c, err := s.Checkpoint(ctx, KindManual, "nightly", "tester")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if c.Status != StatusOK || !strings.HasSuffix(c.Path, ".enc") {
			t.Fatalf("row = %+v, want a finished encrypted checkpoint", c)
		}
	})
}

func TestRestoreOfAnEncryptedCheckpoint(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	s := New(Options{
		Dir:    t.TempDir(),
		Engine: &fakeEngine{name: "fake", body: "rows\n"},
		Store:  &memStore{},
		Key:    key,
	})
	target, err := s.Checkpoint(ctx, KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Restoring an encrypted archive decrypts it twice: once to read the
	// manifest, once to hand the payload to the engine. Both temporary files
	// must be cleaned up.
	restored, err := s.Restore(ctx, target.ID, target.ID, "tester")
	if err != nil {
		t.Fatalf("restore of an encrypted checkpoint: %v", err)
	}
	if restored.RestoredAt == nil || restored.RestoredBy != "tester" {
		t.Fatalf("row = %+v, want the restore recorded", restored)
	}

	// Without a scratch directory for the decrypted copy the restore is refused
	// before the archive is trusted.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); err == nil {
		t.Fatal("restoring an encrypted archive without a scratch directory must fail")
	}
}

func TestServiceReadPathsSurfaceCatalogueFailures(t *testing.T) {
	ctx := context.Background()

	// Delete asks the catalogue first; an unreadable row is the answer.
	store := &errBackupStore{memStore: &memStore{}, getErr: errInjected}
	s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake"}, Store: store})
	if err := s.Delete(ctx, "bkp-1"); !errors.Is(err, errInjected) {
		t.Fatalf("delete with an unreadable row = %v, want the injected failure", err)
	}

	// A policy that cannot be stored is reported: the console would otherwise
	// show a schedule the instance never saved.
	valid := DefaultSchedule()
	valid.Mode = ModeInterval
	valid.Enabled = true
	valid.Interval = time.Hour
	store.saveErr = errInjected
	if _, err := s.SaveSchedule(ctx, valid, "tester"); !errors.Is(err, errInjected) {
		t.Fatalf("save schedule = %v, want the injected failure", err)
	}
}

func TestSeedScheduleFailureArms(t *testing.T) {
	ctx := context.Background()
	valid := DefaultSchedule()
	valid.Mode = ModeInterval
	valid.Enabled = true
	valid.Interval = time.Hour

	// A stored policy that cannot be read is reported rather than overwritten
	// with the environment default.
	store := &errBackupStore{memStore: &memStore{}, loadErr: errInjected}
	s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake"}, Store: store})
	if err := s.SeedSchedule(ctx, valid, "tester"); !errors.Is(err, errInjected) {
		t.Fatalf("seed with an unreadable policy = %v, want the injected failure", err)
	}

	// An invalid environment policy is refused before it reaches the catalogue.
	store = &errBackupStore{memStore: &memStore{}}
	s = New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake"}, Store: store})
	if err := s.SeedSchedule(ctx, Schedule{Mode: ModeInterval, KeepLast: 1, MaxAgeDays: 1}, "tester"); !errors.Is(err, ErrInterval) {
		t.Fatalf("seed with an invalid policy = %v, want ErrInterval", err)
	}
}

func TestRunLogsAScheduledCheckpointFailure(t *testing.T) {
	// Run loops until the context ends: a context that is already done makes it
	// take one pass, which is enough for the failure arm.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := &memStore{sched: &Schedule{Mode: ModeInterval, Enabled: true, Interval: time.Hour,
		KeepLast: 1, MaxAgeDays: 1}}
	s := New(Options{Dir: t.TempDir(), Engine: &fakeEngine{name: "fake", fail: ErrEngineFailed}, Store: store})
	// A failing dump is logged and the scheduler keeps its cadence instead of
	// exiting: an operator fixes the engine, not the daemon.
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.rows) != 1 || store.rows[0].Status != StatusFailed {
		t.Fatalf("rows = %+v, want one failed scheduled checkpoint", store.rows)
	}
}

func TestRestoreReportsAnUnusableScratchDirectory(t *testing.T) {
	ctx := context.Background()
	engine := &fakeEngine{name: "fake", body: "rows\n"}
	s := New(Options{Dir: t.TempDir(), Engine: engine, Store: &memStore{},
		Applied: func(context.Context) ([]string, error) { return []string{"001_resources.sql"}, nil }})

	target, err := s.Checkpoint(ctx, KindManual, "", "tester")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// The payload is materialised in a scratch directory; without one the
	// restore must fail before the engine is handed anything.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	if _, err := s.Restore(ctx, target.ID, target.ID, "tester"); err == nil {
		t.Fatal("restore without a usable scratch directory must fail")
	}
	for _, event := range engine.events {
		if event == "restore" {
			t.Fatal("the engine must not see a payload the scratch file could not hold")
		}
	}
}

// TestExtractKeepsTheManifestOnFailure documents what a caller can rely on: a
// rejected archive still describes itself, so a console can show which
// checkpoint was refused.
func TestExtractKeepsTheManifestOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stale.tar.gz")
	writeStaleArchive(t, path)
	m, err := Extract(path, io.Discard)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("extract = %v, want ErrChecksumMismatch", err)
	}
	if m.Checksum == "" || m.Version != archiveVersion {
		t.Fatalf("manifest = %+v, want the manifest of the refused archive", m)
	}

	// A payload-less archive is refused without writing anything to dst.
	var buf bytes.Buffer
	empty := filepath.Join(dir, "empty.tar.gz")
	// A manifest this build would accept, for an archive that carries no
	// payload: the refusal has to come from the missing data entry.
	body := []byte(`{"version":` + "1" + `,"checksum":"` + strings.Repeat("0", 64) + `"}`)
	writeRawArchive(t, empty, archiveEntry{name: manifestEntry, body: body})
	if _, err := Extract(empty, &buf); !errors.Is(err, ErrArchive) {
		t.Fatalf("extract of a payload-less archive = %v, want ErrArchive", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a refused archive wrote %d byte(s)", buf.Len())
	}
}
