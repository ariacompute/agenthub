package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Archive layout: one gzip'd tar with exactly two entries.
//
//	data           the engine payload, written first so its checksum is known
//	               before the manifest is written
//	manifest.json  everything a restore needs to decide whether it may proceed
//
// The payload comes first on purpose: the checksum is computed while the
// payload is copied, and the manifest that records it has to be written after.
const (
	archiveVersion = 1
	dataEntry      = "data"
	manifestEntry  = "manifest.json"
)

// Pack writes the checkpoint archive and returns the manifest as it was
// recorded, checksum and size included.
//
// size is the exact payload length, which the tar entry header needs up front;
// it comes from the staged file the dump was written to.
func Pack(dst string, payload io.Reader, size int64, m Manifest) (Manifest, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return m, err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return m, err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(dst)
			return
		}
		_ = f.Close()
	}()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	write := func(name string, body []byte, size int64, src io.Reader) error {
		if err := tw.WriteHeader(&tar.Header{
			Name:    name,
			Mode:    0o600,
			Size:    size,
			ModTime: m.CreatedAt.UTC(),
			Format:  tar.FormatPAX,
		}); err != nil {
			return err
		}
		if src != nil {
			_, err := io.Copy(tw, src)
			return err
		}
		_, err := tw.Write(body)
		return err
	}

	digest := sha256.New()
	if err = write(dataEntry, nil, size, io.TeeReader(payload, digest)); err != nil {
		return m, err
	}
	m.Version = archiveVersion
	m.Checksum = hex.EncodeToString(digest.Sum(nil))
	m.SizeBytes = size

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	if err = write(manifestEntry, body, int64(len(body)), nil); err != nil {
		return m, err
	}
	if err = tw.Close(); err != nil {
		return m, err
	}
	if err = gw.Close(); err != nil {
		return m, err
	}
	if err = f.Sync(); err != nil {
		return m, err
	}
	// The deferred close removes the file only when err is set, so clear it
	// once everything is on disk.
	err = nil
	return m, nil
}

// WriteCheckpoint runs one dump into a packed archive and returns the path plus
// the manifest that was recorded.
//
// The payload is staged on disk first because a tar entry header needs the
// length up front, and a database is routinely larger than the memory anyone
// would want to spend on it. The manifest is filled in from what the engine
// reports, so the caller only supplies what it already knows (kind, label).
func WriteCheckpoint(ctx context.Context, engine Engine, dir string, m Manifest) (string, Manifest, error) {
	stage, err := newStageDump(dir, "payload")
	if err != nil {
		return "", m, err
	}
	defer func() { _ = stage.Close() }()

	result, err := engine.Dump(ctx, stage)
	if err != nil {
		return "", m, err
	}
	payload, size, err := stage.Reader()
	if err != nil {
		return "", m, err
	}

	m.Engine = engine.Name()
	m.Tables = result.Tables
	m.RowCounts = result.RowCounts
	m.Schema = result.Schema
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	path := filepath.Join(dir, checkpointFileName(m.CreatedAt, m.Kind, m.Label))
	packed, err := Pack(path, payload, size, m)
	if err != nil {
		return "", m, err
	}
	return path, packed, nil
}

// ReadManifest reads the manifest out of an archive without touching the
// payload, which is what the console needs to describe a checkpoint before it
// downloads anything.
func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	err := eachEntry(path, func(name string, r io.Reader) (bool, error) {
		if name != manifestEntry {
			return false, nil
		}
		if err := json.NewDecoder(r).Decode(&m); err != nil {
			return true, fmt.Errorf("%w: manifest: %v", ErrArchive, err)
		}
		if m.Version != archiveVersion {
			return true, fmt.Errorf("%w: version %d, want %d", ErrArchive, m.Version, archiveVersion)
		}
		return true, nil
	})
	if err != nil {
		return Manifest{}, err
	}
	if m.Version == 0 {
		return Manifest{}, ErrArchive
	}
	return m, nil
}

