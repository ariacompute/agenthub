package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// localKey is the only shape a key may take: a two-hex shard directory and a
// 64-hex digest. Enforcing it here is what makes path traversal impossible
// without sanitising anything — a key is derived from the content hash, never
// from a file name a browser sent.
var localKey = regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f]{64}$`)

// LocalBackend keeps the bytes on the instance's own disk.
//
// The directory is created with 0700 and every object with 0600: a shared file
// is private to the account that uploaded it, and a permissive umask must not
// widen that. Writes go to a temporary file in the destination directory and are
// renamed into place, so a download never sees a half-written body.
type LocalBackend struct{ dir string }

// NewLocalBackend prepares dir and returns the backend.
func NewLocalBackend(dir string) (*LocalBackend, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("files: the local backend needs a directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &LocalBackend{dir: abs}, nil
}

// Name is the value recorded on every row this backend writes.
func (b *LocalBackend) Name() string { return BackendLocal }

// Dir is where the bytes live, for the operator log line at startup.
func (b *LocalBackend) Dir() string { return b.dir }

func (b *LocalBackend) path(key string) (string, error) {
	if !localKey.MatchString(key) {
		return "", ErrStore
	}
	return filepath.Join(b.dir, filepath.FromSlash(key)), nil
}

// Put writes body under key. size is checked by the caller, which already
// spooled the upload; sha256Hex is used for the key and is not recomputed here.
func (b *LocalBackend) Put(ctx context.Context, key string, body io.Reader, size int64, sha256Hex, contentType string) error {
	path, err := b.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ErrStore
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".part-*")
	if err != nil {
		return ErrStore
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	written, err := io.Copy(tmp, body)
	if err != nil {
		cleanup()
		return ErrStore
	}
	if written != size {
		cleanup()
		return ErrStore
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return ErrStore
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return ErrStore
	}
	// CreateTemp already uses 0600; the Chmod keeps that true even if the umask
	// of a future Go version changes it.
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return ErrStore
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return ErrStore
	}
	return nil
}

// Open returns the stored body. The file handle is the seeker http.ServeContent
// needs for Range requests.
func (b *LocalBackend) Open(ctx context.Context, key string, size int64) (ReadSeekCloser, int64, error) {
	path, err := b.path(key)
	if err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The row survived but the bytes did not: that is a store fault, not
			// "no such file", and the operator has to see it.
			return nil, 0, ErrStore
		}
		return nil, 0, ErrStore
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, ErrStore
	}
	return file, info.Size(), nil
}

// Delete removes the object. A key that is already gone is not an error: the
// caller is removing the row next, and a retry must be safe.
func (b *LocalBackend) Delete(ctx context.Context, key string) error {
	path, err := b.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrStore
	}
	return nil
}

// Probe reports whether the directory is usable for writing, without leaving a
// file behind.
func (b *LocalBackend) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.dir, ".probe-*")
	if err != nil {
		return ErrStore
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return nil
}
