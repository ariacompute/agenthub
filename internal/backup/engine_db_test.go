package backup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"agenthub/internal/testdb"
)

// The engines read and write a real database, so their own tests live behind
// DATABASE_URL, the same gate every DB-backed case in the repo uses. The
// destructive half is gated once more behind AGENTHUB_TEST_BACKUP_RESTORE
// because it empties every table — the same switch the tests/ package uses, and
// never set in CI.

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping DB-backed engine test (make db-up)")
	}
	return dsn
}

// frameNames walks a native payload and returns the tables it carries, which is
// the only way to prove an excluded table is absent rather than merely uncounted.
func frameNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	br := bufio.NewReader(r)
	magic, err := br.ReadString('\n')
	if err != nil || magic != payloadMagic {
		t.Fatalf("payload magic = %q, %v", magic, err)
	}
	var names []string
	for {
		table, _, size, err := readFrame(br)
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		names = append(names, table)
		if _, err := io.Copy(io.Discard, io.LimitReader(br, size)); err != nil {
			t.Fatalf("drain %s: %v", table, err)
		}
	}
}

func TestCopyEngineDumpCarriesTheApplicationTablesOnly(t *testing.T) {
	dsn := testDSN(t)
	eng := newCopyEngine(dsn)

	var payload bytes.Buffer
	res, err := eng.Dump(context.Background(), &payload)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	for _, table := range []string{"resources", "tasks", "schema_migrations"} {
		if _, ok := res.RowCounts[table]; !ok {
			t.Fatalf("the dump skipped %s: %v", table, res.Tables)
		}
	}
	// The catalogue and the ledger are not application state: they must be absent
	// from the manifest *and* from the payload, or a restore would rewrite them.
	for table := range excludedTables {
		if _, ok := res.RowCounts[table]; ok {
			t.Fatalf("%s is in the manifest, so a restore would rewrite it", table)
		}
	}
	for _, name := range frameNames(t, bytes.NewReader(payload.Bytes())) {
		if excludedTables[name] {
			t.Fatalf("the payload carries a frame for excluded table %s", name)
		}
	}
	if len(res.Schema) == 0 {
		t.Fatal("the dump did not record the migration set")
	}
	// Rows is what the console prints next to a checkpoint.
	if res.Rows() != sumCounts(res.RowCounts) {
		t.Fatalf("Rows() = %d, want the sum of the per-table counts", res.Rows())
	}
}

func sumCounts(counts map[string]int64) int64 {
	var total int64
	for _, n := range counts {
		total += n
	}
	return total
}

