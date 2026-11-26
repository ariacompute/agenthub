package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The pg_dump engine is the path a production instance actually uses, and its
// interesting half — argument assembly, exit codes, output scrubbing — is
// independent of what the client binary does with those arguments.
//
// The client is replaced by a POSIX shell script, the way lookPath's comment says
// tests should: whether the real client is installed is a property of the machine,
// not of this code. The database arms still need a database, so the case is gated
// on DATABASE_URL; the script is POSIX only, so Windows skips with a reason.
//
// This also covers rowCounts and archiveSequences, which no other engine calls.

const fakeClientScript = `#!/bin/sh
# A stand-in for pg_dump / pg_restore: it records the argv it was given and
# produces the file the engine expects, without talking to PostgreSQL.
if [ -n "${FAKE_LOG:-}" ]; then
  printf '%s\n' "$*" >> "$FAKE_LOG"
fi
if [ "${FAKE_EXIT:-0}" != "0" ]; then
  echo "connection to server failed for $PGPASSWORD" >&2
  exit "$FAKE_EXIT"
fi
if [ "${FAKE_EMPTY:-0}" = "1" ]; then
  exit 0
fi
prev=""
for arg in "$@"; do
  if [ "$prev" = "--file" ]; then
    printf 'PGDMP-fake-archive' > "$arg"
  fi
  prev="$arg"
done
exit 0
`

