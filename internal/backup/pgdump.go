package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgDumpEngine shells out to the PostgreSQL client tools. It is preferred when
// they exist: it produces the archive format every DBA already has tooling for,
// handles types this package would otherwise have to teach itself, and restores
// through one tool invocation.
//
// The client is looked up once at construction. A container built without it
// never shells out at all — it falls back to the COPY engine — so the absence
// of the binary is a normal configuration, not an error to recover from.
type pgDumpEngine struct {
	dsn        string
	dumpBin    string
	restoreBin string
}

func newPgDumpEngine(dsn string) (*pgDumpEngine, error) {
	dumpBin, err := exec.LookPath("pg_dump")
	if err != nil {
		return nil, fmt.Errorf("%w: pg_dump not found: %v", ErrEngineUnavailable, err)
	}
	restoreBin, err := exec.LookPath("pg_restore")
	if err != nil {
		return nil, fmt.Errorf("%w: pg_restore not found: %v", ErrEngineUnavailable, err)
	}
	return &pgDumpEngine{dsn: dsn, dumpBin: dumpBin, restoreBin: restoreBin}, nil
}

// Name returns the engine identifier stored in the manifest.
func (e *pgDumpEngine) Name() string { return EnginePgDump }

// Dump writes a custom format archive to w. pg_dump can only write to a file,
// so the archive is staged in a temporary directory and streamed into w.
func (e *pgDumpEngine) Dump(ctx context.Context, w io.Writer) (DumpResult, error) {
	cfg, err := pgconn.ParseConfig(e.dsn)
	if err != nil {
		return DumpResult{}, fmt.Errorf("%w: parse DATABASE_URL: %v", ErrEngineUnavailable, err)
	}
	dir, err := os.MkdirTemp("", "agenthub-pgdump-")
	if err != nil {
		return DumpResult{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	schema, err := currentSchema(ctx, e.dsn)
	if err != nil {
		return DumpResult{}, err
	}
	archive := filepath.Join(dir, "dump.pgc")
	args := []string{
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		// Never prompt for a password: a background backup must fail rather
		// than block a scheduler forever.
		"--no-password",
		"--file", archive,
		"--schema", schema,
	}
	for table := range excludedTables {
		args = append(args, "--exclude-table", table)
	}
	args = append(args, connArgs(cfg)...)

	if out, err := runClient(ctx, e.dumpBin, args, cfg.Password); err != nil {
		return DumpResult{}, fmt.Errorf("%w: pg_dump: %v: %s", ErrEngineFailed, err, sanitizeOutput(out, cfg.Password))
	}

	f, err := os.Open(archive)
	if err != nil {
		return DumpResult{}, err
	}
	if _, err := io.Copy(w, f); err != nil {
		_ = f.Close()
		return DumpResult{}, err
	}
	if err := f.Close(); err != nil {
		return DumpResult{}, err
	}

	// pg_dump reports nothing about what it wrote, so the manifest is filled in
	// from the database itself.
	conn, err := connect(ctx, e.dsn)
	if err != nil {
		return DumpResult{}, err
	}
	defer func() { _ = conn.Close(ctx) }()

	tables, err := discoverTables(ctx, conn)
	if err != nil {
		return DumpResult{}, err
	}
	counts, err := rowCounts(ctx, conn, tables)
	if err != nil {
		return DumpResult{}, err
	}
	schemaFiles, err := appliedMigrations(ctx, conn)
	if err != nil {
		return DumpResult{}, err
	}
	return DumpResult{Tables: tables, RowCounts: counts, Schema: schemaFiles}, nil
}

// Restore rewrites the database from a custom format archive.
//
// --clean --if-exists drops what is there and --single-transaction wraps the
// whole thing, so a failure rolls back instead of leaving a half written
// database. pg_restore needs random access to a custom archive, so the payload
// is materialised in a temporary file first.
func (e *pgDumpEngine) Restore(ctx context.Context, r io.Reader, m Manifest) error {
	cfg, err := pgconn.ParseConfig(e.dsn)
	if err != nil {
		return fmt.Errorf("%w: parse DATABASE_URL: %v", ErrEngineUnavailable, err)
	}
	dir, err := os.MkdirTemp("", "agenthub-pgrestore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	archive := filepath.Join(dir, "restore.pgc")
	f, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	schema, err := currentSchema(ctx, e.dsn)
	if err != nil {
		return err
	}
	args := []string{
		"--clean",
		"--if-exists",
		"--single-transaction",
		"--no-owner",
		"--no-privileges",
		"--no-password",
		"--schema", schema,
	}
	args = append(args, connArgs(cfg)...)
	args = append(args, archive)

	if out, err := runClient(ctx, e.restoreBin, args, cfg.Password); err != nil {
		return fmt.Errorf("%w: pg_restore: %v: %s", ErrEngineFailed, err, sanitizeOutput(out, cfg.Password))
	}

	// A restore reloads rows with explicit keys, which leaves sequences behind.
	conn, err := connect(ctx, e.dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return fixSequences(ctx, conn)
}

// connArgs turns a parsed DSN into the flags the client tools understand. The
// password never travels on the command line: it is passed through the
// environment, where ps and the shell history cannot see it.
func connArgs(cfg *pgconn.Config) []string {
	args := []string{"-d", cfg.Database, "-U", cfg.User}
	if cfg.Host != "" {
		args = append(args, "-h", cfg.Host)
	}
	if cfg.Port != 0 {
		args = append(args, "-p", strconv.Itoa(int(cfg.Port)))
	}
	return args
}

func runClient(ctx context.Context, bin string, args []string, password string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	return cmd.CombinedOutput()
}

// sanitizeOutput trims client output for an error message. The DSN password is
// scrubbed because a connection error can echo the connection string, and
// backups are logged, not printed.
func sanitizeOutput(out []byte, password string) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "no output"
	}
	if password != "" {
		text = strings.ReplaceAll(text, password, "***")
	}
	const limit = 400
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	return text
}

// currentSchema is the schema the tools are pointed at, so a dump of the
// agenthub tables never drags in another schema that happens to share the
// database.
func currentSchema(ctx context.Context, dsn string) (string, error) {
	conn, err := connect(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()
	var schema string
	if err := conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return "", fmt.Errorf("backup: read current schema: %w", err)
	}
	return schema, nil
}
