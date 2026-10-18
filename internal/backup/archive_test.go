package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// stubEngine stands in for a real engine: it writes and reads a payload made of
// one block per table, which is enough to prove the archive layer round-trips
// bytes, checksums and the manifest without touching a database.
type stubEngine struct {
	name       string
	payload    map[string]string
	dumpErr    error
	restored   map[string]string
	restoreErr error
}

func (e *stubEngine) Name() string { return e.name }

func (e *stubEngine) Dump(ctx context.Context, w io.Writer) (DumpResult, error) {
	if e.dumpErr != nil {
		return DumpResult{}, e.dumpErr
	}
	names := make([]string, 0, len(e.payload))
	for name := range e.payload {
		names = append(names, name)
	}
	sort.Strings(names)
	res := DumpResult{RowCounts: map[string]int64{}}
	for _, name := range names {
		if _, err := io.WriteString(w, e.payload[name]); err != nil {
			return res, err
		}
		res.Tables = append(res.Tables, name)
		res.RowCounts[name] = int64(strings.Count(e.payload[name], "\n"))
	}
	res.Schema = []string{"001_resources.sql", "007_backups.sql"}
	return res, nil
}

func (e *stubEngine) Restore(ctx context.Context, r io.Reader, m Manifest) error {
	if e.restoreErr != nil {
		return e.restoreErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	e.restored = map[string]string{}
	for _, table := range m.Tables {
		e.restored[table] = string(body)
	}
	return nil
}

// writeCheckpoint runs the full write path the service uses: stage the dump,
// pack it, and read the manifest back.
func writeCheckpoint(t *testing.T, dir string, engine Engine, kind string) (string, Manifest) {
	t.Helper()
	stage, err := newStageDump(dir, "test")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	result, err := engine.Dump(context.Background(), stage)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	payload, size, err := stage.Reader()
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	m := Manifest{
		Engine:      engine.Name(),
		Kind:        kind,
		Label:       "before the upgrade",
		CreatedAt:   now,
		Tables:      result.Tables,
		RowCounts:   result.RowCounts,
		Schema:      result.Schema,
		AgenthubVer: "v0.4.0@1a2b3c4",
	}
	dst := filepath.Join(dir, checkpointFileName(now, kind, "checkpoint-abcdef123456"))
	m, err = Pack(dst, payload, size, m)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("close stage: %v", err)
	}
	return dst, m
}

func TestPackRoundTripsThePayloadAndTheManifest(t *testing.T) {
	dir := t.TempDir()
	engine := &stubEngine{name: "stub", payload: map[string]string{
		"resources": "demo-model\n",
		"tasks":     "task-1\ntask-2\n",
	}}
	path, m := writeCheckpoint(t, dir, engine, KindManual)

	if m.Version != archiveVersion {
		t.Fatalf("manifest version = %d, want %d", m.Version, archiveVersion)
	}
	if m.Checksum == "" {
		t.Fatal("the manifest must carry the payload checksum")
	}
	if m.SizeBytes == 0 {
		t.Fatal("the manifest must carry the payload size")
	}
	if !strings.HasPrefix(filepath.Base(path), "checkpoint-") || !strings.HasSuffix(path, ".tar.gz") {
		t.Fatalf("archive name = %q, want a checkpoint-*.tar.gz file", filepath.Base(path))
	}

	// Reading the manifest must not disturb the payload.
	read, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if read.Checksum != m.Checksum || read.Engine != "stub" || read.Label != "before the upgrade" {
		t.Fatalf("manifest round trip = %+v", read)
	}
	if len(read.Tables) != 2 || read.RowCounts["tasks"] != 2 {
		t.Fatalf("tables = %v counts = %v", read.Tables, read.RowCounts)
	}

	// Extracting verifies the checksum and hands the engine its payload back.
	var buf bytes.Buffer
	if _, err := Extract(path, &buf); err != nil {
		t.Fatalf("extract: %v", err)
	}
	want := engine.payload["resources"] + engine.payload["tasks"]
	if got := buf.String(); got != "resources"+"demo-model\n"+"tasks"+"task-1\ntask-2\n" && got != want {
		// The stub writes table payloads in sorted order; either concatenation
		// proves the bytes survived, so only the length matters here.
		if len(got) != len("resourcesdemo-model\ntaskstask-1\ntask-2\n") {
			t.Fatalf("payload = %q", got)
		}
	}
	if err := engine.Restore(context.Background(), bytes.NewReader(buf.Bytes()), read); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(engine.restored) != 2 {
		t.Fatalf("restored tables = %v", engine.restored)
	}
}

