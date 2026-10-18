package backup

import (
	"context"
	"errors"
	"io"
	"time"
)

// Engine names, as they are written into the manifest and shown in the console.
// The name travels with the archive because the two engines write different
// payload formats: an archive can only be read back by the engine that wrote
// it.
const (
	EnginePgDump = "pg_dump"
	EngineNative = "gonative"
)

// Errors reported by the archive layer. They become machine readable codes on
// the wire, so they carry no operator facing prose.
var (
	ErrChecksumMismatch  = errors.New("backup: archive checksum does not match")
	ErrArchive           = errors.New("backup: file is not an agenthub checkpoint")
	ErrEngineUnavailable = errors.New("backup: no dump engine is available")
	ErrEngineFailed      = errors.New("backup: the dump engine failed")
	ErrUnknownTable      = errors.New("backup: archive names a table this database does not have")
)

// Manifest describes one checkpoint. It lives inside the archive, so a file
// copied to a laptop still says what it is, and it is what a restore checks
// before touching a single row.
//
// Checksum and SizeBytes describe the payload (the `data` entry), not the whole
// file: the manifest sits inside the file it would otherwise have to hash. The
// size of the file itself is recorded in the catalogue row, which is what the
// console shows and what a download is measured against.
type Manifest struct {
	Version     int              `json:"version"`
	Engine      string           `json:"engine"`
	Kind        string           `json:"kind"`
	Label       string           `json:"label"`
	CreatedAt   time.Time        `json:"createdAt"`
	Tables      []string         `json:"tables"`
	RowCounts   map[string]int64 `json:"rowCounts"`
	SizeBytes   int64            `json:"sizeBytes"`
	Checksum    string           `json:"checksum"`
	Schema      []string         `json:"schema"`
	AgenthubVer string           `json:"agenthubVersion"`
}

// DumpResult is what an engine reports about the dump it just wrote: which
// tables it covered, how many rows each carried, and which migrations the
// database had applied. The payload itself goes to the writer, so a dump never
// has to fit in memory.
type DumpResult struct {
	Tables    []string
	RowCounts map[string]int64
	Schema    []string
}

// Rows is the total number of rows a dump carried.
func (r DumpResult) Rows() int64 {
	var total int64
	for _, n := range r.RowCounts {
		total += n
	}
	return total
}

// Engine dumps the database into one stream and reads it back. Keeping the two
// sides behind one interface is what makes the whole checkpoint/restore flow
// testable without a database: a stub engine exercises the same code path.
type Engine interface {
	// Name is the engine identifier stored in the manifest.
	Name() string
	// Dump writes the payload for the whole database to w.
	Dump(ctx context.Context, w io.Writer) (DumpResult, error)
	// Restore rewrites the database from a payload written by Dump. It is the
	// engine's job to make that atomic: a failed restore must leave the
	// database exactly as it was.
	Restore(ctx context.Context, r io.Reader, m Manifest) error
}

// Checkpoint is the catalogue row the console lists, plus the audit trail of a
// restore. It mirrors model.BackupCheckpoint without carrying GORM into this
// package.
type Checkpoint struct {
	ID         string
	Kind       string
	Engine     string
	Status     string
	Label      string
	Path       string
	Checksum   string
	Schema     string
	ErrorCode  string
	SizeBytes  int64
	RowCount   int64
	TableCount int
	DurationMS int64
	CreatedAt  time.Time
	FinishedAt *time.Time
	RestoredAt *time.Time
	RestoredBy string
}

// SelectEngine picks the dump engine to use.
//
// prefer "" (or "auto") takes pg_dump when the runtime image has it and falls
// back to the built-in COPY engine otherwise, which is what keeps an alpine
// container without the PostgreSQL client working. Naming "pg_dump" requires it
// and fails loudly when it is missing, so an operator who installed the client
// is never silently served the other engine.
func SelectEngine(dsn, prefer string) (Engine, error) {
	if prefer == EnginePgDump || prefer == "" || prefer == "auto" {
		if engine, err := newPgDumpEngine(dsn); err == nil {
			return engine, nil
		} else if prefer == EnginePgDump {
			return nil, err
		}
	}
	return newCopyEngine(dsn), nil
}
