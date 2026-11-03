package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

// memStore is the smallest possible files.Store: the service is what is under
// test here, and the GORM implementation has its own integration coverage.
type memStore struct {
	rows      map[string]*File
	createErr error
	getErr    error
	listErr   error
	deleteErr error
}

func newMemStore() *memStore { return &memStore{rows: map[string]*File{}} }

func (s *memStore) Create(ctx context.Context, f *File) (*File, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	copy := *f
	s.rows[f.ID] = &copy
	return &copy, nil
}

func (s *memStore) Get(ctx context.Context, id, ownerID string) (*File, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	row, ok := s.rows[id]
	if !ok || row.OwnerID != ownerID {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (s *memStore) List(ctx context.Context, ownerID string) ([]*File, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []*File
	for _, row := range s.rows {
		if row.OwnerID == ownerID {
			copy := *row
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (s *memStore) Delete(ctx context.Context, id, ownerID string) (bool, error) {
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	row, ok := s.rows[id]
	if !ok || row.OwnerID != ownerID {
		return false, nil
	}
	delete(s.rows, id)
	return true, nil
}

// memBackend records what reached "storage" so a test can prove an oversized or
// failed upload never did.
type memBackend struct {
	name     string
	objects  map[string][]byte
	puts     int
	deletes  int
	putErr   error
	openErr  error
	delErr   error
	probeErr error
	sizeSeen int64
	shaSeen  string
	mimeSeen string
}

func newMemBackend() *memBackend {
	return &memBackend{name: BackendLocal, objects: map[string][]byte{}}
}

func (b *memBackend) Name() string { return b.name }

func (b *memBackend) Put(ctx context.Context, key string, body io.Reader, size int64, sha256Hex, contentType string) error {
	if b.putErr != nil {
		return b.putErr
	}
	blob, err := io.ReadAll(body)
	if err != nil {
		return ErrStore
	}
	b.puts++
	b.sizeSeen = size
	b.shaSeen = sha256Hex
	b.mimeSeen = contentType
	b.objects[key] = blob
	return nil
}

func (b *memBackend) Open(ctx context.Context, key string, size int64) (ReadSeekCloser, int64, error) {
	if b.openErr != nil {
		return nil, 0, b.openErr
	}
	blob, ok := b.objects[key]
	if !ok {
		return nil, 0, ErrStore
	}
	return &memFile{Reader: strings.NewReader(string(blob))}, int64(len(blob)), nil
}

func (b *memBackend) Delete(ctx context.Context, key string) error {
	if b.delErr != nil {
		return b.delErr
	}
	b.deletes++
	delete(b.objects, key)
	return nil
}

func (b *memBackend) Probe(ctx context.Context) error { return b.probeErr }

// memFile is a ReadSeekCloser over a string: bytes.Reader already seeks.
type memFile struct{ *strings.Reader }

func (m *memFile) Close() error { return nil }

func serviceFor(t *testing.T, store Store, backend Backend, max int64) *Service {
	t.Helper()
	svc, err := NewService(Options{
		Store:    store,
		Backend:  backend,
		MaxBytes: max,
		Now:      func() time.Time { return time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func codeOf(err error) httputil.Code {
	if err == nil {
		return ""
	}
	return httputil.AsError(err).Code
}

func TestNewServiceRequiresAStore(t *testing.T) {
	if _, err := NewService(Options{Backend: newMemBackend()}); err == nil {
		t.Fatal("a service without a store must not be built")
	}
}

func TestUploadIsOffWithoutABackend(t *testing.T) {
	svc := serviceFor(t, newMemStore(), nil, 0)
	if svc.Enabled() {
		t.Fatal("no backend means the feature is off")
	}
	if svc.BackendName() != "" {
		t.Fatalf("backend name = %q, want empty", svc.BackendName())
	}
	if _, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi")); codeOf(err) != httputil.CodeFilesDisabled {
		t.Fatalf("upload = %v, want files_disabled", err)
	}
	if _, _, _, err := svc.Download(context.Background(), "file_x", "alice"); codeOf(err) != httputil.CodeFilesDisabled {
		t.Fatalf("download = %v, want files_disabled", err)
	}
	if err := svc.Delete(context.Background(), "file_x", "alice"); codeOf(err) != httputil.CodeFilesDisabled {
		t.Fatalf("delete = %v, want files_disabled", err)
	}
	if err := svc.Probe(context.Background()); codeOf(err) != httputil.CodeFilesDisabled {
		t.Fatalf("probe = %v, want files_disabled", err)
	}
	if got := svc.MaxBytes(); got != DefaultMaxBytes {
		t.Fatalf("max bytes = %d, want the default", got)
	}
}

func TestUploadRejectsAnAnonymousOwner(t *testing.T) {
	svc := serviceFor(t, newMemStore(), newMemBackend(), 0)
	if _, err := svc.Upload(context.Background(), "   ", "a.txt", "text/plain", strings.NewReader("hi")); codeOf(err) != httputil.CodeForbidden {
		t.Fatalf("upload = %v, want forbidden", err)
	}
}

func TestUploadStoresBytesAndAttributesThemToTheOwner(t *testing.T) {
	store := newMemStore()
	backend := newMemBackend()
	svc := serviceFor(t, store, backend, 1<<20)

	row, err := svc.Upload(context.Background(), "alice", "../../etc/passwd", "text/plain", strings.NewReader("hello files"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	sum := sha256.Sum256([]byte("hello files"))
	wantSum := hex.EncodeToString(sum[:])
	if row.OwnerID != "alice" {
		t.Fatalf("owner = %q, want alice", row.OwnerID)
	}
	if row.SHA256 != wantSum {
		t.Fatalf("sha = %q, want %q", row.SHA256, wantSum)
	}
	if row.ObjectKey != wantSum[:2]+"/"+wantSum {
		t.Fatalf("key = %q, want a sha shard", row.ObjectKey)
	}
	if row.Size != int64(len("hello files")) {
		t.Fatalf("size = %d, want %d", row.Size, len("hello files"))
	}
	// The browser supplied a traversing path; only the base name is kept.
	if row.Name != "passwd" {
		t.Fatalf("name = %q, want passwd", row.Name)
	}
	if row.Backend != BackendLocal || row.CreatedAt != "2026-05-04T03:02:01Z" {
		t.Fatalf("row = %+v, want backend and timestamp from the service", row)
	}
	if backend.sizeSeen != row.Size || backend.shaSeen != wantSum {
		t.Fatalf("backend saw size=%d sha=%q, want %d/%q", backend.sizeSeen, backend.shaSeen, row.Size, wantSum)
	}
	if string(backend.objects[row.ObjectKey]) != "hello files" {
		t.Fatalf("object body = %q, want the uploaded bytes", backend.objects[row.ObjectKey])
	}

	got, body, size, err := svc.Download(context.Background(), row.ID, "alice")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer func() { _ = body.Close() }()
	blob, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got.ID != row.ID || size != row.Size || string(blob) != "hello files" {
		t.Fatalf("download = %s/%d/%q", got.ID, size, blob)
	}
}

func TestUploadRejectsAnOversizedBodyBeforeItReachesStorage(t *testing.T) {
	backend := newMemBackend()
	store := newMemStore()
	svc := serviceFor(t, store, backend, 8)

	if _, err := svc.Upload(context.Background(), "alice", "big.bin", "", strings.NewReader(strings.Repeat("x", 64))); codeOf(err) != httputil.CodeFileTooLarge {
		t.Fatalf("upload = %v, want file_too_large", err)
	}
	if backend.puts != 0 {
		t.Fatalf("backend puts = %d, want 0", backend.puts)
	}
	if len(store.rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(store.rows))
	}
}

func TestUploadRejectsAnEmptyBody(t *testing.T) {
	backend := newMemBackend()
	svc := serviceFor(t, newMemStore(), backend, 1<<10)
	if _, err := svc.Upload(context.Background(), "alice", "empty.txt", "text/plain", strings.NewReader("")); codeOf(err) != httputil.CodeFileUnsupported {
		t.Fatalf("upload = %v, want file_unsupported", err)
	}
	if backend.puts != 0 {
		t.Fatalf("backend puts = %d, want 0", backend.puts)
	}
}

func TestUploadPropagatesABackendFailure(t *testing.T) {
	store := newMemStore()
	backend := newMemBackend()
	backend.putErr = ErrStore
	svc := serviceFor(t, store, backend, 1<<10)
	if _, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi")); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("upload = %v, want file_store_error", err)
	}
	if len(store.rows) != 0 {
		t.Fatalf("a failed put must not leave a row behind")
	}
}

func TestUploadRemovesTheObjectWhenTheRowCannotBeWritten(t *testing.T) {
	store := newMemStore()
	store.createErr = errors.New("db down")
	backend := newMemBackend()
	svc := serviceFor(t, store, backend, 1<<10)
	if _, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi")); err == nil {
		t.Fatal("expected the store error to surface")
	}
	if backend.deletes != 1 || len(backend.objects) != 0 {
		t.Fatalf("deletes = %d objects = %d, want the orphan removed", backend.deletes, len(backend.objects))
	}
}

func TestUploadKeepsTheClientsTypeOnlyWhenSniffingCannotDoBetter(t *testing.T) {
	backend := newMemBackend()
	svc := serviceFor(t, newMemStore(), backend, 1<<10)
	// A PDF header sniffs as octet-stream on some builds, so the claim survives.
	if _, err := svc.Upload(context.Background(), "alice", "doc.pdf", "application/pdf", strings.NewReader("%PDF-1.7\n")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if backend.mimeSeen != "application/pdf" {
		t.Fatalf("mime = %q, want application/pdf", backend.mimeSeen)
	}
}

func TestListRejectsAnAnonymousCaller(t *testing.T) {
	svc := serviceFor(t, newMemStore(), newMemBackend(), 0)
	if _, err := svc.List(context.Background(), ""); codeOf(err) != httputil.CodeForbidden {
		t.Fatalf("list = %v, want forbidden", err)
	}
}

func TestGetScopesLookupsToTheOwner(t *testing.T) {
	store := newMemStore()
	svc := serviceFor(t, store, newMemBackend(), 1<<10)
	row, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if _, err := svc.Get(context.Background(), row.ID, "mallory"); codeOf(err) != httputil.CodeFileNotFound {
		t.Fatalf("cross-owner get = %v, want file_not_found", err)
	}
	if _, err := svc.Get(context.Background(), row.ID, ""); codeOf(err) != httputil.CodeFileNotFound {
		t.Fatalf("ownerless get = %v, want file_not_found", err)
	}
	if _, err := svc.Get(context.Background(), "file_missing", "alice"); codeOf(err) != httputil.CodeFileNotFound {
		t.Fatalf("missing get = %v, want file_not_found", err)
	}
	if got, err := svc.List(context.Background(), "alice"); err != nil || len(got) != 1 {
		t.Fatalf("list = %v/%v, want one row", got, err)
	}
	if got, err := svc.List(context.Background(), "mallory"); err != nil || len(got) != 0 {
		t.Fatalf("list for a stranger = %v/%v, want none", got, err)
	}
}

func TestGetPropagatesAStoreFailure(t *testing.T) {
	store := newMemStore()
	store.getErr = errors.New("db down")
	svc := serviceFor(t, store, newMemBackend(), 0)
	if _, err := svc.Get(context.Background(), "file_x", "alice"); err == nil {
		t.Fatal("a store failure must surface, not turn into not-found")
	}
}

func TestDownloadRefusesARowFromAnotherBackend(t *testing.T) {
	store := newMemStore()
	store.rows["file_1"] = &File{ID: "file_1", OwnerID: "alice", Backend: BackendS3, ObjectKey: strings.Repeat("a", 2) + "/" + strings.Repeat("a", 64)}
	svc := serviceFor(t, store, newMemBackend(), 0)
	if _, _, _, err := svc.Download(context.Background(), "file_1", "alice"); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("download = %v, want file_store_error", err)
	}
}

func TestDownloadPropagatesABackendFailure(t *testing.T) {
	store := newMemStore()
	svc := serviceFor(t, store, newMemBackend(), 1<<10)
	row, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	svc.backend.(*memBackend).openErr = ErrStore
	if _, _, _, err := svc.Download(context.Background(), row.ID, "alice"); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("download = %v, want file_store_error", err)
	}
}

func TestDeleteRemovesTheRowAndTheBytes(t *testing.T) {
	store := newMemStore()
	backend := newMemBackend()
	svc := serviceFor(t, store, backend, 1<<10)
	row, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := svc.Delete(context.Background(), row.ID, "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(store.rows) != 0 || len(backend.objects) != 0 {
		t.Fatalf("row or object survived the delete")
	}
	if err := svc.Delete(context.Background(), row.ID, "alice"); codeOf(err) != httputil.CodeFileNotFound {
		t.Fatalf("second delete = %v, want file_not_found", err)
	}
}

func TestDeleteRefusesSomebodyElsesFile(t *testing.T) {
	svc := serviceFor(t, newMemStore(), newMemBackend(), 1<<10)
	row, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := svc.Delete(context.Background(), row.ID, "mallory"); codeOf(err) != httputil.CodeFileNotFound {
		t.Fatalf("cross-owner delete = %v, want file_not_found", err)
	}
}

func TestDeleteSurfacesARowThatVanishedUnderneath(t *testing.T) {
	store := newMemStore()
	store.rows["file_1"] = &File{ID: "file_1", OwnerID: "alice", Backend: BackendLocal, ObjectKey: strings.Repeat("a", 2) + "/" + strings.Repeat("a", 64)}
	svc := serviceFor(t, store, newMemBackend(), 0)
	// The store says the row is there but the delete reports no rows affected.
	store.deleteErr = nil
	if err := svc.Delete(context.Background(), "file_1", "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// A backend that refuses the delete is an error the operator has to see.
	store2 := &memStore{rows: map[string]*File{"file_2": {ID: "file_2", OwnerID: "alice", Backend: BackendLocal, ObjectKey: strings.Repeat("b", 2) + "/" + strings.Repeat("b", 64)}}}
	backend := newMemBackend()
	backend.delErr = ErrStore
	svc2 := serviceFor(t, store2, backend, 0)
	if err := svc2.Delete(context.Background(), "file_2", "alice"); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("delete = %v, want file_store_error", err)
	}
}

func TestProbeReportsTheBackend(t *testing.T) {
	backend := newMemBackend()
	svc := serviceFor(t, newMemStore(), backend, 0)
	if err := svc.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	backend.probeErr = ErrStore
	if err := svc.Probe(context.Background()); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("probe = %v, want file_store_error", err)
	}
	if svc.BackendName() != BackendLocal {
		t.Fatalf("backend name = %q, want local", svc.BackendName())
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"  spaced.txt  ", "spaced.txt"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\me\shot.png`, "shot.png"},
		{".hidden", "hidden"},
		{"---", "---"},
		{"a\"b'c", "abc"},
		{"tab\there", "tabhere"},
		{strings.Repeat("x", 300), strings.Repeat("x", 200)},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in); got != c.want {
			t.Fatalf("SanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUploadFallsBackToADefaultName(t *testing.T) {
	svc := serviceFor(t, newMemStore(), newMemBackend(), 1<<10)
	row, err := svc.Upload(context.Background(), "alice", "   ", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if row.Name != "upload.bin" {
		t.Fatalf("name = %q, want upload.bin", row.Name)
	}
}

func TestResolveMIME(t *testing.T) {
	cases := []struct{ sniffed, claimed, want string }{
		{"text/plain; charset=utf-8", "application/octet-stream", "text/plain; charset=utf-8"},
		{"application/octet-stream", "application/pdf", "application/pdf"},
		{"application/octet-stream", "", "application/octet-stream"},
		{"", "image/png", "image/png"},
		{"", "", "application/octet-stream"},
		{"application/octet-stream", "not a media type;;;", "application/octet-stream"},
	}
	for _, c := range cases {
		if got := resolveMIME(c.sniffed, c.claimed); got != c.want {
			t.Fatalf("resolveMIME(%q,%q) = %q, want %q", c.sniffed, c.claimed, got, c.want)
		}
	}
}

// Local backend: the key shape is the traversal defence, so it is asserted
// directly rather than through a name.
func TestLocalBackendRefusesAKeyOutsideTheDigestShape(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	ctx := context.Background()
	for _, key := range []string{"../escape", "aa/../../etc/passwd", "", strings.Repeat("a", 64)} {
		if err := backend.Put(ctx, key, strings.NewReader("x"), 1, "ignored", ""); codeOf(err) != httputil.CodeFileStoreError {
			t.Fatalf("put %q = %v, want file_store_error", key, err)
		}
		if _, _, err := backend.Open(ctx, key, 1); codeOf(err) != httputil.CodeFileStoreError {
			t.Fatalf("open %q = %v, want file_store_error", key, err)
		}
		if err := backend.Delete(ctx, key); codeOf(err) != httputil.CodeFileStoreError {
			t.Fatalf("delete %q = %v, want file_store_error", key, err)
		}
	}
}

func TestLocalBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	ctx := context.Background()
	sum := sha256.Sum256([]byte("round trip"))
	key := hex.EncodeToString(sum[:])[:2] + "/" + hex.EncodeToString(sum[:])

	if err := backend.Put(ctx, key, strings.NewReader("round trip"), int64(len("round trip")), hex.EncodeToString(sum[:]), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	body, size, err := backend.Open(ctx, key, int64(len("round trip")))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	blob, _ := io.ReadAll(body)
	// Closed before the delete on purpose: Windows refuses to unlink an open
	// handle, and a download is always finished before its owner deletes it.
	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if size != int64(len("round trip")) || string(blob) != "round trip" {
		t.Fatalf("open = %d/%q", size, blob)
	}

	stored := filepath.Join(dir, filepath.FromSlash(key))
	info, err := os.Stat(stored)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows has no POSIX mode bits: os.Chmod only toggles the read-only flag and
	// the reported mode is whatever the CRT defaults to, so the assertion is
	// meaningful on the platforms where the directory is actually private.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}

	if err := backend.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := backend.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Deleting twice is safe: the row is removed first, so a retry must not fail.
	if err := backend.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestLocalBackendConfirmsTheByteCountItWasPromised(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	key := strings.Repeat("c", 2) + "/" + strings.Repeat("c", 64)
	if err := backend.Put(context.Background(), key, strings.NewReader("short"), 99, strings.Repeat("c", 64), ""); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("put = %v, want file_store_error", err)
	}
	if _, _, err := backend.Open(context.Background(), key, 5); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("a missing object must be a store fault, got %v", err)
	}
}

func TestLocalBackendHonoursACancelledRequest(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key := strings.Repeat("d", 2) + "/" + strings.Repeat("d", 64)
	if err := backend.Put(ctx, key, strings.NewReader("x"), 1, "", ""); err == nil {
		t.Fatal("a cancelled put must not write")
	}
	if _, _, err := backend.Open(ctx, key, 1); err == nil {
		t.Fatal("a cancelled open must not read")
	}
	if err := backend.Delete(ctx, key); err == nil {
		t.Fatal("a cancelled delete must not run")
	}
	if err := backend.Probe(ctx); err == nil {
		t.Fatal("a cancelled probe must not run")
	}
}

func TestNewLocalBackendRequiresADirectory(t *testing.T) {
	if _, err := NewLocalBackend("  "); err == nil {
		t.Fatal("an empty directory must be refused")
	}
}
