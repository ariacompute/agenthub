package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

// DefaultMaxBytes is the upload cap when the operator does not set one. It is a
// single-file ceiling, not a quota: a shared file is meant to be a document, a
// screenshot or a recording, and the same number caps what the S3 backend will
// read back.
const DefaultMaxBytes = 32 << 20

// maxNameLength keeps a stored name short enough to render in a table cell.
const maxNameLength = 200

// Options builds a Service. A nil Backend means the feature is off, which is the
// default and must keep working: every endpoint then answers files_disabled.
type Options struct {
	Store    Store
	Backend  Backend
	MaxBytes int64
	Now      func() time.Time
}

// Service is the use case layer: it owns the size cap, the digest, the name, the
// MIME type and the order of the two writes (bytes first, row second).
type Service struct {
	store    Store
	backend  Backend
	maxBytes int64
	now      func() time.Time
}

// NewService validates the options.
func NewService(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("files: a store is required")
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{store: opts.Store, backend: opts.Backend, maxBytes: opts.MaxBytes, now: opts.Now}, nil
}

// Enabled reports whether a backend is configured.
func (s *Service) Enabled() bool { return s != nil && s.backend != nil }

// MaxBytes is the cap the console shows before an upload starts.
func (s *Service) MaxBytes() int64 {
	if s == nil || s.maxBytes <= 0 {
		return DefaultMaxBytes
	}
	return s.maxBytes
}

// BackendName is "local", "s3" or "" when the feature is off.
func (s *Service) BackendName() string {
	if !s.Enabled() {
		return ""
	}
	return s.backend.Name()
}

// Probe reports whether the configured backend is usable.
func (s *Service) Probe(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	return s.backend.Probe(ctx)
}

// Upload stores one body.
//
// The bytes go to a temporary file first, hashing and counting as they arrive, so
// an oversized upload is rejected before it reaches the backend and the object
// key is the content digest rather than anything the caller chose. Only then is
// the row written: a row with no bytes behind it is worse than a rejected upload.
func (s *Service) Upload(ctx context.Context, ownerID, name, contentType string, body io.Reader) (*File, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if strings.TrimSpace(ownerID) == "" {
		// A file with no owner would be invisible to every policy, including the
		// one that would let its uploader read it back.
		return nil, ErrForbidden
	}
	name = SanitizeName(name)
	if name == "" {
		name = "upload.bin"
	}

	spool, err := os.CreateTemp("", "agenthub-upload-*.part")
	if err != nil {
		return nil, ErrStore
	}
	spoolPath := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolPath)
	}()

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(spool, digest), io.LimitReader(body, s.maxBytes+1))
	if err != nil {
		return nil, ErrUnsupported
	}
	if written > s.maxBytes {
		return nil, ErrTooLarge
	}
	if written == 0 {
		return nil, ErrUnsupported
	}

	head := make([]byte, 512)
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, ErrStore
	}
	read, _ := io.ReadFull(spool, head)
	mimeType := resolveMIME(http.DetectContentType(head[:read]), contentType)
	sum := hex.EncodeToString(digest.Sum(nil))
	key := sum[:2] + "/" + sum

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, ErrStore
	}
	if err := s.backend.Put(ctx, key, spool, written, sum, mimeType); err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		_ = s.backend.Delete(ctx, key)
		return nil, ErrStore
	}
	row, err := s.store.Create(ctx, &File{
		ID:        id,
		OwnerID:   ownerID,
		Name:      name,
		MIME:      mimeType,
		Size:      written,
		SHA256:    sum,
		Backend:   s.backend.Name(),
		ObjectKey: key,
		CreatedAt: s.now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		// The bytes are there but the row is not: remove the object rather than
		// leaving an orphan only a filesystem search could find.
		_ = s.backend.Delete(ctx, key)
		return nil, err
	}
	return row, nil
}

// List returns the caller's files, newest first.
func (s *Service) List(ctx context.Context, ownerID string) ([]*File, error) {
	if strings.TrimSpace(ownerID) == "" {
		return nil, ErrForbidden
	}
	return s.store.List(ctx, ownerID)
}

// Get returns one row, scoped to the caller.
func (s *Service) Get(ctx context.Context, id, ownerID string) (*File, error) {
	if strings.TrimSpace(ownerID) == "" {
		return nil, ErrNotFound
	}
	row, err := s.store.Get(ctx, id, ownerID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return row, nil
}

// Download opens the bytes behind a row.
func (s *Service) Download(ctx context.Context, id, ownerID string) (*File, ReadSeekCloser, int64, error) {
	if !s.Enabled() {
		return nil, nil, 0, ErrDisabled
	}
	row, err := s.Get(ctx, id, ownerID)
	if err != nil {
		return nil, nil, 0, err
	}
	if row.Backend != s.backend.Name() {
		// Switching AGENTHUB_FILES_STORE does not migrate what was already
		// stored. Saying so is better than reading a key from the wrong store and
		// reporting a mysterious 502.
		return nil, nil, 0, ErrStore
	}
	body, size, err := s.backend.Open(ctx, row.ObjectKey, row.Size)
	if err != nil {
		return nil, nil, 0, err
	}
	return row, body, size, nil
}

// Delete removes a row and its bytes. The row goes first: if the backend call
// fails afterwards the worst case is an orphaned object, which is recoverable; a
// deleted object behind a live row would be a broken download forever.
func (s *Service) Delete(ctx context.Context, id, ownerID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	row, err := s.Get(ctx, id, ownerID)
	if err != nil {
		return err
	}
	removed, err := s.store.Delete(ctx, id, ownerID)
	if err != nil {
		return err
	}
	if !removed {
		return ErrNotFound
	}
	if err := s.backend.Delete(ctx, row.ObjectKey); err != nil {
		return err
	}
	return nil
}

// SanitizeName reduces a client supplied name to something safe to store and
// display: no directory separators, no control characters, no leading dots and a
// bounded length. The stored name is metadata only — the object key comes from
// the content digest — but a name is rendered in a table and put into a
// Content-Disposition header, so both of those have to be safe.
func SanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '\'' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(name, ".")
	name = strings.TrimSpace(name)
	if len(name) > maxNameLength {
		name = name[:maxNameLength]
	}
	return name
}

// resolveMIME prefers what the bytes say over what the client claimed.
//
// A browser-supplied Content-Type is a suggestion from an untrusted party, and
// getting it wrong is how an upload turns into a stored XSS payload served back
// with the wrong type. The sniffed value wins whenever it is specific; the
// client's value is kept only when sniffing could do no better than
// application/octet-stream (a real PDF and a zip both sniff as octet-stream on
// some builds, so the claim is worth keeping there).
func resolveMIME(sniffed, claimed string) string {
	sniffed = strings.TrimSpace(sniffed)
	claimedType, _, err := mime.ParseMediaType(claimed)
	if err != nil {
		claimedType = ""
	}
	if sniffed == "" || sniffed == "application/octet-stream" {
		if claimedType != "" {
			return claimedType
		}
		return "application/octet-stream"
	}
	return sniffed
}

// newID mints a file id. It is opaque and carries no owner or timestamp: an id
// that can be read should not be the only thing standing between two accounts.
func newID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail on any platform agenthub runs on; if it ever
		// does, refusing is the only safe answer.
		return "", err
	}
	return "file_" + hex.EncodeToString(buf), nil
}
