package backup

// The pg_dump engine only runs on a host with the PostgreSQL client, so its
// dump and restore need a real database. Proved here is the part that must be
// right on every host: how the connection is handed to a child process, and
// how that child's output is made safe to log.

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestConnArgsCarryTheConnectionButNeverThePassword(t *testing.T) {
	cfg, err := pgconn.ParseConfig("postgres://agenthub:s3cr3t@db.internal:5434/agenthub?sslmode=disable")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	args := connArgs(cfg)
	joined := strings.Join(args, " ")

	for _, want := range []string{"-d agenthub", "-U agenthub", "-h db.internal", "-p 5434"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args = %q, want it to contain %q", joined, want)
		}
	}
	// The password is passed through PGPASSWORD, in the child's environment:
	// an argv entry is visible to every process on the host.
	if strings.Contains(joined, "s3cr3t") || strings.Contains(joined, "PGPASSWORD") {
		t.Errorf("args = %q, want no password material", joined)
	}
}

func TestConnArgsOmitsWhatTheClientCanDefault(t *testing.T) {
	// A DSN that names only the database: pg_dump can default host and port
	// from the environment, and flags we do not need are flags that cannot
	// contradict it.
	cfg := &pgconn.Config{Database: "agenthub"}
	joined := strings.Join(connArgs(cfg), " ")
	if strings.Contains(joined, "-h") || strings.Contains(joined, "-p") {
		t.Errorf("args = %q, want no host or port flag when the DSN has none", joined)
	}
	if !strings.Contains(joined, "-d agenthub") {
		t.Errorf("args = %q, want the database", joined)
	}
}

// A failed pg_dump prints the connection it tried, password included, and that
// text ends up in an error that gets logged.
func TestSanitizeOutputScrubsThePasswordBeforeItIsLogged(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		password string
		want     string
	}{
		{"empty output", "   \n", "s3cr3t", "no output"},
		{"password scrubbed", "connection to db failed: password s3cr3t rejected", "s3cr3t", "connection to db failed: password *** rejected"},
		{"no password to scrub", "pg_dump: error: relation missing", "", "pg_dump: error: relation missing"},
	}
	for _, c := range cases {
		if got := sanitizeOutput([]byte(c.out), c.password); got != c.want {
			t.Errorf("%s: sanitizeOutput = %q, want %q", c.name, got, c.want)
		}
	}

	// A client that dumps a thousand lines of diagnostics must not dump a
	// thousand lines into the log.
	long := strings.Repeat("x", 900)
	got := sanitizeOutput([]byte(long), "")
	if n := len([]rune(got)); n != 401 {
		t.Errorf("sanitizeOutput of %d bytes = %d characters, want it trimmed to 401", len(long), n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("sanitizeOutput = %q, want the ellipsis that marks a trim", got)
	}
}

// The client is optional by design: a container without it has to fall back to
// the COPY engine rather than fail every checkpoint.
func TestNewPgDumpEngineIsHonestAboutAMissingClient(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err == nil {
		t.Skip("pg_dump is installed here; the missing-client path belongs to a host without it")
	}
	engine, err := newPgDumpEngine("postgres://u:p@localhost:5432/db")
	if engine != nil {
		t.Fatal("newPgDumpEngine returned an engine without the client")
	}
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("error = %v, want ErrEngineUnavailable so the caller can fall back", err)
	}
}

func TestPgDumpEngineNameIsTheOneTheManifestStores(t *testing.T) {
	if (&pgDumpEngine{}).Name() != EnginePgDump {
		t.Fatal("Name must be the identifier the restore side looks for")
	}
}
