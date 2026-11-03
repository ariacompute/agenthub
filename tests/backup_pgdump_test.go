package tests

// The pg_dump engine, against a real database.
//
// internal/backup proves the parts of this engine that must be right on every
// host (how the connection reaches the child, how its output is scrubbed), but
// Dump and Restore themselves read current_schema, discover the tables and fix
// the sequences afterwards — there is no way to exercise them without PostgreSQL
// and the client tools, and a test that faked either would be testing the fake.
//
// So the engine is gated twice: DATABASE_URL (the package's usual gate) and
// pg_dump/pg_restore being on PATH. On a workstation without them the cases skip
// and say why; in CI (postgres:16 service, client in the image) and in a WSL2
// checkout they run. The restore that rewrites the schema is gated a third time,
// behind AGENTHUB_TEST_BACKUP_RESTORE, for the same reason the COPY engine's
// restore case is: it empties the database the other integration cases share.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"agenthub/internal/backup"
	"agenthub/internal/testdb"
)

// pgDumpEngineFor selects the client engine, or skips with the reason.
func pgDumpEngineFor(t *testing.T) (backup.Engine, string) {
	t.Helper()
	_ = openDBReal(t)
	for _, bin := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed, so the pg_dump engine cannot run here (the COPY engine covers this host)", bin)
		}
	}
	dsn := os.Getenv("DATABASE_URL")
	engine, err := backup.SelectEngine(dsn, backup.EnginePgDump)
	if err != nil {
		t.Fatalf("select the pg_dump engine: %v", err)
	}
	if engine.Name() != backup.EnginePgDump {
		t.Fatalf("engine = %q, want %q — the selection fell back despite the client being present",
			engine.Name(), backup.EnginePgDump)
	}
	return engine, dsn
}

// TestIntegrationPgDumpEngineWritesAnArchive covers the read-only half: the
// archive pg_dump produces, and the manifest filled in from the database
// afterwards.
func TestIntegrationPgDumpEngineWritesAnArchive(t *testing.T) {
	engine, dsn := pgDumpEngineFor(t)
	ctx := context.Background()
	// pg_dump copies every table with a bare COPY ... TO stdout, which PostgreSQL
	// refuses on a row level security guarded table unless the role is a superuser
	// or holds BYPASSRLS. Taking a checkpoint is the migrator's job, so under the
	// runtime role this case skips and names the privilege; the cases that only
	// restore keep running there.
	testdb.RequireBypassRLS(t, ctx, dsn)

	var buf bytes.Buffer
	res, err := engine.Dump(ctx, &buf)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("the archive is empty")
	}
	// A custom-format archive starts with the "PGDMP" magic. Asserting it here
	// keeps the case honest about *which* engine answered: a COPY engine writing
	// text into the same buffer would satisfy a size check.
	if !bytes.HasPrefix(buf.Bytes(), []byte("PGDMP")) {
		t.Fatalf("archive does not start with the pg_dump magic: %q", buf.Bytes()[:min(8, buf.Len())])
	}

	// pg_dump reports nothing about what it wrote, so everything below comes from
	// the database itself — which is the point of this half of the case.
	for _, table := range []string{"resources", "tasks", "schema_migrations"} {
		found := false
		for _, got := range res.Tables {
			if got == table {
				found = true
			}
		}
		if !found {
			t.Fatalf("the manifest does not cover %s: %v", table, res.Tables)
		}
	}
	if _, ok := res.RowCounts["resources"]; !ok {
		t.Fatalf("the manifest has no row count for resources: %v", res.RowCounts)
	}
	if len(res.Schema) == 0 {
		t.Fatal("the manifest records no migration set")
	}
	for _, table := range []string{"backup_checkpoints", "backup_schedules"} {
		if _, ok := res.RowCounts[table]; ok {
			t.Fatalf("%s is the backup catalogue: it must stay out of its own dump", table)
		}
	}

	// A writer that fails halfway must report the failure rather than a partially
	// delivered archive that the next stage would checksum and keep.
	if _, err := engine.Dump(ctx, &shortWriter{}); err == nil {
		t.Fatal("a failing destination must surface as an error")
	}
}

// TestIntegrationPgDumpEngineRefusesAGarbageArchive is the failure half of
// Restore. pg_restore runs with --single-transaction, so a rejected payload
// leaves nothing behind: this case is safe to run without the destructive
// opt-in.
func TestIntegrationPgDumpEngineRefusesAGarbageArchive(t *testing.T) {
	engine, _ := pgDumpEngineFor(t)
	err := engine.Restore(context.Background(), strings.NewReader("this is not a custom format archive"),
		backup.Manifest{Engine: backup.EnginePgDump, Tables: []string{"resources"}})
	if err == nil {
		t.Fatal("restoring a garbage payload must fail")
	}
	if !errors.Is(err, backup.ErrEngineFailed) {
		t.Fatalf("error = %v, want ErrEngineFailed so the caller can report a machine code", err)
	}
}

