package backup

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// copyEngine dumps the database over a plain SQL connection using the COPY
// protocol: one framed block per table, no external binary, no dependency on
// the PostgreSQL major version the server happens to run. It is what an alpine
// container without the client tools falls back to.
//
// The payload is a small framed container rather than one concatenated stream,
// because COPY text rows can legitimately start with any characters a delimiter
// might use — only an explicit length is unambiguous.
type copyEngine struct {
	dsn string
}

// payloadMagic identifies a native payload and its framing version.
const payloadMagic = "agenthub-checkpoint-1\n"

// columnSeparator joins the column list inside a frame; a unit separator cannot
// appear in a Postgres identifier.
const columnSeparator = "\x1f"

// excludedTables are never dumped and never truncated.
//
// The catalogue rows are here because they are not application state: dropping
// them on a restore would erase the record of the checkpoint the operator is
// restoring from — including the one taken seconds earlier as a way back.
//
// audit_log is here because it is an append-only ledger, not state: 025 refuses
// UPDATE and TRUNCATE for every role, owner and superuser included, and the
// hash chain only means something if the rows it covers cannot be rewound.
// Carrying it in a checkpoint would leave the restore with two bad answers —
// rewrite the evidence or fail — so it is neither dumped nor truncated. An
// operator who needs the ledger elsewhere exports it; it is deliberately not
// something a rewind can take away.
var excludedTables = map[string]bool{
	"audit_log":          true,
	"backup_checkpoints": true,
	"backup_schedules":   true,
}

func newCopyEngine(dsn string) *copyEngine { return &copyEngine{dsn: dsn} }

// Name returns the engine identifier stored in the manifest.
func (e *copyEngine) Name() string { return EngineNative }

