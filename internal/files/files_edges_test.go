package files

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noEntropy stands in for a broken random source. crypto/rand.Reader is a
// package variable precisely so a caller can do this.
type noEntropy struct{}

func (noEntropy) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// realFS is the shipped set of filesystem calls, captured before any case here
// mutates it.
var realFS = fsOps

// stubFS puts the shipped calls back and restores them again at the end of the
// case, so one failing operation never leaks into the next block or test.
func stubFS(t *testing.T) {
	t.Helper()
	fsOps = realFS
	t.Cleanup(func() { fsOps = realFS })
}

// errReader fails on the first read, standing in for an upload whose connection
// dies halfway.
type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

// Every filesystem step of a Put has its own ErrStore branch. A healthy disk
// never reaches any of them, so each is called with a failing operation.
func TestLocalPutMapsEveryStorageFailure(t *testing.T) {
	boom := errors.New("disk said no")
	cases := map[string]func(){
		"shard directory": func() {
			fsOps.mkdirAll = func(string, os.FileMode) error { return boom }
		},
		"temp file": func() {
			fsOps.createTemp = func(string, string) (*os.File, error) { return nil, boom }
		},
		"sync": func() {
			fsOps.sync = func(*os.File) error { return boom }
		},
		"close": func() {
			// The real close still has to happen, or the temporary file survives
			// the case and Windows refuses to remove the directory.
			fsOps.closeFile = func(f *os.File) error { _ = f.Close(); return boom }
		},
		"chmod": func() {
			fsOps.chmod = func(string, os.FileMode) error { return boom }
		},
		"rename": func() {
			fsOps.rename = func(string, string) error { return boom }
		},
	}
	for name, stub := range cases {
		t.Run(name, func(t *testing.T) {
			stubFS(t)
			backend, err := NewLocalBackend(t.TempDir())
			if err != nil {
				t.Fatalf("NewLocalBackend: %v", err)
			}
			stub()
			if err := backend.Put(context.Background(), testKey(), strings.NewReader("abc"), 3, testSum, "text/plain"); !errors.Is(err, ErrStore) {
				t.Fatalf("err = %v, want ErrStore", err)
			}
		})
	}
}

// The two steps that decide whether the bytes are the ones the caller promised.
func TestLocalPutRejectsABodyItCannotStoreWhole(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	// A body that fails halfway.
	broken := io.MultiReader(strings.NewReader("ab"), &errReader{err: errors.New("upstream vanished")})
	if err := backend.Put(context.Background(), testKey(), broken, 3, testSum, "text/plain"); !errors.Is(err, ErrStore) {
		t.Fatalf("read failure = %v, want ErrStore", err)
	}
	// A body shorter than the size the caller promised.
	if err := backend.Put(context.Background(), testKey(), strings.NewReader("ab"), 3, testSum, "text/plain"); !errors.Is(err, ErrStore) {
		t.Fatalf("short body = %v, want ErrStore", err)
	}
}

// A directory the backend cannot prepare is a startup error, not a lazy one.
func TestNewLocalBackendReportsAnUnusableDirectory(t *testing.T) {
	stubFS(t)
	fsOps.abs = func(string) (string, error) { return "", errors.New("no working directory") }
	if _, err := NewLocalBackend("data"); err == nil {
		t.Fatal("an unresolvable directory must be reported")
	}

	stubFS(t)
	fsOps.mkdirAll = func(string, os.FileMode) error { return errors.New("read-only file system") }
	if _, err := NewLocalBackend("data"); err == nil {
		t.Fatal("a directory that cannot be created must be reported")
	}
}

