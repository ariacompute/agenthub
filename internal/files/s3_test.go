package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

var testSum = func() string {
	sum := sha256.Sum256([]byte("payload"))
	return hex.EncodeToString(sum[:])
}()

func testKey() string { return testSum[:2] + "/" + testSum }

func s3Options(endpoint string) S3Options {
	return S3Options{
		Endpoint:  endpoint,
		Bucket:    "shared",
		Region:    "eu-west-1",
		PathStyle: true,
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "secret-example",
		Now:       func() time.Time { return time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC) },
	}
}

func TestNewS3BackendValidatesItsOptions(t *testing.T) {
	cases := []struct {
		name string
		opts S3Options
	}{
		{"no endpoint", S3Options{Bucket: "b", AccessKey: "a", SecretKey: "s"}},
		{"bad scheme", S3Options{Endpoint: "ftp://store", Bucket: "b", AccessKey: "a", SecretKey: "s"}},
		{"not a url", S3Options{Endpoint: "://store", Bucket: "b", AccessKey: "a", SecretKey: "s"}},
		{"no bucket", S3Options{Endpoint: "http://store", AccessKey: "a", SecretKey: "s"}},
		{"no access key", S3Options{Endpoint: "http://store", Bucket: "b", SecretKey: "s"}},
		{"no secret", S3Options{Endpoint: "http://store", Bucket: "b", AccessKey: "a"}},
	}
	for _, c := range cases {
		if _, err := NewS3Backend(c.opts); err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
	}

	backend, err := NewS3Backend(s3Options("http://store"))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if backend.Name() != BackendS3 || backend.Bucket() != "shared" {
		t.Fatalf("backend = %s/%s", backend.Name(), backend.Bucket())
	}
}

func TestS3PutSignsThePayloadAndUsesPathStyle(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if err := backend.Put(context.Background(), testKey(), strings.NewReader("payload"), int64(len("payload")), testSum, "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got == nil {
		t.Fatal("no request reached the store")
	}
	if got.Method != http.MethodPut {
		t.Fatalf("method = %s, want PUT", got.Method)
	}
	if got.URL.Path != "/shared/"+testKey() {
		t.Fatalf("path = %q, want path style /shared/<key>", got.URL.Path)
	}
	if got.Header.Get("x-amz-content-sha256") != testSum {
		t.Fatalf("payload hash = %q, want the digest the service computed", got.Header.Get("x-amz-content-sha256"))
	}
	if got.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("content type = %q, want text/plain", got.Header.Get("Content-Type"))
	}
	if got.Header.Get("x-amz-date") != "20260504T030201Z" {
		t.Fatalf("amz date = %q", got.Header.Get("x-amz-date"))
	}
	auth := got.Header.Get("Authorization")
	wantPrefix := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260504/eu-west-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="
	if !strings.HasPrefix(auth, wantPrefix) {
		t.Fatalf("authorization = %q, want prefix %q", auth, wantPrefix)
	}
	if strings.Contains(auth, "secret-example") {
		t.Fatal("the secret must never appear in a header")
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q", body)
	}
}

func TestS3VirtualHostStylePutsTheBucketInTheHost(t *testing.T) {
	opts := s3Options("http://store.example:9000")
	opts.PathStyle = false
	backend, err := NewS3Backend(opts)
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	target, err := backend.keyURL(testKey())
	if err != nil {
		t.Fatalf("keyURL: %v", err)
	}
	if !strings.HasPrefix(target, "http://shared.store.example:9000/"+testKey()) {
		t.Fatalf("target = %q, want a virtual host URL", target)
	}
}

func TestS3RefusesAKeyThatIsNotADigest(t *testing.T) {
	backend, err := NewS3Backend(s3Options("http://store"))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	ctx := context.Background()
	for _, key := range []string{"../escape", "", strings.Repeat("a", 64)} {
		if err := backend.Put(ctx, key, strings.NewReader("x"), 1, testSum, ""); codeOf(err) != httputil.CodeFileStoreError {
			t.Fatalf("put %q = %v", key, err)
		}
		if _, _, err := backend.Open(ctx, key, 1); codeOf(err) != httputil.CodeFileStoreError {
			t.Fatalf("open %q = %v", key, err)
		}
		if err := backend.Delete(ctx, key); codeOf(err) != httputil.CodeFileStoreError {
			t.Fatalf("delete %q = %v", key, err)
		}
	}
}

