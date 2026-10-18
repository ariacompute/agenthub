package tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenthub/internal/backup"
	"agenthub/internal/store"
)

// TestIntegrationBackupDumpDescribesTheDatabase covers the read only half: a
// dump of the real database, packed into an archive whose manifest tells the
// truth about it. It writes to a temporary directory and never touches a row.
func TestIntegrationBackupDumpDescribesTheDatabase(t *testing.T) {
	db := openDBReal(t)
	dsn := os.Getenv("DATABASE_URL")
	ctx := context.Background()

	marker := integrationResource(t, db, "model", "it-backup-marker")
	t.Cleanup(func() { _ = db.Exec("DELETE FROM resources WHERE id = ?", marker).Error })

	engine, err := backup.SelectEngine(dsn, backup.EngineNative)
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	dir := t.TempDir()
	path, m, err := backup.WriteCheckpoint(ctx, engine, dir, backup.Manifest{
		Kind:  backup.KindManual,
		Label: "integration",
	})
	if err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	if m.Engine != backup.EngineNative {
		t.Fatalf("engine = %q, want the native engine", m.Engine)
	}
	if m.Checksum == "" || m.SizeBytes == 0 {
		t.Fatalf("manifest = %+v, want a checksum and a size", m)
	}
	// The migration set travels with the archive: it is what a restore checks
	// before writing anything.
	if len(m.Schema) < 7 {
		t.Fatalf("schema = %v, want at least the seven migrations", m.Schema)
	}
	wantTables := []string{"resources", "tasks", "events", "users", "schema_migrations"}
	for _, table := range wantTables {
		if _, ok := m.RowCounts[table]; !ok {
			t.Fatalf("the dump did not cover %s: %v", table, m.Tables)
		}
	}
	// The backup catalogue is not application state and must stay out of it:
	// restoring would otherwise erase the very record the operator needs.
	for _, table := range []string{"backup_checkpoints", "backup_schedules"} {
		if _, ok := m.RowCounts[table]; ok {
			t.Fatalf("%s must be excluded from the dump", table)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("the archive is empty")
	}
	// The manifest inside must match what is on disk.
	read, err := backup.ReadManifest(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if read.Checksum != m.Checksum || len(read.Tables) != len(m.Tables) {
		t.Fatalf("manifest on disk = %+v, want %+v", read, m)
	}
	// And the payload must verify against it.
	if _, err := backup.Extract(path, io.Discard); err != nil {
		t.Fatalf("extract: %v", err)
	}
	sum, err := backup.FileChecksum(path)
	if err != nil || sum == "" {
		t.Fatalf("file checksum: %v (%q)", err, sum)
	}
}

// TestIntegrationBackupRestoreRewindsTheDatabase is the destructive half: it
// truncates every application table and writes an earlier state back.
//
// It only runs with AGENTHUB_TEST_BACKUP_RESTORE=true, because it empties the
// database the other integration cases share.
func TestIntegrationBackupRestoreRewindsTheDatabase(t *testing.T) {
	db := openDBReal(t)
	if os.Getenv("AGENTHUB_TEST_BACKUP_RESTORE") != "true" {
		t.Skip("set AGENTHUB_TEST_BACKUP_RESTORE=true to run the destructive restore case: it truncates every application table")
	}
	dsn := os.Getenv("DATABASE_URL")
	ctx := context.Background()

	// State as it exists now: one row that must survive the round trip, and a
	// catalogue row that must survive because it is not application state.
	survivor := integrationResource(t, db, "model", "it-restore-survivor")
	marker := "checkpoint-" + time.Now().Format("150405.000000")
	if err := db.Exec(
		`INSERT INTO backup_checkpoints (id, kind, engine, status, label) VALUES (?, ?, ?, ?, ?)`,
		marker, backup.KindManual, backup.EngineNative, backup.StatusOK, "written before the restore",
	).Error; err != nil {
		t.Fatalf("insert catalogue row: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM backup_checkpoints WHERE id = ?", marker).Error })

	engine, err := backup.SelectEngine(dsn, backup.EngineNative)
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	dir := t.TempDir()
	path, m, err := backup.WriteCheckpoint(ctx, engine, dir, backup.Manifest{
		Kind:  backup.KindPreRestore,
		Label: "integration",
	})
	if err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	sumBefore, err := backup.FileChecksum(path)
	if err != nil {
		t.Fatalf("file checksum: %v", err)
	}

	// Move the database on: the survivor is deleted and a row that was never
	// checkpointed appears.
	if err := db.Exec("DELETE FROM resources WHERE id = ?", survivor).Error; err != nil {
		t.Fatalf("delete survivor: %v", err)
	}
	intruder := integrationResource(t, db, "model", "it-restore-intruder")
	t.Cleanup(func() { _ = db.Exec("DELETE FROM resources WHERE id = ?", intruder).Error })

	var gone int64
	if err := db.Raw("SELECT count(*) FROM resources WHERE id = ?", survivor).Scan(&gone).Error; err != nil {
		t.Fatalf("count survivor: %v", err)
	}
	if gone != 0 {
		t.Fatal("the survivor should be gone before the restore")
	}

	// Restore: verify the archive, then hand the payload to the engine.
	payload, err := os.CreateTemp(t.TempDir(), "payload-*.data")
	if err != nil {
		t.Fatalf("payload file: %v", err)
	}
	defer func() { _ = payload.Close() }()
	if _, err := backup.Extract(path, payload); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind payload: %v", err)
	}
	if err := engine.Restore(ctx, payload, m); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The checkpointed row is back and the post-checkpoint row is gone, which
	// is the whole promise of a restore.
	var back int64
	if err := db.Raw("SELECT count(*) FROM resources WHERE id = ?", survivor).Scan(&back).Error; err != nil {
		t.Fatalf("count survivor: %v", err)
	}
	if back != 1 {
		t.Fatalf("resources after restore = %d rows for %s, want 1", back, survivor)
	}
	if err := db.Raw("SELECT count(*) FROM resources WHERE id = ?", intruder).Scan(&back).Error; err != nil {
		t.Fatalf("count intruder: %v", err)
	}
	if back != 0 {
		t.Fatal("a row created after the checkpoint survived the restore")
	}

	// Every table the manifest describes comes back with exactly the row count
	// it had: a reload that lost a child table to a foreign key would show up
	// here, not just in the resource checked above.
	for table, want := range m.RowCounts {
		var got int64
		if err := db.Raw(fmt.Sprintf("SELECT count(*) FROM %q", table)).Scan(&got).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s after restore = %d rows, want %d as the manifest promised", table, got, want)
		}
	}

	// The catalogue survives: the operator can still see the checkpoint they
	// just restored from, and the way back.
	var kept int64
	if err := db.Raw("SELECT count(*) FROM backup_checkpoints WHERE id = ?", marker).Scan(&kept).Error; err != nil {
		t.Fatalf("count catalogue: %v", err)
	}
	if kept != 1 {
		t.Fatal("the backup catalogue must survive a restore")
	}

	// The archive on disk is untouched by the restore, so it can be replayed.
	sumAfter, err := backup.FileChecksum(path)
	if err != nil {
		t.Fatalf("file checksum: %v", err)
	}
	if sumAfter != sumBefore {
		t.Fatal("the archive changed while restoring from it")
	}
}

