package files

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// S3Backend stores the bytes in an S3-compatible object store.
//
// It is written against the standard library on purpose. agenthub's only
// production dependency is GORM, and pulling in an SDK to sign four requests
// would trade a well understood 200 lines for a supply chain and a version
// treadmill. What is implemented is exactly what is used: PUT with a signed
// payload hash, GET, DELETE and a HEAD probe. Multipart uploads are not
// implemented because the service caps a single file well below the point where
// they matter (TODO(real) if that cap ever grows).
//
// Compatibility notes:
//
//   - PathStyle is the default for self-hosted stores. RustFS and MinIO both
//     serve /bucket/key and cannot route bucket.host.example without wildcard
//     DNS, so an operator who sets AGENTHUB_FILES_S3_PATH_STYLE=false must have
//     that DNS in place.
//   - Credentials are read from the environment variables named in the config,
//     never inlined, and never logged: the signature header contains the access
//     key id, so neither it nor the request URL is written to the log.
type S3Backend struct {
	opts S3Options
	base *url.URL
}

// S3Options configures the backend.
type S3Options struct {
	// Endpoint is the store's origin, e.g. https://rustfs.internal:9000.
	Endpoint string
	Bucket   string
	Region   string
	// PathStyle puts the bucket in the path instead of the host.
	PathStyle bool
	AccessKey string
	SecretKey string
	// Client is injectable so tests can point the backend at an httptest server.
	Client *http.Client
	Now    func() time.Time
}

// S3Key is the only shape a key may take, as for the local backend.
var s3Key = localKey

// NewS3Backend validates the options and returns the backend.
func NewS3Backend(opts S3Options) (*S3Backend, error) {
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		return nil, errors.New("files: the s3 backend needs an endpoint")
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("files: the s3 endpoint is not a URL")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("files: the s3 endpoint must be http or https")
	}
	if strings.TrimSpace(opts.Bucket) == "" {
		return nil, errors.New("files: the s3 backend needs a bucket")
	}
	if strings.TrimSpace(opts.AccessKey) == "" || strings.TrimSpace(opts.SecretKey) == "" {
		return nil, errors.New("files: the s3 backend needs credentials (name the environment variables, never inline them)")
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	if opts.Client == nil {
		opts.Client = &http.Client{
			Timeout: 5 * time.Minute,
			// A gateway that redirects is a misconfiguration: following it would
			// hand a signed request to a host the operator did not name.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	base.Path = strings.TrimRight(base.Path, "/")
	return &S3Backend{opts: opts, base: base}, nil
}

// Name is the value recorded on every row this backend writes.
func (b *S3Backend) Name() string { return BackendS3 }

// Bucket exposes the bucket name for the startup log line.
func (b *S3Backend) Bucket() string { return b.opts.Bucket }

func (b *S3Backend) keyURL(key string) (string, error) {
	if !s3Key.MatchString(key) {
		return "", ErrStore
	}
	u := *b.base
	if b.opts.PathStyle {
		u.Path = u.Path + "/" + b.opts.Bucket + "/" + key
	} else {
		u.Host = b.opts.Bucket + "." + u.Host
		u.Path = u.Path + "/" + key
	}
	return u.String(), nil
}

func (b *S3Backend) bucketURL() string {
	u := *b.base
	if b.opts.PathStyle {
		u.Path = u.Path + "/" + b.opts.Bucket
	} else {
		u.Host = b.opts.Bucket + "." + u.Host
	}
	return u.String()
}

// emptyPayload is the SHA-256 of no bytes. GET, DELETE and HEAD send it because
// they carry no body; S3 requires the header to match what was sent.
const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sign adds the SigV4 headers. Only host, the payload hash and the date are
// signed: every other header travels unsigned, which S3 allows and which keeps
// the canonical request stable.
func (b *S3Backend) sign(req *http.Request, payloadHash string) {
	now := b.opts.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	digest := sha256.Sum256([]byte(canonicalRequest))
	scope := dateStamp + "/" + b.opts.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(digest[:])

	mac := hmac.New(sha256.New, signingKey(b.opts.SecretKey, dateStamp, b.opts.Region))
	mac.Write([]byte(stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+b.opts.AccessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+hex.EncodeToString(mac.Sum(nil)))
}

// signingKey derives the request-signing key for one day and region.
func signingKey(secret, dateStamp, region string) []byte {
	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	kDate := mac([]byte("AWS4"+secret), dateStamp)
	kRegion := mac(kDate, region)
	kService := mac(kRegion, "s3")
	return mac(kService, "aws4_request")
}

// Put uploads one object. sha256Hex is the digest the service already computed
// while spooling, so the payload is never read twice.
func (b *S3Backend) Put(ctx context.Context, key string, body io.Reader, size int64, sha256Hex, contentType string) error {
	target, err := b.keyURL(key)
	if err != nil {
		return err
	}
	if size <= 0 || sha256Hex == "" {
		return ErrStore
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
	if err != nil {
		return ErrStore
	}
	req.ContentLength = size
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	b.sign(req, sha256Hex)
	resp, err := b.opts.Client.Do(req)
	if err != nil {
		return ErrStore
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrStore
	}
	return nil
}

// Open reads the object back.
//
// The body is spooled to a temporary file rather than handed over as a bare
// stream, because the download endpoint serves Range requests and http.ServeContent
// needs a seeker. The spool is bounded by the size recorded on the row, deleted
// when the response is done, and never held in memory.
func (b *S3Backend) Open(ctx context.Context, key string, size int64) (ReadSeekCloser, int64, error) {
	target, err := b.keyURL(key)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, ErrStore
	}
	b.sign(req, emptyPayload)
	resp, err := b.opts.Client.Do(req)
	if err != nil {
		return nil, 0, ErrStore
	}
	defer drainAndClose(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, 0, ErrStore
	default:
		return nil, 0, ErrStore
	}
	tmp, err := os.CreateTemp("", "agenthub-s3-*.part")
	if err != nil {
		return nil, 0, ErrStore
	}
	name := tmp.Name()
	limit := size
	if limit <= 0 {
		limit = 1 << 30
	}
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, limit+1))
	if err != nil || written > limit {
		_ = tmp.Close()
		_ = os.Remove(name)
		return nil, 0, ErrStore
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return nil, 0, ErrStore
	}
	return &spooledFile{File: tmp, path: name}, written, nil
}

// Delete removes the object. A key that is already gone is not an error.
func (b *S3Backend) Delete(ctx context.Context, key string) error {
	target, err := b.keyURL(key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return ErrStore
	}
	b.sign(req, emptyPayload)
	resp, err := b.opts.Client.Do(req)
	if err != nil {
		return ErrStore
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode <= 299) {
		return nil
	}
	return ErrStore
}

// Probe asks the store about the bucket. A 404 means the credentials work but
// the bucket is missing, which is worth reporting as unavailable rather than as
// a failed upload later.
func (b *S3Backend) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, b.bucketURL(), nil)
	if err != nil {
		return ErrStore
	}
	b.sign(req, emptyPayload)
	resp, err := b.opts.Client.Do(req)
	if err != nil {
		return ErrStore
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrStore
	}
	return nil
}

// spooledFile removes its temporary file when closed.
type spooledFile struct {
	*os.File
	path string
}

func (s *spooledFile) Close() error {
	err := s.File.Close()
	if rmErr := os.Remove(s.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
		err = rmErr
	}
	return err
}

// drainAndClose reads a little and closes, so the connection is reused instead
// of being torn down after every response.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	_ = body.Close()
}
