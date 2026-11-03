// Package testdb is the gate every integration case in this repository shares.
//
// It exists because DATABASE_URL can point at two very different roles. A
// development box and the CI service container hand the tests a superuser; a
// production-shaped deployment hands them agenthub_app, which has DML rights and
// nothing else — no CREATE on the schema, no CREATEROLE. Before this package a
// case that built a probe table simply failed there with "permission denied for
// schema public", which reads like a product bug and hides the real question:
// does the repository still behave under the role it actually runs as?
//
// Two answers, both honest:
//
//   - the suite runs, and the cases that need no DDL prove the production shape;
//   - the cases that do need DDL skip with a reason that names the role and the
//     missing privilege, instead of failing.
//
// A throwaway schema is not a way out: creating one needs CREATE on the
// database, and the runtime role has neither. The migrator role is the one that
// owns the schema, so it is the one that can run these cases.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

// DSN returns DATABASE_URL, skipping the case when it is unset. Every DB-backed
// case in the repo gates on this, so an offline `go test ./...` stays green.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping DB-backed test (make db-up)")
	}
	return dsn
}

// Connect opens one connection for the duration of the case. It is a plain
// connection (no pool) so a case can close it deterministically.
func Connect(t *testing.T, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// CanDDL reports whether the session may create objects in its current schema.
// It is the one privilege the probe-table cases need, asked directly instead of
// being inferred from a failure message.
func CanDDL(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var ok bool
	err := conn.QueryRow(ctx, `SELECT has_schema_privilege(current_schema(), 'CREATE')`).Scan(&ok)
	return ok, err
}

// RequireDDL skips the case when the runtime role cannot create objects, naming
// the privilege instead of letting the DDL fail as "permission denied".
func RequireDDL(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn := Connect(t, ctx, dsn)
	ok, err := CanDDL(ctx, conn)
	if err != nil {
		t.Fatalf("read the schema privilege: %v", err)
	}
	if !ok {
		var role string
		_ = conn.QueryRow(ctx, `SELECT current_user`).Scan(&role)
		t.Skipf("role %q has DML only (no CREATE on the current schema): this case builds a probe table, so it needs the migrator role", role)
	}
}

// MigrateOrExisting runs the embedded migrations and tolerates a failure when the
// schema is already there.
//
// That is the production shape: the migrator created the schema once and the
// runtime role runs the tests. Asking a DML-only role to migrate would fail for a
// reason that has nothing to do with the code under test, so the helper checks
// whether the tables exist before deciding it is a real error.
func MigrateOrExisting(t *testing.T, db *gorm.DB, migrate func(*gorm.DB) error) {
	t.Helper()
	err := migrate(db)
	if err == nil {
		return
	}
	var present bool
	if qerr := db.Raw(`SELECT to_regclass('public.tasks') IS NOT NULL`).Scan(&present).Error; qerr != nil {
		t.Fatalf("migrate: %v (and the schema could not be inspected: %v)", err, qerr)
	}
	if !present {
		t.Fatalf("migrate: %v (and the schema is missing: run with a role that may create objects)", err)
	}
	t.Logf("migrate skipped, the schema already exists: %v", err)
}