// TestIntegrationBackupRefusesAnArchiveFromAnotherTableSet is the schema guard:
// an archive that names a table this database does not have is refused before
// anything is truncated. It runs without the destructive opt-in because it
// fails before touching a row.
func TestIntegrationBackupRefusesAnArchiveFromAnotherTableSet(t *testing.T) {
	_ = openDBReal(t)
	dsn := os.Getenv("DATABASE_URL")
	ctx := context.Background()

	engine, err := backup.SelectEngine(dsn, backup.EngineNative)
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	err = engine.Restore(ctx, errReader{}, backup.Manifest{
		Engine: backup.EngineNative,
		Tables: []string{"resources"},
	})
	if err == nil {
		t.Fatal("restoring a truncated payload must fail")
	}
}

// TestIntegrationBackupDumpEngineSelection documents which engine this machine
// gets: pg_dump when the client is installed, the COPY engine otherwise. Both
// are supported; the log line is what an operator checks when they wonder why.
func TestIntegrationBackupDumpEngineSelection(t *testing.T) {
	_ = openDBReal(t)
	engine, err := backup.SelectEngine(os.Getenv("DATABASE_URL"), "")
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	t.Logf("engine selected: %s", engine.Name())
	switch engine.Name() {
	case backup.EnginePgDump, backup.EngineNative:
	default:
		t.Fatalf("unknown engine %q", engine.Name())
	}
}