func TestS3PutRejectsAnEmptyPayloadWithoutCallingTheStore(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if err := backend.Put(context.Background(), testKey(), strings.NewReader(""), 0, testSum, ""); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("put with size 0 = %v", err)
	}
	if err := backend.Put(context.Background(), testKey(), strings.NewReader("x"), 1, "", ""); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("put with no digest = %v", err)
	}
	if called {
		t.Fatal("a locally invalid put must not reach the store")
	}
}

func TestS3ErrorsBecomeStoreFaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	ctx := context.Background()
	if err := backend.Put(ctx, testKey(), strings.NewReader("payload"), int64(len("payload")), testSum, ""); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("put = %v", err)
	}
	if _, _, err := backend.Open(ctx, testKey(), 7); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("open = %v", err)
	}
	if err := backend.Delete(ctx, testKey()); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("delete = %v", err)
	}
	if err := backend.Probe(ctx); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("probe = %v", err)
	}
}

func TestS3OpenSpoolsTheBodyIntoASeekableFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.Header.Get("x-amz-content-sha256") != emptyPayload {
			t.Errorf("a bodyless request must sign the empty payload hash")
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	body, size, err := backend.Open(context.Background(), testKey(), 7)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if size != 7 {
		t.Fatalf("size = %d, want 7", size)
	}
	// The serve-content path seeks before it writes the first byte.
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	blob, _ := io.ReadAll(body)
	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if string(blob) != "payload" {
		t.Fatalf("body = %q", blob)
	}
}

func TestS3OpenRefusesAnObjectLargerThanItsRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 64)))
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if _, _, err := backend.Open(context.Background(), testKey(), 8); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("open = %v, want file_store_error when the store disagrees with the row", err)
	}
}

func TestS3OpenTreatsAMissingObjectAsAStoreFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if _, _, err := backend.Open(context.Background(), testKey(), 7); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("open = %v, want file_store_error", err)
	}
}

func TestS3DeleteAndProbeAcceptTheStoresAnswers(t *testing.T) {
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if r.URL.Path != "/shared" {
				t.Errorf("probe path = %q, want the bucket", r.URL.Path)
			}
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	if err := backend.Delete(context.Background(), testKey()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := backend.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// A delete of an object that is already gone is not an error; a probe that
	// cannot see the bucket is.
	status = http.StatusNotFound
	if err := backend.Delete(context.Background(), testKey()); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
	if err := backend.Probe(context.Background()); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("probe = %v, want file_store_error", err)
	}
}

func TestS3TransportFailuresBecomeStoreFaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL
	srv.Close() // every request now fails at the transport layer

	backend, err := NewS3Backend(s3Options(endpoint))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	ctx := context.Background()
	if err := backend.Put(ctx, testKey(), strings.NewReader("payload"), int64(len("payload")), testSum, ""); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("put = %v", err)
	}
	if _, _, err := backend.Open(ctx, testKey(), 7); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("open = %v", err)
	}
	if err := backend.Delete(ctx, testKey()); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("delete = %v", err)
	}
	if err := backend.Probe(ctx); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("probe = %v", err)
	}
}

func TestS3FollowsNoRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	backend, err := NewS3Backend(s3Options(srv.URL))
	if err != nil {
		t.Fatalf("NewS3Backend: %v", err)
	}
	// A 3xx is not a success, so the call fails instead of handing the signed
	// request to a host the operator never named.
	if err := backend.Put(context.Background(), testKey(), strings.NewReader("payload"), int64(len("payload")), testSum, ""); codeOf(err) != httputil.CodeFileStoreError {
		t.Fatalf("put = %v, want file_store_error on a redirect", err)
	}
}
