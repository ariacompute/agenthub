// Package files stores the bytes a user shares: upload, list, download and
// delete, with the bytes themselves held either on the instance's disk or in an
// S3-compatible object store.
//
// The split is deliberate and follows the same shape as backups: the port
// (Backend, Store) is defined here, the GORM implementation of Store lives in
// internal/store, and the two backends (local disk, S3) live beside this file.
// Nothing in this package knows about HTTP.
//
// Two properties matter more than the plumbing:
//
//   - A stored row always belongs to exactly one owner, and the owner is taken
//     from the session, never from the request body. shared_files is under a
//     row level security policy, so the database agrees.
//   - The size cap and the digest are computed while the bytes are spooled to a
//     temporary file, before anything is written to the backend. A too-large
//     upload therefore never reaches storage, and the key is derived from the
//     content hash rather than from the file name a browser sent.
package files

import (
	"context"
	"io"

	"agenthub/internal/httputil"
)

// File is one stored blob's metadata. ObjectKey is the address inside the
// backend and is not part of the API: a client should never need to know
// whether the bytes are on a disk or in a bucket.
type File struct {
	ID      string `json:"id"`
	OwnerID string `json:"ownerId,omitempty"`
	Name    string `json:"name"`
	MIME    string `json:"mime"`
	// Size is the byte length of the stored body.
	Size int64 `json:"size"`
	// SHA256 is the hex digest of the body, computed while spooling. It is also
	// the object key, so two identical uploads share one object.
	SHA256 string `json:"sha256"`
	// Backend records where this row's bytes live ("local" or "s3"). It is
	// per-row rather than per-instance so switching backends does not orphan the
	// files uploaded before the switch.
	Backend   string `json:"backend"`
	ObjectKey string `json:"-"`
	CreatedAt string `json:"createdAt"`
}

// Store persists file metadata. Implemented in internal/store.
//
// Ownership is part of the key on every read: a lookup without the owner cannot
// answer "does this exist", so somebody else's file is reported as absent.
type Store interface {
	Create(ctx context.Context, f *File) (*File, error)
	Get(ctx context.Context, id, ownerID string) (*File, error)
	List(ctx context.Context, ownerID string) ([]*File, error)
	// Delete removes the row and reports whether it was there.
	Delete(ctx context.Context, id, ownerID string) (bool, error)
}

// ReadSeekCloser is what a download hands to http.ServeContent: a body plus the
// seeking Range requests need.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Backend is where the bytes live.
//
// Put receives the digest the service already computed, so an S3 implementation
// can sign the payload hash without reading the body twice.
type Backend interface {
	// Name is the value recorded in File.Backend.
	Name() string
	Put(ctx context.Context, key string, body io.Reader, size int64, sha256Hex, contentType string) error
	// Open returns the body and its length. The caller closes it. size is the
	// length recorded on the row: an object store uses it to bound the read, and
	// a mismatch means the store and the row disagree.
	Open(ctx context.Context, key string, size int64) (ReadSeekCloser, int64, error)
	Delete(ctx context.Context, key string) error
	// Probe reports whether the backend is usable right now, without writing
	// anything. It answers the console's "is this wired up" question.
	Probe(ctx context.Context) error
}

// Backend names.
const (
	BackendLocal = "local"
	BackendS3    = "s3"
)

// Sentinel errors, carried as machine readable codes so the console can
// localise them instead of printing prose.
var (
	// ErrDisabled means no storage backend is configured. It is a supported
	// configuration, not a fault: an instance without a files directory behaves
	// exactly as it did before the feature existed.
	ErrDisabled = &httputil.Error{Code: httputil.CodeFilesDisabled}
	// ErrNotFound covers both "no such file" and "not yours". The two are the
	// same answer on purpose.
	ErrNotFound = &httputil.Error{Code: httputil.CodeFileNotFound}
	ErrTooLarge = &httputil.Error{Code: httputil.CodeFileTooLarge}
	// ErrUnsupported means the request was not a single-file multipart upload.
	ErrUnsupported = &httputil.Error{Code: httputil.CodeFileUnsupported}
	// ErrStore means the row exists but the backend could not serve the bytes.
	ErrStore = &httputil.Error{Code: httputil.CodeFileStoreError}
	// ErrForbidden means there is no owner to attribute the bytes to. Uploading
	// anonymously would create a row nobody can read, so it is refused.
	ErrForbidden = &httputil.Error{Code: httputil.CodeForbidden}
)