// A table that refuses to be truncated cannot be loaded back, so the dump has to
// refuse too — on the day the archive is needed, a pg_dump capable image is no
// longer a choice.
func TestCopyEngineRefusesToDumpAGuardedTable(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	// This case builds a probe table, so it needs the migrator role. Under the
	// production runtime role it skips with the reason instead of failing with
	// "permission denied for schema public".
	testdb.RequireDDL(t, ctx, dsn)
	conn, err := connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS it_guarded_engine (id INT PRIMARY KEY)`,
		`CREATE OR REPLACE FUNCTION it_guarded_engine_no_truncate() RETURNS trigger AS $$
		 BEGIN RAISE EXCEPTION 'it_guarded_engine is guarded'; END $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS it_guarded_engine_truncate ON it_guarded_engine`,
		`CREATE TRIGGER it_guarded_engine_truncate BEFORE TRUNCATE ON it_guarded_engine
		 FOR EACH STATEMENT EXECUTE FUNCTION it_guarded_engine_no_truncate()`,
	}
	for _, stmt := range statements {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("prepare probe table: %v", err)
		}
	}
	// The probe table must not survive into another case — a guarded table left
	// behind would make every later dump in this package refuse. The cleanup
	// opens its own connection because the deferred Close runs before t.Cleanup.
	t.Cleanup(func() {
		bg := context.Background()
		c, err := connect(bg, dsn)
		if err != nil {
			t.Errorf("reconnect for cleanup: %v", err)
			return
		}
		defer func() { _ = c.Close(bg) }()
		if _, err := c.Exec(bg, `DROP TABLE IF EXISTS it_guarded_engine CASCADE`); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
		if _, err := c.Exec(bg, `DROP FUNCTION IF EXISTS it_guarded_engine_no_truncate()`); err != nil {
			t.Errorf("drop probe function: %v", err)
		}
	})

	_, err = newCopyEngine(dsn).Dump(ctx, io.Discard)
	if !errors.Is(err, ErrTableGuarded) {
		t.Fatalf("Dump = %v, want ErrTableGuarded", err)
	}
	if !strings.Contains(err.Error(), "it_guarded_engine") {
		t.Fatalf("error = %v, want it to name the guarded table", err)
	}
}

// The pg_dump engine builds its archive from a positive --table list, so an
// excluded table takes its sequence and its trigger functions with it. Without
// that, pg_restore --clean drops objects the excluded table still uses.
func TestPgDumpEngineBuildsAPositiveTableList(t *testing.T) {
	dsn := testDSN(t)
	for _, bin := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed, so this engine cannot run here", bin)
		}
	}
	eng, err := newPgDumpEngine(dsn)
	if err != nil {
		t.Fatalf("newPgDumpEngine: %v", err)
	}

	var payload bytes.Buffer
	res, err := eng.Dump(context.Background(), &payload)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if !bytes.HasPrefix(payload.Bytes(), []byte("PGDMP")) {
		t.Fatalf("payload does not start with the custom-format signature: %q", payload.Bytes()[:8])
	}
	for table := range excludedTables {
		if _, ok := res.RowCounts[table]; ok {
			t.Fatalf("%s is in the manifest, so a restore would rewrite it", table)
		}
	}
	if _, ok := res.RowCounts["resources"]; !ok {
		t.Fatalf("the dump skipped resources: %v", res.Tables)
	}
	if len(res.Schema) == 0 {
		t.Fatal("the dump did not record the migration set")
	}
}

// The destructive half: dump, move the database on, write the archive back.
// Behind AGENTHUB_TEST_BACKUP_RESTORE because it empties every table, and it
// carries an extra audit_log frame to prove the restore drops blocks for tables
// this build no longer puts in a checkpoint.
func TestCopyEngineRestoreWritesTheArchiveBack(t *testing.T) {
	dsn := testDSN(t)
	if os.Getenv("AGENTHUB_TEST_BACKUP_RESTORE") != "true" {
		t.Skip("set AGENTHUB_TEST_BACKUP_RESTORE=true: the restore rewrites every table")
	}
	ctx := context.Background()
	eng := newCopyEngine(dsn)

	conn, err := connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var payload bytes.Buffer
	res, err := eng.Dump(ctx, &payload)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	// An archive written before audit_log was excluded: the restore has to skip
	// the block rather than refuse the whole file.
	if err := writeFrame(&payload, "audit_log", []string{"id"}, strings.NewReader(""), 0); err != nil {
		t.Fatalf("append an excluded-table frame: %v", err)
	}

	// Move the database on: a row that was never checkpointed appears.
	marker := "it-engine-marker"
	if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1) ON CONFLICT DO NOTHING`, marker); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	var marked int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE filename = $1`, marker).Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("marker before the restore = %d, %v", marked, err)
	}

	manifest := Manifest{Engine: EngineNative, Tables: res.Tables, RowCounts: res.RowCounts}
	if err := eng.Restore(ctx, bytes.NewReader(payload.Bytes()), manifest); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var after int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE filename = $1`, marker).Scan(&after); err != nil {
		t.Fatalf("count marker: %v", err)
	}
	if after != 0 {
		t.Fatal("a row written after the checkpoint survived the restore")
	}
	// Every table the manifest names comes back with the row count it promised.
	for table, want := range res.RowCounts {
		var got int64
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+quoteIdent(table)).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s after restore = %d rows, want %d", table, got, want)
		}
	}
}
