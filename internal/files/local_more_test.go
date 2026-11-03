package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The local backend is the only shipped one, so its own guards are exercised
// directly rather than only through the service: the key shape is what makes
// path traversal impossible, and the two accessors are what the operator's
// startup line and every stored row are labelled with.
func TestLocalBackendNeedsADirectory(t *testing.T) {
	if _, err := NewLocalBackend("   "); err == nil {
		t.Fatal("an empty directory must be refused")
	}
}

func TestLocalBackendNamesItselfAndItsDirectory(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	if backend.Name() != BackendLocal {
		t.Fatalf("name = %q, want %q", backend.Name(), BackendLocal)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if backend.Dir() != abs {
		t.Fatalf("dir = %q, want %q", backend.Dir(), abs)
	}
}

func TestLocalBackendRefusesAKeyOutsideTheShardShape(t *testing.T) {
	backend, err := NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	ctx := context.Background()
	bad := []string{
		"../../etc/passwd",
		"ab/" + strings.Repeat("g", 64),
		"ab/" + strings.Repeat("a", 63),
		"",
	}
	for _, key := range bad {
		if err := backend.Put(ctx, key, strings.NewReader("x"), 1, "", "text/plain"); !errors.Is(err, ErrStore) {
			t.Fatalf("put(%q) err = %v, want ErrStore", key, err)
		}
		if _, _, err := backend.Open(ctx, key, 1); !errors.Is(err, ErrStore) {
			t.Fatalf("open(%q) err = %v, want ErrStore", key, err)
		}
		if err := backend.Delete(ctx, key); !errors.Is(err, ErrStore) {
			t.Fatalf("delete(%q) err = %v, want ErrStore", key, err)
		}
	}
}

// A cancelled request must not leave a half-written object behind, and a missing
// object is a store fault rather than "not found": the row survived, so the
// bytes going missing is something the operator has to see.
func TestLocalBackendReportsCancellationAndAMissingObject(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	sha := strings.Repeat("ab", 32)
	key := "ab/" + sha

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.Put(ctx, key, strings.NewReader("x"), 1, sha, "text/plain"); !errors.Is(err, context.Canceled) {
		t.Fatalf("put err = %v, want context.Canceled", err)
	}
	if _, _, err := backend.Open(ctx, key, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("open err = %v, want context.Canceled", err)
	}

	if _, _, err := backend.Open(context.Background(), key, 1); !errors.Is(err, ErrStore) {
		t.Fatalf("open err = %v, want ErrStore for a missing object", err)
	}
}

func TestLocalBackendRoundTripsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewLocalBackend(dir)
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	sha := strings.Repeat("cd", 32)
	key := "cd/" + sha
	ctx := context.Background()
	body := "hello"

	if err := backend.Put(ctx, key, strings.NewReader(body), int64(len(body)), sha, "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	// 0600: a shared file stays private to the uploader. Windows only models the
	// read-only bit, so the mode is asserted where it exists.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key)))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("perm = %o, want 600", perm)
		}
	}

	rc, size, err := backend.Open(ctx, key, int64(len(body)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if string(got) != body || size != int64(len(body)) {
		t.Fatalf("read %q size %d, want %q", got, size, body)
	}

	// A size that does not match what was spooled is refused, not truncated.
	if err := backend.Put(ctx, key, strings.NewReader(body), int64(len(body))+1, sha, "text/plain"); !errors.Is(err, ErrStore) {
		t.Fatalf("put err = %v, want ErrStore on a size mismatch", err)
	}

	if err := backend.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := backend.Open(ctx, key, 1); !errors.Is(err, ErrStore) {
		t.Fatalf("open after delete err = %v, want ErrStore", err)
	}
}