// Opening a stored object has three failure shapes: the bytes are gone, the open
// itself fails, and the handle cannot be inspected.
func TestLocalOpenReportsEachFailure(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}

	stubFS(t)
	fsOps.open = func(string) (*os.File, error) { return nil, os.ErrNotExist }
	if _, _, err := backend.Open(context.Background(), testKey(), 3); !errors.Is(err, ErrStore) {
		t.Fatalf("missing object = %v, want ErrStore", err)
	}

	fsOps.open = func(string) (*os.File, error) { return nil, os.ErrPermission }
	if _, _, err := backend.Open(context.Background(), testKey(), 3); !errors.Is(err, ErrStore) {
		t.Fatalf("refused open = %v, want ErrStore", err)
	}

	file, err := os.CreateTemp(t.TempDir(), "handle-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	fsOps.open = func(string) (*os.File, error) { return file, nil }
	fsOps.stat = func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	if _, _, err := backend.Open(context.Background(), testKey(), 3); !errors.Is(err, ErrStore) {
		t.Fatalf("stat failure = %v, want ErrStore", err)
	}
}

// Delete and Probe map their storage faults the same way: a key that is already
// gone is fine, anything else is not.
func TestLocalDeleteAndProbeMapStorageFaults(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	stubFS(t)

	fsOps.remove = func(string) error { return os.ErrNotExist }
	if err := backend.Delete(context.Background(), testKey()); err != nil {
		t.Fatalf("a vanished object must not be an error: %v", err)
	}
	fsOps.remove = func(string) error { return os.ErrPermission }
	if err := backend.Delete(context.Background(), testKey()); !errors.Is(err, ErrStore) {
		t.Fatalf("refused delete = %v, want ErrStore", err)
	}

	fsOps.createTemp = func(string, string) (*os.File, error) { return nil, errors.New("no space left") }
	if err := backend.Probe(context.Background()); !errors.Is(err, ErrStore) {
		t.Fatalf("probe = %v, want ErrStore", err)
	}
}

// roundTripTransport answers every request without a socket, which is what lets
// the virtual-host URL shape be exercised without DNS for a bucket subdomain.
type roundTripTransport struct {
	status int
	body   string
	seen   []string
}