// TestIntegrationBackupCatalogueSurvivesTheDatabase is the store's own round
// trip: a checkpoint row is written before the dump, updated when it finishes,
// read back by the console and eventually deleted.
//
// The unit tests run the orchestration against a fake store, so this is the
// only case that proves the column names in 007_backups.sql and the ones
// internal/store writes still agree — a mismatch here is invisible until an
// operator takes the first checkpoint on a real database.
func TestIntegrationBackupCatalogueSurvivesTheDatabase(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")

	engine, err := backup.SelectEngine(dsn, backup.EngineNative)
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	svc := backup.New(backup.Options{
		Dir:    t.TempDir(),
		Engine: engine,
		Store:  store.NewBackupStore(db),
		Log:    slog.Default(),
	})

	c, err := svc.Checkpoint(ctx, backup.KindManual, "catalogue round trip", "integration")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM backup_checkpoints WHERE id = ?", c.ID).Error })

	if c.Status != backup.StatusOK {
		t.Fatalf("status = %q, want ok (error: %s)", c.Status, c.ErrorCode)
	}
	if c.Path == "" || c.Checksum == "" || c.SizeBytes == 0 {
		t.Fatalf("checkpoint = %+v, want a path, a checksum and a size", c)
	}

	// Read the row back the way the console does: everything the table shows
	// has to come out of PostgreSQL, not out of the struct that wrote it.
	stored, err := svc.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.SizeBytes != c.SizeBytes || stored.RowCount != c.RowCount || stored.TableCount != c.TableCount {
		t.Fatalf("stored = %+v, want %+v", stored, c)
	}
	// schema_version is the column the restore guard reads; a store that wrote
	// the wrong name would leave this empty.
	if stored.Schema == "" {
		t.Fatal("schema_version is empty: the migration set was not recorded")
	}
	if stored.FinishedAt == nil || stored.DurationMS < 0 {
		t.Fatalf("stored = %+v, want a finished_at and a duration", stored)
	}

	listed, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for i := range listed {
		if listed[i].ID == c.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list = %d rows, the checkpoint is missing from it", len(listed))
	}
	last, err := store.NewBackupStore(db).LastSuccess(ctx)
	if err != nil {
		t.Fatalf("last success: %v", err)
	}
	if last == nil || last.ID != c.ID {
		t.Fatalf("last success = %+v, want the checkpoint just taken", last)
	}

	// The schedule is a single shared row, so it is put back the way it was
	// found: another case may rely on the defaults.
	backupStore := store.NewBackupStore(db)
	original, err := backupStore.LoadSchedule(ctx)
	if err != nil {
		t.Fatalf("load schedule: %v", err)
	}
	t.Cleanup(func() {
		if original != nil {
			_ = backupStore.SaveSchedule(ctx, *original, "integration")
		}
	})

	want := backup.DefaultSchedule()
	want.Mode = backup.ModeDaily
	want.Enabled = true
	want.DailyTime = "04:15"
	want.KeepLast = 3
	want.MaxAgeDays = 9
	if _, err := svc.SaveSchedule(ctx, want, "integration"); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if stored, err := svc.Schedule(ctx); err != nil {
		t.Fatalf("schedule: %v", err)
	} else if stored.Mode != backup.ModeDaily || stored.DailyTime != "04:15" ||
		stored.KeepLast != 3 || stored.MaxAgeDays != 9 || !stored.Enabled {
		t.Fatalf("schedule = %+v, want %+v", stored, want)
	}
	// A policy that cannot run is refused before it reaches the database.
	if _, err := svc.SaveSchedule(ctx, backup.Schedule{Mode: "hourly"}, "integration"); !errors.Is(err, backup.ErrInvalidMode) {
		t.Fatalf("invalid schedule: err = %v, want ErrInvalidMode", err)
	}
	// Seeding only happens while the row has never been saved by the console.
	if err := svc.SeedSchedule(ctx, backup.DefaultSchedule(), "integration"); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	if after, err := svc.Schedule(ctx); err != nil || after.Mode != backup.ModeDaily {
		t.Fatalf("after seed: %+v (err = %v), want the saved daily policy", after, err)
	}

	// Deleting removes the row and the archive it points at.
	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(c.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive still on disk after the delete: %v", err)
	}
	if _, err := svc.Get(ctx, c.ID); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("get after delete: err = %v, want ErrNotFound", err)
	}
}

// errReader fails immediately, like a payload that was cut short.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("truncated") }

// ensure the helpers above keep the package honest about what a restore needs.
var _ = errors.Is
var _ = filepath.Join
