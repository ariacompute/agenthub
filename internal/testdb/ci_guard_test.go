package testdb

// The other half of the gate.
//
// DSN skips when DATABASE_URL is unset, which is what keeps an offline
// `go test ./...` green. The same behaviour is a trap in CI: every DB-backed case
// self-skips, the run stays green, and the coverage number that comes out
// describes the offline subset rather than the product. The round-11 report
// (agenthub-completeness-round11.md §7 #3 / §9.2) named exactly that risk for the
// gated owner-isolation matrix in internal/store.
//
// So CI without a database fails here instead, with the variable named and the
// fix in the message. `github.com/.../ci.yml` also asserts the variable before
// any test runs; this case is the part that cannot be lost by editing a workflow
// only.

import (
	"os"
	"testing"
)

func TestDatabaseURLIsSetInCI(t *testing.T) {
	if os.Getenv("DATABASE_URL") != "" {
		return
	}
	if os.Getenv("CI") == "" && os.Getenv("GITHUB_ACTIONS") == "" {
		t.Skip("DATABASE_URL unset and not in CI: DB-backed cases skip by design")
	}
	t.Fatal("DATABASE_URL is required in CI: without it every DB-backed case " +
		"(internal/store, internal/backup, tests/) skips and the suite reports on " +
		"the offline subset. Start PostgreSQL and export DATABASE_URL — see " +
		".github/workflows/ci.yml, or `make db-up` locally.")
}
