// Package backup_test exercises the restore path end to end without emptying the
// database the rest of the suite is using.
//
// Every statement in copy.go resolves through current_schema(), so a DSN that
// pins search_path gives the engine a private copy of the whole schema to
// truncate and reload. The public schema is never touched, which is why this case
// needs no AGENTHUB_TEST_BACKUP_RESTORE switch: that switch guards the
// destructive variant in engine_db_test.go.
//
// The external test package is deliberate. Migrating the isolated schema needs
// internal/store, and internal/store imports internal/backup (backup_store.go), so
// an in-package test would close an import cycle. Through the exported Engine
// interface the coverage still lands on internal/backup.
package backup_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"agenthub/internal/backup"
	"agenthub/internal/store"

	"github.com/jackc/pgx/v5"
)

func TestCopyEngineRestoresAnIsolatedSchema(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping DB-backed restore test (make db-up)")
	}
	ctx := context.Background()

	schema := newIsolatedSchema(t)
	scoped := scopedDSN(t, dsn, schema)
	createSchema(t, ctx, dsn, schema)
	// Registered before the pool is opened, so it runs after the pool is closed
	// (cleanups are LIFO).
	t.Cleanup(func() { dropSchema(t, dsn, schema) })

	// RLS is relaxed for the migration (the same posture `make db-migrate` uses):
	// a policy must never decide whether DDL or a seed statement runs.
	db, err := store.OpenWithOptions(scoped, store.OpenOptions{})
	if err != nil {
		t.Fatalf("open the isolated schema: %v", err)
	}
	t.Cleanup(func() { _ = store.Close(db) })

	if err := store.Migrate(db); err != nil {
		if isPrivilegeError(err) {
			t.Skipf("this role cannot finish the schema inside an isolated schema: %v", err)
		}
		t.Fatalf("migrate the isolated schema: %v", err)
	}

	// A parent, a child and a sibling: enough to prove insertOrder ran (the child
	// cannot be written before its parent) and that the foreign key survived.
	const (
		resourceID  = "it-resource"
		taskID      = "it-task"
		artifactID  = "it-artifact"
		lateResID   = "it-resource-late"
		secondArtID = "it-artifact-after"
	)
	seed := []string{
		`INSERT INTO resources (id, kind, name, version, display_name)
		 VALUES ('` + resourceID + `', 'model', 'round trip', '1.0.0', 'Round trip')`,
		`INSERT INTO tasks (id, kind, resource_id, state)
		 VALUES ('` + taskID + `', 'model', '` + resourceID + `', 'completed')`,
		`INSERT INTO task_artifacts (task_id, artifact_id, name)
		 VALUES ('` + taskID + `', '` + artifactID + `', 'answer')`,
	}
	for _, stmt := range seed {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}

	engine, err := backup.SelectEngine(scoped, backup.EngineNative)
	if err != nil {
		t.Fatalf("select the native engine: %v", err)
	}
	if engine.Name() != backup.EngineNative {
		t.Fatalf("engine = %q, want %q", engine.Name(), backup.EngineNative)
	}

	var archive bytes.Buffer
	dumped, err := engine.Dump(ctx, &archive)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if dumped.RowCounts["resources"] != 1 || dumped.RowCounts["tasks"] != 1 || dumped.RowCounts["task_artifacts"] != 1 {
		t.Fatalf("row counts = %v, want one row in each seeded table", dumped.RowCounts)
	}
	if len(dumped.Schema) == 0 {
		t.Fatal("the dump did not report the applied migrations")
	}

	// Rewrite the world, then let the archive put it back.
	for _, stmt := range []string{
		`DELETE FROM task_artifacts`,
		`DELETE FROM tasks`,
		`DELETE FROM resources`,
		`INSERT INTO resources (id, kind, name) VALUES ('` + lateResID + `', 'model', 'after the checkpoint')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("mutate: %v\n%s", err, stmt)
		}
	}

	manifest := backup.Manifest{
		Engine:    backup.EngineNative,
		Tables:    dumped.Tables,
		RowCounts: dumped.RowCounts,
		Schema:    dumped.Schema,
	}
	if err := engine.Restore(ctx, bytes.NewReader(archive.Bytes()), manifest); err != nil {
		t.Fatalf("restore: %v", err)
	}

	for table, want := range map[string]int64{"resources": 1, "tasks": 1, "task_artifacts": 1} {
		var got int64
		if err := db.Raw(`SELECT count(*) FROM ` + table).Scan(&got).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s has %d rows after restore, want %d", table, got, want)
		}
	}
	// The row written after the checkpoint is gone: the restore truncated the
	// tables instead of merging into them.
	var late int64
	if err := db.Raw(`SELECT count(*) FROM resources WHERE id = ?`, lateResID).Scan(&late).Error; err != nil {
		t.Fatalf("look for the late row: %v", err)
	}
	if late != 0 {
		t.Fatalf("restore kept %d row(s) written after the checkpoint", late)
	}
	// Parent and child came back in the right order and still point at each other.
	var joined int64
	if err := db.Raw(`SELECT count(*) FROM task_artifacts a JOIN tasks t ON t.id = a.task_id WHERE t.id = ?`, taskID).Scan(&joined).Error; err != nil {
		t.Fatalf("join restored rows: %v", err)
	}
	if joined != 1 {
		t.Fatalf("the restored artifact is not attached to its task (join rows = %d)", joined)
	}

	// The migrator's own table is part of the archive, so the manifest and the
	// database agree afterwards.
	applied, err := store.Applied(db)
	if err != nil {
		t.Fatalf("read applied migrations: %v", err)
	}
	if strings.Join(applied, ",") != strings.Join(dumped.Schema, ",") {
		t.Fatalf("applied = %v, manifest = %v", applied, dumped.Schema)
	}

	// fixSequences ran: the sequence behind task_artifacts was advanced past the
	// restored maximum, so the next insert does not collide with a restored id.
	if err := db.Exec(`INSERT INTO task_artifacts (task_id, artifact_id, name) VALUES (?, ?, ?)`,
		taskID, secondArtID, "after restore").Error; err != nil {
		t.Fatalf("insert after restore (a sequence left at 1 would collide): %v", err)
	}
}

// newIsolatedSchema names the schema. Only hex is used, so the name needs no
// quoting in the statements below.
func newIsolatedSchema(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random schema name: %v", err)
	}
	return "it_backup_" + hex.EncodeToString(buf)
}

// scopedDSN pins search_path to the isolated schema. OpenWithOptions appends its
// own app.system=on setting to the same option string instead of replacing it, so
// both survive.
func scopedDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		t.Skipf("DATABASE_URL is not a URL DSN, so search_path cannot be pinned")
	}
	q := u.Query()
	opt := "-c search_path=" + schema
	if existing := q.Get("options"); existing != "" {
		opt = existing + " " + opt
	}
	q.Set("options", opt)
	u.RawQuery = q.Encode()
	return u.String()
}

// createSchema makes the schema or skips: creating one needs CREATE on the
// database, which a least-privilege application role does not have.
func createSchema(t *testing.T, ctx context.Context, dsn, schema string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	var allowed bool
	if err := conn.QueryRow(ctx, `SELECT has_database_privilege(current_database(), 'CREATE')`).Scan(&allowed); err != nil {
		t.Fatalf("read the CREATE privilege: %v", err)
	}
	if !allowed {
		var role string
		if err := conn.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil {
			t.Fatalf("read the role: %v", err)
		}
		t.Skipf("role %q may not create a schema: this case needs the migration role", role)
	}
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		if isPrivilegeError(err) {
			t.Skipf("cannot create an isolated schema: %v", err)
		}
		t.Fatalf("create schema %s: %v", schema, err)
	}
}

// dropSchema cleans up on a fresh connection: the pool is closed before this
// runs, and DROP needs a session that is not pinned to the schema being dropped.
func dropSchema(t *testing.T, dsn, schema string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Errorf("reconnect to drop %s: %v", schema, err)
		return
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
		t.Errorf("drop schema %s: %v", schema, err)
	}
}

// isPrivilegeError tells "this instance keeps me from doing this" apart from "the
// code is wrong". Only the first may skip; a broken migration has to fail.
func isPrivilegeError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "must be owner") ||
		strings.Contains(msg, "insufficient privilege")
}
