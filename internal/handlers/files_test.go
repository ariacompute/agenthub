package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/files"
	"agenthub/internal/httputil"
)

// memFileStore is the metadata half of the files surface. The GORM
// implementation is covered by the integration suite; the handler only needs the
// port to behave.
type memFileStore struct {
	rows map[string]*files.File
	err  error
}

func newMemFileStore() *memFileStore { return &memFileStore{rows: map[string]*files.File{}} }

func (s *memFileStore) Create(ctx context.Context, f *files.File) (*files.File, error) {
	if s.err != nil {
		return nil, s.err
	}
	copy := *f
	s.rows[f.ID] = &copy
	return &copy, nil
}

func (s *memFileStore) Get(ctx context.Context, id, ownerID string) (*files.File, error) {
	if s.err != nil {
		return nil, s.err
	}
	row, ok := s.rows[id]
	if !ok || row.OwnerID != ownerID {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (s *memFileStore) List(ctx context.Context, ownerID string) ([]*files.File, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*files.File, 0, len(s.rows))
	for _, row := range s.rows {
		if row.OwnerID == ownerID {
			copy := *row
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (s *memFileStore) Delete(ctx context.Context, id, ownerID string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	row, ok := s.rows[id]
	if !ok || row.OwnerID != ownerID {
		return false, nil
	}
	delete(s.rows, id)
	return true, nil
}

// filesDeps wires a working local backend under a temporary directory.
func filesDeps(t *testing.T, max int64) (Deps, *memFileStore) {
	t.Helper()
	cfg := testConfig()
	if cfg == nil {
		t.Fatal("test config failed validation")
	}
	d := testDeps(t, cfg)
	store := newMemFileStore()
	backend, err := files.NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	svc, err := files.NewService(files.Options{Store: store, Backend: backend, MaxBytes: max})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	d.Files = svc
	return d, store
}

// uploadBody builds the multipart body the console sends.
func uploadBody(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, mw.FormDataContentType()
}

func postUpload(t *testing.T, h http.Handler, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := uploadBody(t, filename, content)
	req := httptest.NewRequest(http.MethodPost, "/api/files", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestFilesEndpointsReportADisabledFeature(t *testing.T) {
	d := testDeps(t, testConfig())
	h := Register(d)
	if d.Files != nil {
		t.Fatal("the harness must not wire a backend by default")
	}
	cases := []struct {
		method, target string
	}{
		{http.MethodGet, "/api/files"},
		{http.MethodGet, "/api/files/file_1"},
		{http.MethodGet, "/api/files/file_1/content"},
		{http.MethodDelete, "/api/files/file_1"},
	}
	for _, c := range cases {
		w := do(t, h, c.method, c.target, "", nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", c.method, c.target, w.Code)
		}
		if code := fileErrorCode(t, w); code != httputil.CodeFilesDisabled {
			t.Fatalf("%s %s code = %s, want files_disabled", c.method, c.target, code)
		}
	}
	// The upload is refused before the body is read, so no multipart envelope is
	// needed to prove the answer.
	w := do(t, h, http.MethodPost, "/api/files", "x", map[string]string{"Content-Type": "application/octet-stream"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload = %d, want 503", w.Code)
	}
}

func TestFileUploadListDownloadDeleteRoundTrip(t *testing.T) {
	d, _ := filesDeps(t, 1<<20)
	h := Register(d)

	w := postUpload(t, h, "notes.txt", "hello shared file")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload = %d (%s)", w.Code, w.Body.String())
	}
	var stored files.File
	decodeJSON(t, w, &stored)
	if stored.ID == "" || stored.Size != int64(len("hello shared file")) || stored.SHA256 == "" {
		t.Fatalf("stored = %+v", stored)
	}

	w = do(t, h, http.MethodGet, "/api/files", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}
	var list struct {
		Files    []*files.File `json:"files"`
		MaxBytes int64         `json:"maxBytes"`
		Backend  string        `json:"backend"`
	}
	decodeJSON(t, w, &list)
	if len(list.Files) != 1 || list.Files[0].ID != stored.ID {
		t.Fatalf("list = %+v", list)
	}
	// The console shows the cap and the backend before a file is chosen.
	if list.MaxBytes != 1<<20 || list.Backend != files.BackendLocal {
		t.Fatalf("cap/backend = %d/%q", list.MaxBytes, list.Backend)
	}

	w = do(t, h, http.MethodGet, "/api/files/"+stored.ID, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d", w.Code)
	}

	w = do(t, h, http.MethodGet, "/api/files/"+stored.ID+"/content", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("download = %d", w.Code)
	}
	if w.Body.String() != "hello shared file" {
		t.Fatalf("body = %q", w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") || !strings.Contains(got, "notes.txt") {
		t.Fatalf("disposition = %q, want an attachment named notes.txt", got)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff missing: %v", w.Header())
	}
	// A stored HTML fragment must come back as the type that was recorded, not
	// as something a browser renders.
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type = %q, want text/plain", ct)
	}

	w = do(t, h, http.MethodDelete, "/api/files/"+stored.ID, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}
	w = do(t, h, http.MethodGet, "/api/files/"+stored.ID, "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", w.Code)
	}
}

func TestFileDownloadServesRanges(t *testing.T) {
	d, _ := filesDeps(t, 1<<20)
	h := Register(d)
	w := postUpload(t, h, "clip.txt", "0123456789")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload = %d", w.Code)
	}
	var stored files.File
	decodeJSON(t, w, &stored)

	w = do(t, h, http.MethodGet, "/api/files/"+stored.ID+"/content", "", map[string]string{"Range": "bytes=2-5"})
	if w.Code != http.StatusPartialContent {
		t.Fatalf("range = %d, want 206", w.Code)
	}
	if w.Body.String() != "2345" {
		t.Fatalf("range body = %q, want 2345", w.Body.String())
	}
}

func TestFileUploadRejectsABadEnvelope(t *testing.T) {
	d, _ := filesDeps(t, 1<<20)
	h := Register(d)

	// Not multipart at all.
	w := do(t, h, http.MethodPost, "/api/files", `{"name":"a.txt"}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("json upload = %d, want 415", w.Code)
	}

	// Multipart without a boundary: parsing it would be a guess.
	w = do(t, h, http.MethodPost, "/api/files", "x", map[string]string{"Content-Type": "multipart/form-data"})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("boundaryless upload = %d, want 415", w.Code)
	}

	// Multipart with no file part.
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if err := mw.WriteField("note", "no file here"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/files", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("partless upload = %d, want 415", rec.Code)
	}
}

func TestFileUploadEnforcesTheCap(t *testing.T) {
	d, store := filesDeps(t, 8)
	h := Register(d)
	w := postUpload(t, h, "big.txt", strings.Repeat("x", 64))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload = %d, want 413", w.Code)
	}
	if code := fileErrorCode(t, w); code != httputil.CodeFileTooLarge {
		t.Fatalf("code = %s, want file_too_large", code)
	}
	if len(store.rows) != 0 {
		t.Fatalf("an oversized upload left a row behind")
	}
}

func TestFileReadsOfAMissingIDAreNotFound(t *testing.T) {
	d, _ := filesDeps(t, 1<<20)
	h := Register(d)
	for _, target := range []string{"/api/files/file_missing", "/api/files/file_missing/content"} {
		w := do(t, h, http.MethodGet, target, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", target, w.Code)
		}
	}
	if w := do(t, h, http.MethodDelete, "/api/files/file_missing", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", w.Code)
	}
}

func TestFileStoreFailuresAreGatewayErrors(t *testing.T) {
	d, store := filesDeps(t, 1<<20)
	h := Register(d)
	w := postUpload(t, h, "a.txt", "hi")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload = %d", w.Code)
	}
	var stored files.File
	decodeJSON(t, w, &stored)

	store.err = errTestStore
	w = do(t, h, http.MethodGet, "/api/files", "", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("list with a broken store = %d, want 500", w.Code)
	}
	w = do(t, h, http.MethodGet, "/api/files/"+stored.ID, "", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("get with a broken store = %d, want 500", w.Code)
	}
}

// errTestStore stands in for a database that is down: it is deliberately not a
// httputil.Error, so the handler has to fall back to the generic 500 rather than
// invent a file code.
var errTestStore = &storeTestError{}

type storeTestError struct{}

func (*storeTestError) Error() string { return "store down" }

func fileErrorCode(t *testing.T, w *httptest.ResponseRecorder) httputil.Code {
	t.Helper()
	var body struct {
		Code httputil.Code `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return body.Code
}
