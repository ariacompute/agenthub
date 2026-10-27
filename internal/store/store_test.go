package store

import (
	"errors"
	"io/fs"
	"sort"
	"testing"

	"agenthub/internal/registry"
	"agenthub/migrations"
)

func TestOpenRejectsEmptyDSN(t *testing.T) {
	if _, err := Open("", false); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Open(\"\") = %v, want ErrNoDatabase", err)
	}
}

func TestPingAndCloseWithoutDatabase(t *testing.T) {
	if err := Ping(nil); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Ping(nil) = %v, want ErrNoDatabase", err)
	}
	if err := Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v", err)
	}
}

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "password in userinfo",
			in:   "postgres://agenthub:secret@localhost:5434/agenthub",
			want: "postgres://agenthub:REDACTED@localhost:5434/agenthub",
		},
		{
			name: "user without password",
			in:   "postgres://agenthub@localhost:5434/agenthub",
			want: "postgres://agenthub@localhost:5434/agenthub",
		},
		{
			name: "password as a query parameter",
			in:   "postgres://localhost:5434/agenthub?password=secret",
			want: "postgres://localhost:5434/agenthub?password=REDACTED",
		},
	}
	for _, c := range cases {
		if got := RedactDSN(c.in); got != c.want {
			t.Fatalf("%s: RedactDSN = %q, want %q", c.name, got, c.want)
		}
		if contains(RedactDSN(c.in), "secret") {
			t.Fatalf("%s: secret leaked into %q", c.name, RedactDSN(c.in))
		}
	}
	// A DSN url.Parse cannot split must degrade instead of leaking.
	if got := RedactDSN("postgres://user:pass@[::1/agenthub"); !contains(got, Redacted) {
		t.Fatalf("RedactDSN of a broken dsn = %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestMigrateRequiresDatabase(t *testing.T) {
	if err := Migrate(nil); err == nil {
		t.Fatal("Migrate(nil) must fail")
	}
}

func TestSeedRequiresDatabase(t *testing.T) {
	if err := Seed(nil); err == nil {
		t.Fatal("Seed(nil) must fail")
	}
}

func TestEmbeddedMigrationsAreOrderedAndIdempotent(t *testing.T) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			names = append(names, e.Name())
		}
	}
	want := []string{
		"001_resources.sql", "002_tasks.sql", "003_events.sql", "004_push_records.sql",
		"005_auth.sql", "006_rbac.sql", "007_backups.sql",
		"008_registry_governance.sql", "009_registry_sync.sql", "010_api_keys.sql",
		"011_login_protection.sql", "012_threads.sql",
		"013_persona.sql", "014_channels.sql", "015_thread_origin.sql",
	}
	if len(names) != len(want) {
		t.Fatalf("embedded migrations = %v, want %v", names, want)
	}
	sort.Strings(names)
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("migration %d = %q, want %q", i, names[i], want[i])
		}
	}

	for _, name := range names {
		raw, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		stmts := splitStatements(string(raw))
		if len(stmts) == 0 {
			t.Fatalf("%s produced no statements", name)
		}
		for _, stmt := range stmts {
			switch {
			case hasPrefix(stmt, "CREATE TABLE"):
				if !contains(stmt, "IF NOT EXISTS") {
					t.Fatalf("%s: CREATE TABLE is not idempotent: %q", name, stmt)
				}
			case hasPrefix(stmt, "CREATE INDEX"), hasPrefix(stmt, "CREATE UNIQUE INDEX"):
				if !contains(stmt, "IF NOT EXISTS") {
					t.Fatalf("%s: CREATE INDEX is not idempotent: %q", name, stmt)
				}
			}
			if contains(stmt, ";") {
				t.Fatalf("%s: statement still carries a separator: %q", name, stmt)
			}
		}
	}
}

func TestSplitStatementsSkipsCommentsAndBlanks(t *testing.T) {
	got := splitStatements("-- a comment\n\nCREATE TABLE IF NOT EXISTS t (\n  id TEXT PRIMARY KEY\n);\n-- trailing\nSELECT 1;\n")
	want := []string{
		"CREATE TABLE IF NOT EXISTS t ( id TEXT PRIMARY KEY )",
		"SELECT 1",
	}
	if len(got) != len(want) {
		t.Fatalf("statements = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statement %d = %q, want %q", i, got[i], want[i])
		}
	}
	if stmts := splitStatements("-- nothing but a comment\n"); len(stmts) != 0 {
		t.Fatalf("statements = %#v, want none", stmts)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestDemoResourcesCoverEveryKind(t *testing.T) {
	entries := DemoResources()
	if len(entries) != len(registry.Kinds) {
		t.Fatalf("demo resources = %d, want %d", len(entries), len(registry.Kinds))
	}
	seen := map[registry.Kind]bool{}
	for _, e := range entries {
		if e.ID == "" {
			t.Fatalf("demo resource without an id: %+v", e)
		}
		if !e.Kind.Valid() {
			t.Fatalf("demo resource %s has an invalid kind %q", e.ID, e.Kind)
		}
		if e.Health == "" {
			t.Fatalf("demo resource %s has no health", e.ID)
		}
		if e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
			t.Fatalf("demo resource %s has no timestamps", e.ID)
		}
		seen[e.Kind] = true
	}
	for _, k := range registry.Kinds {
		if !seen[k] {
			t.Fatalf("no demo resource of kind %s", k)
		}
	}
}
