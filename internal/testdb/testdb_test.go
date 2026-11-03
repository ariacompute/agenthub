package testdb

// The gate itself, not the cases behind it.
//
// DSN decides whether the whole integration suite runs or skips, and both
// answers are load-bearing: returning the DSN keeps the suite honest when a
// database is configured, and skipping — rather than failing — is what lets an
// offline `go test ./...` stay green without pretending the cases passed.
//
// Everything else in this package needs a live connection, so it is exercised by
// the DB-gated cases themselves (tests/rls_test.go, internal/store/*_test.go)
// and by nothing here.

import "testing"

func TestDSNReturnsTheConfiguredURL(t *testing.T) {
	const dsn = "postgres://agenthub:secret@127.0.0.1:5432/agenthub?sslmode=disable"
	t.Setenv("DATABASE_URL", dsn)

	if got := DSN(t); got != dsn {
		t.Fatalf("DSN = %q, want the environment value verbatim", got)
	}
}

func TestDSNSkipsInsteadOfFailingWhenUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	// DSN calls t.Skip, which ends the subtest: reaching the line after it means
	// the gate returned an empty DSN and every DB-backed case would have run
	// against nothing.
	returned := false
	t.Run("offline", func(t *testing.T) {
		DSN(t)
		returned = true
	})
	if returned {
		t.Fatal("DSN returned with DATABASE_URL unset; the suite would run against nothing")
	}
}