// Extract streams the payload to dst and verifies it against the manifest. A
// restore never sees an archive whose bytes do not match what the manifest
// promises, so a corrupted volume is reported instead of half applied.
func Extract(path string, dst io.Writer) (Manifest, error) {
	m, err := ReadManifest(path)
	if err != nil {
		return m, err
	}
	digest := sha256.New()
	found := false
	err = eachEntry(path, func(name string, r io.Reader) (bool, error) {
		if name != dataEntry {
			return false, nil
		}
		found = true
		if _, err := io.Copy(io.MultiWriter(dst, digest), r); err != nil {
			return true, err
		}
		return true, nil
	})
	if err != nil {
		return m, err
	}
	if !found {
		return m, ErrArchive
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != m.Checksum {
		return m, fmt.Errorf("%w: archive says %s, payload is %s", ErrChecksumMismatch, shortHex(m.Checksum), shortHex(got))
	}
	return m, nil
}

// FileChecksum hashes the whole archive file. It is stored in the catalogue row
// so a restore can prove the file on disk is the one that was written, which a
// payload checksum alone cannot do.
func FileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// eachEntry walks the archive entries until the visitor says it is done.
func eachEntry(path string, visit func(name string, r io.Reader) (bool, error)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArchive, err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrArchive, err)
		}
		// A stray entry would mean someone repacked the file; refuse it rather
		// than guessing which entry was meant.
		if hdr.Name != dataEntry && hdr.Name != manifestEntry {
			return fmt.Errorf("%w: unexpected entry %q", ErrArchive, hdr.Name)
		}
		done, err := visit(hdr.Name, io.LimitReader(tr, hdr.Size))
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// shortHex keeps a checksum readable in a log line and an error code.
func shortHex(sum string) string {
	sum = strings.TrimSpace(sum)
	if len(sum) <= 12 {
		return sum
	}
	return sum[:12]
}

// stageDump is the file a dump is written to before it is packed: the tar entry
// header needs the payload length up front, and a database is routinely bigger
// than the memory an operator would want to spend on it.
type stageDump struct {
	file *os.File
	dir  string
}

func newStageDump(dir string, prefix string) (*stageDump, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, prefix+"-*.data")
	if err != nil {
		return nil, err
	}
	return &stageDump{file: f, dir: dir}, nil
}

func (s *stageDump) Write(p []byte) (int, error) { return s.file.Write(p) }

// Path is the staged payload, for engines that need to seek over it.
func (s *stageDump) Path() string { return s.file.Name() }

// Size is the exact payload length.
func (s *stageDump) Size() (int64, error) {
	info, err := s.file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// Reader rewinds the staged file ready for packing.
func (s *stageDump) Reader() (io.Reader, int64, error) {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	size, err := s.Size()
	if err != nil {
		return nil, 0, err
	}
	return s.file, size, nil
}

func (s *stageDump) Close() error {
	err := s.file.Close()
	if rmErr := os.Remove(s.file.Name()); err == nil {
		err = rmErr
	}
	return err
}

// checkpointFileName builds the archive name: a sortable timestamp, the kind and
// a hint of why it was taken, so a directory listing already tells an operator
// what is in there without opening anything.
//
// The hint is slugified: a label is free text an operator typed into a form, and
// a filename has no business carrying its spaces, slashes or non-ASCII.
func checkpointFileName(at time.Time, kind, label string) string {
	stamp := at.UTC().Format("20060102T150405Z")
	hint := slug(label)
	if hint == "" {
		return fmt.Sprintf("checkpoint-%s-%s.tar.gz", stamp, kind)
	}
	return fmt.Sprintf("checkpoint-%s-%s-%s.tar.gz", stamp, kind, hint)
}

// slug reduces free text to something safe in a filename and still readable in
// a directory listing.
func slug(in string) string {
	var b strings.Builder
	last := '-'
	for _, r := range strings.ToLower(strings.TrimSpace(in)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			last = r
		default:
			// Anything else — punctuation, whitespace, non-Latin scripts —
			// becomes one separator, and separators never double up.
			if last != '-' {
				b.WriteRune('-')
				last = '-'
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}