// TestIntegrationPgDumpEngineRoundTrips is the destructive half, behind the same
// opt-in as the COPY engine's restore: it drops and recreates the schema.
func TestIntegrationPgDumpEngineRoundTrips(t *testing.T) {
	if os.Getenv("AGENTHUB_TEST_BACKUP_RESTORE") != "true" {
		t.Skip("set AGENTHUB_TEST_BACKUP_RESTORE=true to run the restore case: it rewrites every table in the schema")
	}
	engine, _ := pgDumpEngineFor(t)
	ctx := context.Background()
	db := openDBReal(t)

	// Same reason as the COPY engine case: a restore resurrects rows its
	// checkpoint captured, and the next run would then collide on the dedup
	// index.
	if err := db.Exec("DELETE FROM resources WHERE name LIKE 'it-%'").Error; err != nil {
		t.Fatalf("clear leftover test rows: %v", err)
	}

	survivor := integrationResource(t, db, "model", "it-pgdump-survivor")
	marker := "pgdump-" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM resources WHERE id = ?", survivor).Error
		_ = db.Exec("DELETE FROM backup_checkpoints WHERE id = ?", marker).Error
	})
	if err := db.Exec(
		`INSERT INTO backup_checkpoints (id, kind, engine, status, label) VALUES (?, ?, ?, ?, ?)`,
		marker, backup.KindManual, backup.EnginePgDump, backup.StatusOK, "written before the restore",
	).Error; err != nil {
		t.Fatalf("insert catalogue row: %v", err)
	}

	dir := t.TempDir()
	path, m, err := backup.WriteCheckpoint(ctx, engine, dir, backup.Manifest{
		Kind:  backup.KindPreRestore,
		Label: "integration-pgdump",
	})
	if err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if m.Engine != backup.EnginePgDump {
		t.Fatalf("manifest engine = %q, want %q", m.Engine, backup.EnginePgDump)
	}

	// Move the database on, then hand the payload back to pg_restore.
	if err := db.Exec("DELETE FROM resources WHERE id = ?", survivor).Error; err != nil {
		t.Fatalf("delete survivor: %v", err)
	}
	// A ledger row written after the archive was taken: the checkpoint does not
	// carry the ledger, so this row must still be there afterwards.
	auditID := "it-pgdump-audit-" + time.Now().Format("150405.000000")
	if err := db.Exec(
		`INSERT INTO audit_log (event_id, source, action) VALUES (?, 'api', 'integration.pgdump')`, auditID,
	).Error; err != nil {
		t.Fatalf("insert audit row: %v", err)
	}
	payload, err := os.CreateTemp(dir, "payload-*.data")
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

	var back int64
	if err := db.Raw("SELECT count(*) FROM resources WHERE id = ?", survivor).Scan(&back).Error; err != nil {
		t.Fatalf("count survivor: %v", err)
	}
	if back != 1 {
		t.Fatal("the checkpointed resource did not come back")
	}
	// The catalogue is not in the dump, so the row written before it survives —
	// the operator can still see the checkpoint they restored from.
	var kept int64
	if err := db.Raw("SELECT count(*) FROM backup_checkpoints WHERE id = ?", marker).Scan(&kept).Error; err != nil {
		t.Fatalf("count catalogue: %v", err)
	}
	if kept != 1 {
		t.Fatal("the backup catalogue must survive a restore")
	}
	// The archive is built from a positive list of tables, so nothing the
	// excluded ledger owns is in it — neither the rows nor the trigger that
	// keeps them append-only.
	var auditKept, auditGuard int64
	if err := db.Raw("SELECT count(*) FROM audit_log WHERE event_id = ?", auditID).Scan(&auditKept).Error; err != nil {
		t.Fatalf("count audit row: %v", err)
	}
	if auditKept != 1 {
		t.Fatal("the restore must leave the audit ledger alone")
	}
	if err := db.Raw(`
		SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
		WHERE c.relname = 'audit_log' AND t.tgname = 'audit_log_no_truncate'`).Scan(&auditGuard).Error; err != nil {
		t.Fatalf("count the ledger trigger: %v", err)
	}
	if auditGuard != 1 {
		t.Fatal("the restore dropped the ledger's append-only trigger")
	}
	// A table's policies and triggers travel with it in the archive. Losing
	// them would be a security regression no row count could show: the rows
	// would come back and the rules that hide them would not.
	var policies int64
	if err := db.Raw(`
		SELECT count(*) FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid
		WHERE c.relname = 'tasks'`).Scan(&policies).Error; err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if policies == 0 {
		t.Fatal("the restore dropped the row level security policies")
	}
	var ownerTriggers int64
	if err := db.Raw(`
		SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
		WHERE c.relname = 'events' AND NOT t.tgisinternal`).Scan(&ownerTriggers).Error; err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if ownerTriggers == 0 {
		t.Fatal("the restore dropped the owner triggers")
	}
	// fixSequences runs after pg_restore, and a sequence left at zero is what
	// breaks the next insert. One insert proves the sequence moved.
	if err := db.Exec(
		`INSERT INTO backup_checkpoints (id, kind, engine, status, label) VALUES (?, ?, ?, ?, ?)`,
		marker+"-after", backup.KindManual, backup.EnginePgDump, backup.StatusOK, "after the restore",
	).Error; err != nil {
		t.Fatalf("insert after restore: %v", err)
	}
	_ = db.Exec("DELETE FROM backup_checkpoints WHERE id = ?", marker+"-after").Error
}

// shortWriter accepts a little and then fails, standing in for a disk that filled
// up while the archive was being copied out.
type shortWriter struct{ written int }

func (w *shortWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	if w.written > 16 {
		return 0, fmt.Errorf("no space left on device")
	}
	return len(p), nil
}