func (t *roundTripTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.seen = append(t.seen, req.URL.String())
	return &http.Response{
		StatusCode: t.status,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func s3With(t *testing.T, pathStyle bool, transport http.RoundTripper) *S3Backend {
	t.Helper()
	backend, err := NewS3Backend(S3Options{
		Endpoint:  "https://s3.example.test",
		Bucket:    "agenthub",
		PathStyle: pathStyle,
		AccessKey: "ak",
		SecretKey: "sk",
		Client:    &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	return backend
}

// The backend fills in the two settings an operator may leave empty, and the
// virtual-host shape keeps the bucket in the host rather than the path.
func TestS3BackendDefaultsAndVirtualHostShape(t *testing.T) {
	transport := &roundTripTransport{status: http.StatusOK}
	backend, err := NewS3Backend(S3Options{
		Endpoint:  "https://s3.example.test",
		Bucket:    "agenthub",
		PathStyle: true,
		AccessKey: "ak",
		SecretKey: "sk",
		Client:    &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if backend.opts.Region != "us-east-1" {
		t.Fatalf("region = %q, want the default", backend.opts.Region)
	}
	if backend.opts.Now == nil {
		t.Fatal("a clock must always be set, or signing panics")
	}
	// Path style by default: /bucket/key.
	if err := backend.Put(context.Background(), testKey(), strings.NewReader("abc"), 3, testSum, "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(transport.seen) != 1 || !strings.Contains(transport.seen[0], "/agenthub/"+testSum[:2]+"/") {
		t.Fatalf("requested %v, want the bucket in the path", transport.seen)
	}

	// Virtual-host style: the bucket becomes a subdomain. The transport answers
	// regardless of the host, which is the point: no DNS is involved.
	virtual := s3With(t, false, transport)
	transport.seen = nil
	if err := virtual.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(transport.seen) != 1 || !strings.HasPrefix(transport.seen[0], "https://agenthub.s3.example.test") {
		t.Fatalf("requested %v, want the bucket in the host", transport.seen)
	}
}

// A nil context cannot build any of the four requests; each reports a store
// fault rather than panicking.
func TestS3RequestsWithoutAContext(t *testing.T) {
	backend := s3With(t, true, &roundTripTransport{status: http.StatusOK})
	key := testKey()
	if err := backend.Put(nil, key, strings.NewReader("abc"), 3, testSum, "text/plain"); !errors.Is(err, ErrStore) {
		t.Fatalf("put = %v, want ErrStore", err)
	}
	if _, _, err := backend.Open(nil, key, 3); !errors.Is(err, ErrStore) {
		t.Fatalf("open = %v, want ErrStore", err)
	}
	if err := backend.Delete(nil, key); !errors.Is(err, ErrStore) {
		t.Fatalf("delete = %v, want ErrStore", err)
	}
	if err := backend.Probe(nil); !errors.Is(err, ErrStore) {
		t.Fatalf("probe = %v, want ErrStore", err)
	}
}

// A row whose size is unknown still has to be bounded: the read falls back to
// the one gibibyte ceiling instead of copying until the disk fills.
func TestS3OpenBoundsAnUnknownSize(t *testing.T) {
	backend := s3With(t, true, &roundTripTransport{status: http.StatusOK, body: "abc"})
	body, size, err := backend.Open(context.Background(), testKey(), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = body.Close() }()
	if size != 3 {
		t.Fatalf("size = %d, want the bytes read", size)
	}
}

// A spool that cannot be created or rewound cannot be served: a seeker is the
// whole reason the body was spooled.
func TestS3OpenReportsASpoolFault(t *testing.T) {
	stubFS(t)
	fsOps.createTemp = func(string, string) (*os.File, error) { return nil, errors.New("no space left") }
	if _, _, err := s3With(t, true, &roundTripTransport{status: http.StatusOK, body: "abc"}).
		Open(context.Background(), testKey(), 3); !errors.Is(err, ErrStore) {
		t.Fatalf("spool = %v, want ErrStore", err)
	}

	stubFS(t)
	fsOps.seek = func(*os.File, int64, int) (int64, error) { return 0, errors.New("seek failed") }
	if _, _, err := s3With(t, true, &roundTripTransport{status: http.StatusOK, body: "abc"}).
		Open(context.Background(), testKey(), 3); !errors.Is(err, ErrStore) {
		t.Fatalf("seek = %v, want ErrStore", err)
	}
}

// Closing a spool removes the temporary file, and a removal that fails is
// reported: the caller is the one that has to notice a leftover.
func TestSpooledFileReportsAFailedRemoval(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "spool-*")
	if err != nil {
		t.Fatal(err)
	}
	// A non-empty directory cannot be removed as a file, which is the failure
	// the branch exists for.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := &spooledFile{File: file, path: dir}
	if err := spool.Close(); err == nil {
		t.Fatal("a failed removal must be reported")
	}
}

// The service fills in the two settings an operator may leave empty, and the
// console reads the cap back from a service that was never given one.
func TestNewServiceDefaultsItsClockAndCap(t *testing.T) {
	svc, err := NewService(Options{Store: newMemStore()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if got := svc.MaxBytes(); got != DefaultMaxBytes {
		t.Fatalf("cap = %d, want the default", got)
	}
	if d := time.Since(svc.now()); d < 0 || d > time.Minute {
		t.Fatalf("clock = %v, want now", d)
	}
	// A zeroed service and a nil one both answer the default cap.
	if got := (&Service{}).MaxBytes(); got != DefaultMaxBytes {
		t.Fatalf("zeroed cap = %d, want the default", got)
	}
	var nilService *Service
	if got := nilService.MaxBytes(); got != DefaultMaxBytes {
		t.Fatalf("nil cap = %d, want the default", got)
	}
	if nilService.Enabled() || nilService.BackendName() != "" {
		t.Fatal("a nil service has no backend")
	}
}

// Upload fails closed at every step that follows the spool: no spool, an
// unreadable body, and a spool that cannot be rewound.
func TestUploadMapsEveryInternalFailure(t *testing.T) {
	stubFS(t)
	fsOps.createTemp = func(string, string) (*os.File, error) { return nil, errors.New("no space left") }
	if _, err := serviceFor(t, newMemStore(), newMemBackend(), 1<<20).
		Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hello")); !errors.Is(err, ErrStore) {
		t.Fatalf("spool = %v, want ErrStore", err)
	}

	stubFS(t)
	broken := io.MultiReader(strings.NewReader("hel"), &errReader{err: errors.New("upstream vanished")})
	if _, err := serviceFor(t, newMemStore(), newMemBackend(), 1<<20).
		Upload(context.Background(), "alice", "a.txt", "text/plain", broken); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("body = %v, want unsupported", err)
	}

	// The first rewind feeds the sniffer, the second feeds the backend. Both have
	// to become a store fault, and the second must not leave an object behind.
	realSeek := fsOps.seek
	for _, failing := range []int{1, 2} {
		stubFS(t)
		calls := 0
		fsOps.seek = func(f *os.File, off int64, whence int) (int64, error) {
			calls++
			if calls == failing {
				return 0, errors.New("seek failed")
			}
			return realSeek(f, off, whence)
		}
		backend := newMemBackend()
		if _, err := serviceFor(t, newMemStore(), backend, 1<<20).
			Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hello")); !errors.Is(err, ErrStore) {
			t.Fatalf("seek %d = %v, want ErrStore", failing, err)
		}
		if backend.puts != 0 {
			t.Fatalf("seek %d: puts = %d, want no object written", failing, backend.puts)
		}
	}
}

// An id that cannot be minted leaves nothing behind: the object that already
// reached storage is removed again.
func TestUploadRemovesTheObjectWhenNoIDCanBeMinted(t *testing.T) {
	orig := rand.Reader
	rand.Reader = noEntropy{}
	t.Cleanup(func() { rand.Reader = orig })

	backend := newMemBackend()
	store := newMemStore()
	svc := serviceFor(t, store, backend, 1<<20)
	if _, err := svc.Upload(context.Background(), "alice", "a.txt", "text/plain", strings.NewReader("hello")); !errors.Is(err, ErrStore) {
		t.Fatalf("upload = %v, want ErrStore", err)
	}
	if backend.deletes != 1 {
		t.Fatalf("deletes = %d, want the orphan removed", backend.deletes)
	}
	if len(store.rows) != 0 {
		t.Fatalf("rows = %d, want none", len(store.rows))
	}
}

// Download and Delete both surface a store failure instead of masking it as a
// missing file, and a row that vanishes between the lookup and the delete is
// reported as not found rather than as a silent success.
func TestDownloadAndDeleteSurfaceStoreFailures(t *testing.T) {
	store := newMemStore()
	store.getErr = errors.New("database is down")
	svc := serviceFor(t, store, newMemBackend(), 1<<20)
	if _, _, _, err := svc.Download(context.Background(), "file_1", "alice"); err == nil {
		t.Fatal("a failing lookup must be reported")
	}
	if err := svc.Delete(context.Background(), "file_1", "alice"); err == nil {
		t.Fatal("a failing lookup must be reported")
	}

	store = newMemStore()
	store.deleteErr = errors.New("delete failed")
	svc = serviceFor(t, store, newMemBackend(), 1<<20)
	if _, err := store.Create(context.Background(), &File{ID: "file_1", OwnerID: "alice", Backend: BackendLocal, ObjectKey: testKey()}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.getErr = nil
	if err := svc.Delete(context.Background(), "file_1", "alice"); err == nil {
		t.Fatal("a failing delete must be reported")
	}

	// vanishingStore answers the lookup and then reports nothing removed.
	racing := newMemStore()
	racing.rows["file_2"] = &File{ID: "file_2", OwnerID: "alice", Backend: BackendLocal, ObjectKey: testKey()}
	svc = serviceFor(t, &vanishingStore{memStore: racing}, newMemBackend(), 1<<20)
	if err := svc.Delete(context.Background(), "file_2", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

// vanishingStore reports a row on the lookup and then answers "nothing removed",
// which is the race the second check exists for.
type vanishingStore struct{ *memStore }

func (s *vanishingStore) Delete(context.Context, string, string) (bool, error) { return false, nil }