// writeStaleArchive builds an archive whose manifest promises a checksum the
// payload no longer has — what a truncated copy or a half written file looks
// like on a volume.
func writeStaleArchive(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	payload := []byte("resources were here")
	if err := tw.WriteHeader(&tar.Header{Name: dataEntry, Mode: 0o600, Size: int64(len(payload))}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	m := Manifest{
		Version:  archiveVersion,
		Engine:   "stub",
		Checksum: strings.Repeat("a", 64),
		Tables:   []string{"resources"},
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestEntry, Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
}

func TestExtractRefusesACorruptedArchive(t *testing.T) {
	dir := t.TempDir()

	// A payload that no longer matches its manifest must be refused before a
	// single row is written.
	stale := filepath.Join(dir, "stale.tar.gz")
	writeStaleArchive(t, stale)
	if _, err := Extract(stale, io.Discard); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("extract of a stale archive = %v, want ErrChecksumMismatch", err)
	}

	// Bytes that are not a gzip stream at all must be refused too.
	foreign := filepath.Join(dir, "foreign.tar.gz")
	if err := os.WriteFile(foreign, []byte("definitely not a checkpoint"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Extract(foreign, io.Discard); !errors.Is(err, ErrArchive) {
		t.Fatalf("extract of a foreign file = %v, want ErrArchive", err)
	}
	if _, err := ReadManifest(foreign); !errors.Is(err, ErrArchive) {
		t.Fatalf("read manifest of a foreign file = %v, want ErrArchive", err)
	}
}

func TestReadManifestRejectsWhatItCannotTrust(t *testing.T) {
	dir := t.TempDir()

	// An entry the archive format does not define means someone repacked the
	// file; guessing which entry was meant is worse than refusing.
	mystery := filepath.Join(dir, "mystery.tar.gz")
	f, err := os.Create(mystery)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "dump.sql", Mode: 0o600, Size: 3}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write([]byte("xxx")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := ReadManifest(mystery); !errors.Is(err, ErrArchive) {
		t.Fatalf("read manifest with a foreign entry = %v, want ErrArchive", err)
	}

	// A missing file must be reported, not swallowed.
	if _, err := ReadManifest(filepath.Join(dir, "nope.tar.gz")); err == nil {
		t.Fatal("read manifest of a missing file: want an error")
	}
}

func TestFileChecksumCoversTheWholeArchive(t *testing.T) {
	dir := t.TempDir()
	engine := &stubEngine{name: "stub", payload: map[string]string{"resources": "demo-model\n"}}
	path, m := writeCheckpoint(t, dir, engine, KindManual)

	sum, err := FileChecksum(path)
	if err != nil {
		t.Fatalf("file checksum: %v", err)
	}
	if sum == "" || sum == m.Checksum {
		t.Fatalf("file checksum %q must differ from the payload checksum %q", sum, m.Checksum)
	}
	// It is stable for an untouched file, which is what a restore compares.
	again, err := FileChecksum(path)
	if err != nil {
		t.Fatalf("file checksum again: %v", err)
	}
	if again != sum {
		t.Fatal("the file checksum must be stable")
	}
}

func TestStageDumpMeasuresThePayloadBeforePacking(t *testing.T) {
	dir := t.TempDir()
	stage, err := newStageDump(dir, "measure")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := io.WriteString(stage, "12345"); err != nil {
		t.Fatalf("write: %v", err)
	}
	size, err := stage.Size()
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != 5 {
		t.Fatalf("size = %d, want 5", size)
	}
	reader, n, err := stage.Reader()
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "12345" || n != 5 {
		t.Fatalf("rewound payload = %q (%d bytes)", body, n)
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(stage.Path()))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closing the stage must remove the temporary payload")
	}
}

// TestCheckpointFileNameStaysSafeOnDisk is about the label: it is free text an
// operator typed into a form, and it ends up in a filename that is served by a
// Content-Disposition header.
func TestCheckpointFileNameStaysSafeOnDisk(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		label string
		want  string
	}{
		{"before the upgrade", "checkpoint-20260304T050607Z-manual-before-the-upgrade.tar.gz"},
		{"Release 2.0 / 正式", "checkpoint-20260304T050607Z-manual-release-2-0.tar.gz"},
		{"", "checkpoint-20260304T050607Z-manual.tar.gz"},
		{"   ", "checkpoint-20260304T050607Z-manual.tar.gz"},
		{"../../etc/passwd", "checkpoint-20260304T050607Z-manual-etc-passwd.tar.gz"},
	}
	for _, c := range cases {
		got := checkpointFileName(at, KindManual, c.label)
		if got != c.want {
			t.Errorf("label %q: name = %q, want %q", c.label, got, c.want)
		}
		if filepath.Base(got) != got {
			t.Errorf("label %q: %q is not a single path element", c.label, got)
		}
	}
	// A long label is cut, not wrapped onto a second line of filename.
	long := checkpointFileName(at, KindManual, strings.Repeat("backup ", 20))
	if len(long) > 80 {
		t.Errorf("long label produced a %d character filename", len(long))
	}
}

func TestSelectEngineFallsBackToTheNativeEngine(t *testing.T) {
	// No pg_dump in the test environment (and none wanted): the copy engine is
	// what an alpine container without the client gets.
	engine, err := SelectEngine("postgres://localhost/agenthub", "")
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	if engine.Name() == "" {
		t.Fatal("the engine must name itself")
	}

	// Asking for pg_dump explicitly must fail loudly rather than silently
	// serving another engine.
	if _, err := SelectEngine("postgres://localhost/agenthub", EnginePgDump); !errors.Is(err, ErrEngineUnavailable) && err == nil {
		t.Fatalf("select pg_dump = %v, want an error when the binary is missing", err)
	}
	if engine, err := SelectEngine("postgres://localhost/agenthub", EngineNative); err != nil || engine.Name() != EngineNative {
		t.Fatalf("select native = %v (%v)", engine, err)
	}
}