func TestPgDumpEngineDrivesTheClient(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping DB-backed client test (make db-up)")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake PostgreSQL client is a POSIX shell script")
	}
	ctx := context.Background()

	schema := "it_pgdump_" + randomHex(t)
	admin := connectAdmin(t, ctx, dsn)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close(ctx)
		t.Skipf("cannot create an isolated schema: %v", err)
	}
	t.Cleanup(func() { dropIsolatedSchema(t, dsn, schema) })
	scoped := withSearchPath(t, dsn, schema)
	if _, err := admin.Exec(ctx, `SET search_path = `+schema); err != nil {
		t.Fatalf("pin the schema on the admin session: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE schema_migrations (filename text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`,
		`INSERT INTO schema_migrations (filename) VALUES ('001_resources.sql')`,
		`CREATE TABLE parent (id serial PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE child (id serial PRIMARY KEY, parent_id integer NOT NULL REFERENCES parent (id), note text)`,
		`INSERT INTO parent (name) VALUES ('one'), ('two')`,
		`INSERT INTO child (parent_id, note) VALUES (1, 'first')`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			_ = admin.Close(ctx)
			t.Fatalf("stage the schema: %v\n%s", err, stmt)
		}
	}
	_ = admin.Close(ctx)

	script := filepath.Join(t.TempDir(), "fake-pg-client")
	if err := os.WriteFile(script, []byte(fakeClientScript), 0o755); err != nil {
		t.Fatalf("write the fake client: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "client.log")
	t.Setenv("FAKE_LOG", logPath)

	original := lookPath
	t.Cleanup(func() { lookPath = original })
	lookPath = func(string) (string, error) { return script, nil }

	engine, err := newPgDumpEngine(scoped)
	if err != nil {
		t.Fatalf("build the engine: %v", err)
	}
	if engine.Name() != EnginePgDump {
		t.Fatalf("engine name = %q", engine.Name())
	}

	t.Run("dump names every table and the sequences", func(t *testing.T) {
		var archive bytes.Buffer
		result, err := engine.Dump(ctx, &archive)
		if err != nil {
			t.Fatalf("dump: %v", err)
		}
		if archive.String() == "" {
			t.Fatal("the archive was not streamed into the writer")
		}
		if result.RowCounts["parent"] != 2 || result.RowCounts["child"] != 1 {
			t.Fatalf("row counts = %v", result.RowCounts)
		}
		if len(result.Schema) != 1 || result.Schema[0] != "001_resources.sql" {
			t.Fatalf("schema = %v, want the applied migration", result.Schema)
		}

		argv := readClientLog(t, logPath)
		if len(argv) != 1 {
			t.Fatalf("the client ran %d times, want once", len(argv))
		}
		line := argv[0]
		for _, want := range []string{"--format=custom", "--no-owner", "--no-privileges", "--no-password", "--file"} {
			if !strings.Contains(line, want) {
				t.Fatalf("argv %q is missing %s", line, want)
			}
		}
		// A positive list, not an exclusion: every table is named explicitly.
		for _, table := range []string{"parent", "child", "schema_migrations"} {
			if !strings.Contains(line, "--table "+schema+"."+table) {
				t.Fatalf("argv %q does not name %s.%s", line, schema, table)
			}
		}
		// A SERIAL's sequence is not picked up by naming the table, so it is
		// named too.
		if !strings.Contains(line, "--table "+schema+".parent_id_seq") {
			t.Fatalf("argv %q does not archive the parent sequence", line)
		}
	})

	t.Run("a failing client is reported with the password scrubbed", func(t *testing.T) {
		if err := os.Remove(logPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reset the log: %v", err)
		}
		t.Setenv("FAKE_EXIT", "3")
		var archive bytes.Buffer
		_, err := engine.Dump(ctx, &archive)
		if !errors.Is(err, ErrEngineFailed) {
			t.Fatalf("dump = %v, want ErrEngineFailed", err)
		}
		if strings.Contains(err.Error(), "agenthub") {
			t.Fatalf("the error echoes the password: %v", err)
		}
		if !strings.Contains(err.Error(), "***") {
			t.Fatalf("the error does not show the scrubbed password: %v", err)
		}
		t.Setenv("FAKE_EXIT", "0")
	})

	t.Run("a client that writes nothing leaves no archive", func(t *testing.T) {
		t.Setenv("FAKE_EMPTY", "1")
		var archive bytes.Buffer
		if _, err := engine.Dump(ctx, &archive); err == nil {
			t.Fatal("a dump with no archive on disk must fail")
		}
		t.Setenv("FAKE_EMPTY", "0")
	})

	t.Run("restore passes --clean and fixes the sequences", func(t *testing.T) {
		if err := os.Remove(logPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reset the log: %v", err)
		}
		if err := engine.Restore(ctx, strings.NewReader("PGDMP-fake-archive"), Manifest{Engine: EnginePgDump}); err != nil {
			t.Fatalf("restore: %v", err)
		}
		argv := readClientLog(t, logPath)
		if len(argv) != 1 {
			t.Fatalf("the client ran %d times, want once", len(argv))
		}
		line := argv[0]
		for _, want := range []string{"--clean", "--if-exists", "--single-transaction", "--no-owner", "--no-privileges", "--no-password", "--schema " + schema} {
			if !strings.Contains(line, want) {
				t.Fatalf("argv %q is missing %s", line, want)
			}
		}
		fields := strings.Fields(line)
		if last := fields[len(fields)-1]; !strings.HasSuffix(last, "restore.pgc") {
			t.Fatalf("last argument = %q, want the staged archive", last)
		}
	})

	t.Run("a failing restore is reported", func(t *testing.T) {
		t.Setenv("FAKE_EXIT", "1")
		err := engine.Restore(ctx, strings.NewReader("PGDMP-fake-archive"), Manifest{Engine: EnginePgDump})
		if !errors.Is(err, ErrEngineFailed) {
			t.Fatalf("restore = %v, want ErrEngineFailed", err)
		}
		t.Setenv("FAKE_EXIT", "0")
	})
}

// TestQualifiedAlwaysNamesTheSchema pins the one thing the client flags depend on.
func TestQualifiedAlwaysNamesTheSchema(t *testing.T) {
	if got := qualified("public", "resources"); got != "public.resources" {
		t.Fatalf("qualified = %q", got)
	}
}

func readClientLog(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the client log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func randomHex(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random name: %v", err)
	}
	return hex.EncodeToString(buf)
}

func connectAdmin(t *testing.T, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

func dropIsolatedSchema(t *testing.T, dsn, schema string) {
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

// withSearchPath returns the DSN with current_schema() pinned to the isolated
// schema. It is shared with the external test package's round trip, but defined
// here because an in-package test cannot import internal/store.
func withSearchPath(t *testing.T, dsn, schema string) string {
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