// Dump writes every application table to w as a framed COPY stream.
func (e *copyEngine) Dump(ctx context.Context, w io.Writer) (DumpResult, error) {
	conn, err := connect(ctx, e.dsn)
	if err != nil {
		return DumpResult{}, err
	}
	defer func() { _ = conn.Close(ctx) }()

	tables, err := discoverTables(ctx, conn)
	if err != nil {
		return DumpResult{}, err
	}
	// A checkpoint this engine cannot load back is worse than none: the operator
	// would only find out when they need it. So the guard runs on the way in as
	// well, while there is still time to switch engines.
	if err := ensureRewritable(ctx, conn, tables); err != nil {
		return DumpResult{}, err
	}
	res := DumpResult{Tables: tables, RowCounts: map[string]int64{}}
	if _, err := io.WriteString(w, payloadMagic); err != nil {
		return DumpResult{}, err
	}
	dir, err := os.MkdirTemp("", "agenthub-dump-")
	if err != nil {
		return DumpResult{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	for _, table := range tables {
		cols, err := tableColumns(ctx, conn, table)
		if err != nil {
			return DumpResult{}, err
		}
		stage, err := newStageDump(dir, table)
		if err != nil {
			return DumpResult{}, err
		}
		// An explicit column list on both sides is what keeps the write back
		// aligned even if a table's physical order ever changes.
		sql := fmt.Sprintf(`COPY (SELECT %s FROM %s) TO STDOUT`, columnList(cols), quoteIdent(table))
		tag, err := conn.PgConn().CopyTo(ctx, stage, sql)
		if err != nil {
			_ = stage.Close()
			return DumpResult{}, fmt.Errorf("%w: copy %s: %v", ErrEngineFailed, table, err)
		}
		size, err := stage.Size()
		if err != nil {
			_ = stage.Close()
			return DumpResult{}, err
		}
		payload, _, err := stage.Reader()
		if err != nil {
			_ = stage.Close()
			return DumpResult{}, err
		}
		if err := writeFrame(w, table, cols, payload, size); err != nil {
			_ = stage.Close()
			return DumpResult{}, err
		}
		if err := stage.Close(); err != nil {
			return DumpResult{}, err
		}
		res.RowCounts[table] = tag.RowsAffected()
	}
	schema, err := appliedMigrations(ctx, conn)
	if err != nil {
		return DumpResult{}, err
	}
	res.Schema = schema
	return res, nil
}

// Restore rewrites every application table from a native payload. The whole
// rewrite runs in one transaction: a failure leaves the database untouched
// rather than half restored.
func (e *copyEngine) Restore(ctx context.Context, r io.Reader, m Manifest) error {
	conn, err := connect(ctx, e.dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	br := bufio.NewReader(r)
	magic, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArchive, err)
	}
	if magic != payloadMagic {
		return fmt.Errorf("%w: unsupported payload framing", ErrArchive)
	}

	// Refuse an archive that names tables this database does not have: it would
	// mean the archive came from a different schema than the running build.
	known, err := discoverTables(ctx, conn)
	if err != nil {
		return err
	}
	knownSet := make(map[string]bool, len(known))
	for _, t := range known {
		knownSet[t] = true
	}
	for _, t := range m.Tables {
		// A table the manifest names but this build keeps out of the checkpoint
		// is an archive from before the exclusion: readable, not an error.
		if frameDisposition(t, knownSet) == frameRefuse {
			return fmt.Errorf("%w: %q", ErrUnknownTable, t)
		}
	}
	// This engine empties the schema with one TRUNCATE. A table that refuses to
	// be truncated would fail that statement with a database error the operator
	// cannot act on, so the refusal happens here, by name, before anything is
	// staged or locked.
	if err := ensureRewritable(ctx, conn, known); err != nil {
		return err
	}

	// The payload arrives in the dump's order, but a reload has to write
	// parents before children: a foreign key is checked row by row, so each
	// table's block is staged on disk and written back in dependency order.
	dir, err := os.MkdirTemp("", "agenthub-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	type block struct {
		cols []string
		path string
	}
	blocks := map[string]block{}
	for {
		table, cols, size, err := readFrame(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch frameDisposition(table, knownSet) {
		case frameRefuse:
			return fmt.Errorf("%w: %q", ErrUnknownTable, table)
		case frameDrop:
			// The archive was written while this table was still part of the
			// checkpoint. Its block is consumed and dropped, so yesterday's
			// archive stays loadable after the table was taken out.
			if _, err := io.Copy(io.Discard, io.LimitReader(br, size)); err != nil {
				return err
			}
			continue
		}
		// Staged as a plain temporary file: this one has to survive until the
		// table's turn comes, unlike the staging a dump uses.
		f, err := os.CreateTemp(dir, table+"-*.data")
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(br, size)); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		blocks[table] = block{cols: cols, path: f.Name()}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// CASCADE empties referencing tables with the referenced ones, so no
	// ordering is needed here.
	if err := truncateAll(ctx, tx.Conn(), known); err != nil {
		return err
	}
	order, err := insertOrder(ctx, conn, known)
	if err != nil {
		return err
	}
	for _, table := range order {
		b, ok := blocks[table]
		if !ok {
			// A table the archive predates stays empty: the schema check has
			// already refused anything this build cannot read.
			continue
		}
		f, err := os.Open(b.path)
		if err != nil {
			return err
		}
		sql := fmt.Sprintf(`COPY %s (%s) FROM STDIN`, quoteIdent(table), columnList(b.cols))
		if _, err := tx.Conn().PgConn().CopyFrom(ctx, f, sql); err != nil {
			_ = f.Close()
			return fmt.Errorf("%w: copy %s: %v", ErrEngineFailed, table, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	// A COPY that wrote explicit keys does not move a sequence, so a table that
	// gains one later would start from 1 and collide.
	if err := fixSequences(ctx, tx.Conn()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// insertOrder sorts the tables parents first.
//
// TRUNCATE ... CASCADE can empty a whole graph in one statement, but INSERT
// cannot: a foreign key is validated per row, so events must wait for the tasks
// they belong to. The order is derived from the foreign keys themselves, which
// means a migration that adds a referencing table needs no change here.
func insertOrder(ctx context.Context, conn *pgx.Conn, tables []string) ([]string, error) {
	parentOf, err := foreignKeys(ctx, conn)
	if err != nil {
		return nil, err
	}
	return orderTables(tables, parentOf), nil
}

// orderTables is the sort itself, kept free of the connection so the ordering
// can be tested without a database: it is the rule that decides whether a
// reload is accepted by the foreign keys or rejected halfway through.
func orderTables(tables []string, parentOf map[string]string) []string {
	inSet := make(map[string]bool, len(tables))
	for _, t := range tables {
		inSet[t] = true
	}
	children := map[string][]string{}
	indegree := map[string]int{}
	for _, t := range tables {
		indegree[t] = 0
	}
	for child, parent := range parentOf {
		// A table that references itself cannot be ordered against itself; the
		// rows still load because the constraint is checked at statement end.
		if !inSet[child] || !inSet[parent] || child == parent {
			continue
		}
		children[parent] = append(children[parent], child)
		indegree[child]++
	}

	ready := make([]string, 0, len(tables))
	for _, t := range tables {
		if indegree[t] == 0 {
			ready = append(ready, t)
		}
	}
	order := make([]string, 0, len(tables))
	for len(ready) > 0 {
		sort.Strings(ready)
		table := ready[0]
		ready = ready[1:]
		order = append(order, table)
		for _, child := range children[table] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	// A cycle would leave tables behind; append them rather than losing rows,
	// and let the database report the problem if it really cannot be loaded.
	if len(order) != len(tables) {
		rest := make([]string, 0, len(tables)-len(order))
		placed := make(map[string]bool, len(order))
		for _, t := range order {
			placed[t] = true
		}
		for _, t := range tables {
			if !placed[t] {
				rest = append(rest, t)
			}
		}
		sort.Strings(rest)
		order = append(order, rest...)
	}
	return order
}

// foreignKeys maps each table to the table it references.
func foreignKeys(ctx context.Context, conn *pgx.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT DISTINCT tc.table_name AS child, ccu.table_name AS parent
		FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
			ON ccu.constraint_name = tc.constraint_name
			AND ccu.constraint_schema = tc.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
			AND tc.table_schema = current_schema()
			AND ccu.table_schema = current_schema()`)
	if err != nil {
		return nil, fmt.Errorf("backup: list foreign keys: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, fmt.Errorf("backup: read foreign key: %w", err)
		}
		out[child] = parent
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list foreign keys: %w", err)
	}
	return out, nil
}

// writeFrame writes one table block: name, column list, then the payload
// preceded by its length.
func writeFrame(w io.Writer, table string, cols []string, payload io.Reader, size int64) error {
	var buf [binary.MaxVarintLen64]byte
	put := func(v uint64) error {
		n := binary.PutUvarint(buf[:], v)
		_, err := w.Write(buf[:n])
		return err
	}
	if err := put(uint64(len(table))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, table); err != nil {
		return err
	}
	colsBlob := strings.Join(cols, columnSeparator)
	if err := put(uint64(len(colsBlob))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, colsBlob); err != nil {
		return err
	}
	if err := put(uint64(size)); err != nil {
		return err
	}
	_, err := io.Copy(w, payload)
	return err
}

// readFrame reads the next table block. io.EOF marks the clean end of the
// payload, which is the only way this loop is allowed to stop.
func readFrame(r *bufio.Reader) (table string, cols []string, size int64, err error) {
	nameLen, err := binary.ReadUvarint(r)
	if err != nil {
		return "", nil, 0, err
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(r, name); err != nil {
		return "", nil, 0, fmt.Errorf("%w: truncated table name", ErrArchive)
	}
	colsLen, err := binary.ReadUvarint(r)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%w: truncated column list", ErrArchive)
	}
	colsBlob := make([]byte, colsLen)
	if _, err := io.ReadFull(r, colsBlob); err != nil {
		return "", nil, 0, fmt.Errorf("%w: truncated column list", ErrArchive)
	}
	payloadLen, err := binary.ReadUvarint(r)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%w: truncated payload length", ErrArchive)
	}
	table = string(name)
	if !validIdent(table) {
		return "", nil, 0, fmt.Errorf("%w: bad table name %q", ErrArchive, table)
	}
	if len(colsBlob) == 0 {
		return "", nil, 0, fmt.Errorf("%w: table %q has no columns", ErrArchive, table)
	}
	for _, c := range strings.Split(string(colsBlob), columnSeparator) {
		if !validIdent(c) {
			return "", nil, 0, fmt.Errorf("%w: bad column name %q", ErrArchive, c)
		}
		cols = append(cols, c)
	}
	return table, cols, int64(payloadLen), nil
}

// frameAction is what a restore does with one block of a native payload.
type frameAction int

const (
	// frameLoad is a table that is part of the rewrite.
	frameLoad frameAction = iota
	// frameDrop is a table this build keeps out of the checkpoint. An archive
	// written before the exclusion still carries a block for it.
	frameDrop
	// frameRefuse is a table this build does not have at all, which means the
	// archive came from another schema and nothing in it can be trusted.
	frameRefuse
)

// frameDisposition decides what happens to one table named by an archive. It is
// kept apart from the connection so the rule can be tested without a database:
// getting it wrong either drops rows silently or refuses a loadable archive.
func frameDisposition(table string, known map[string]bool) frameAction {
	if excludedTables[table] {
		return frameDrop
	}
	if !known[table] {
		return frameRefuse
	}
	return frameLoad
}

// ---------------------------------------------------------------------------
// metadata helpers, shared with the pg_dump engine
// ---------------------------------------------------------------------------

// connect opens a connection of its own. A dump can run for minutes, so it must
// never borrow one from the pool the request path uses.
func connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: connect: %v", ErrEngineUnavailable, err)
	}
	// A checkpoint reads every row of every table, and COPY is subject to row
	// level security like any other read. Without the system identity a
	// checkpoint would quietly export only the rows without an owner and report
	// success — the worst possible failure for a backup, so the session declares
	// itself a process owned path up front.
	if _, err := conn.Exec(ctx, `SELECT set_config('app.system', 'on', false)`); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("%w: connect: %v", ErrEngineUnavailable, err)
	}
	return conn, nil
}

// discoverTables lists the application tables in the current schema, excluding
// the backup catalogue. The list is discovered rather than hard coded, so a new
// migration that adds a table is included without a code change here.
func discoverTables(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("backup: list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: read table name: %w", err)
		}
		if excludedTables[name] {
			continue
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list tables: %w", err)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("%w: no application tables found", ErrEngineFailed)
	}
	return tables, nil
}

// tableColumns returns a table's columns in ordinal order.
func tableColumns(ctx context.Context, conn *pgx.Conn, table string) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("backup: list columns of %s: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: read column of %s: %w", table, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list columns of %s: %w", table, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%w: table %q has no columns", ErrEngineFailed, table)
	}
	return cols, nil
}

// appliedMigrations is the set of migrations the database has run. It travels
// with the archive so a restore can refuse data written at a schema the running
// build does not know.
func appliedMigrations(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT filename FROM schema_migrations ORDER BY filename`)
	if err != nil {
		return nil, fmt.Errorf("backup: read schema_migrations: %w", err)
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: read migration: %w", err)
		}
		files = append(files, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: read schema_migrations: %w", err)
	}
	return files, nil
}

// rowCounts counts the rows of each table, for engines whose dump format does
// not report them (pg_dump writes an opaque archive).
func rowCounts(ctx context.Context, conn *pgx.Conn, tables []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		if !validIdent(table) {
			return nil, fmt.Errorf("%w: bad table name %q", ErrArchive, table)
		}
		var n int64
		// The identifier is validated above, so it is safe to interpolate into
		// a statement that cannot take it as a parameter.
		if err := conn.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, quoteIdent(table))).Scan(&n); err != nil {
			return nil, fmt.Errorf("backup: count rows of %s: %w", table, err)
		}
		counts[table] = n
	}
	return counts, nil
}

// refusesTruncate reports whether a pg_trigger.tgtype describes a BEFORE
// TRUNCATE trigger. PostgreSQL packs the timing and the events into one bit
// mask: BEFORE is bit 1, TRUNCATE is bit 5. Only that pair matters here —
// TRUNCATE fires no row trigger, so a BEFORE DELETE or BEFORE UPDATE guard does
// not stop the rewrite and must not become a refusal.
func refusesTruncate(tgtype int16) bool {
	const before, truncate = 1 << 1, 1 << 5
	return tgtype&before != 0 && tgtype&truncate != 0
}

// ensureRewritable refuses the checkpoint when a table it would empty is
// guarded against being emptied, naming the tables so the operator knows what
// stopped them. The check is discovered rather than hard coded: a migration
// that adds such a trigger shows up here without a change to this file.
func ensureRewritable(ctx context.Context, conn *pgx.Conn, tables []string) error {
	guarded, err := guardedTables(ctx, conn, tables)
	if err != nil {
		return err
	}
	if len(guarded) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrTableGuarded, strings.Join(guarded, ", "))
}

// guardedTables lists the given tables that carry a BEFORE TRUNCATE trigger.
func guardedTables(ctx context.Context, conn *pgx.Conn, tables []string) ([]string, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	wanted := make(map[string]bool, len(tables))
	for _, t := range tables {
		wanted[t] = true
	}
	rows, err := conn.Query(ctx, `
		SELECT c.relname, t.tgtype
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT t.tgisinternal AND n.nspname = current_schema()`)
	if err != nil {
		return nil, fmt.Errorf("backup: list triggers: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		var tgtype int16
		if err := rows.Scan(&name, &tgtype); err != nil {
			return nil, fmt.Errorf("backup: read trigger: %w", err)
		}
		if wanted[name] && refusesTruncate(tgtype) {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: read trigger: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// archiveSequences lists every sequence in the schema except the ones owned by
// an excluded table. A pg_dump built from a positive list of tables does not
// pick a SERIAL's sequence up on its own, so they are named too; the ones
// belonging to a table kept out of the checkpoint stay out with it.
func archiveSequences(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	names := make([]string, 0, len(excludedTables))
	for name := range excludedTables {
		names = append(names, name)
	}
	sort.Strings(names)

	rows, err := conn.Query(ctx, `
		SELECT DISTINCT s.relname
		FROM pg_class s
		JOIN pg_namespace n ON n.oid = s.relnamespace
		LEFT JOIN pg_depend d ON d.objid = s.oid AND d.deptype IN ('a', 'i')
		LEFT JOIN pg_class t ON t.oid = d.refobjid
		WHERE s.relkind = 'S' AND n.nspname = current_schema()
		  AND (t.relname IS NULL OR t.relname <> ALL($1))
		ORDER BY s.relname`, names)
	if err != nil {
		return nil, fmt.Errorf("backup: list sequences: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: read sequence name: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list sequences: %w", err)
	}
	return out, nil
}

// truncateAll empties the application tables so the archive can be written back
// without a merge. It is called inside the restore transaction.
func truncateAll(ctx context.Context, conn *pgx.Conn, tables []string) error {
	if len(tables) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(tables))
	for _, t := range tables {
		if !validIdent(t) {
			return fmt.Errorf("%w: bad table name %q", ErrArchive, t)
		}
		quoted = append(quoted, quoteIdent(t))
	}
	_, err := conn.Exec(ctx, fmt.Sprintf(`TRUNCATE TABLE %s CASCADE`, strings.Join(quoted, ", ")))
	if err != nil {
		return fmt.Errorf("%w: truncate: %v", ErrEngineFailed, err)
	}
	return nil
}

// fixSequences advances every sequence past the largest value its column now
// holds. A reload that wrote explicit keys leaves the sequence where it was,
// which would collide on the next insert.
func fixSequences(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT seq.relname, tbl.relname, col.attname
		FROM pg_class seq
		JOIN pg_depend dep ON dep.objid = seq.oid AND dep.classid = 'pg_class'::regclass
		JOIN pg_class tbl ON tbl.oid = dep.refobjid
		JOIN pg_attribute col ON col.attrelid = tbl.oid AND col.attnum = dep.refobjsubid
		WHERE seq.relkind = 'S' AND tbl.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		return fmt.Errorf("backup: list sequences: %w", err)
	}
	defer rows.Close()

	type sequence struct{ name, table, column string }
	var seqs []sequence
	for rows.Next() {
		var s sequence
		if err := rows.Scan(&s.name, &s.table, &s.column); err != nil {
			return fmt.Errorf("backup: read sequence: %w", err)
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("backup: list sequences: %w", err)
	}
	for _, s := range seqs {
		if excludedTables[s.table] {
			continue
		}
		if !validIdent(s.name) || !validIdent(s.table) || !validIdent(s.column) {
			return fmt.Errorf("%w: bad identifier in sequence %q", ErrArchive, s.name)
		}
		stmt := fmt.Sprintf(
			`SELECT setval(%s, GREATEST(COALESCE((SELECT MAX(%s) FROM %s), 0), 1), (SELECT count(*) > 0 FROM %s))`,
			quoteLiteral(s.name), quoteIdent(s.column), quoteIdent(s.table), quoteIdent(s.table))
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("backup: setval %s: %w", s.name, err)
		}
	}
	return nil
}

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validIdent gates every identifier that reaches a SQL string, because table
// and column names arrive from an archive — a file someone could have crafted.
func validIdent(name string) bool {
	return identRe.MatchString(name)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func columnList(cols []string) string {
	quoted := make([]string, 0, len(cols))
	for _, c := range cols {
		quoted = append(quoted, quoteIdent(c))
	}
	return strings.Join(quoted, ", ")
}
