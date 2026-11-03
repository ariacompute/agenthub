package handlers

import (
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"agenthub/internal/files"
	"agenthub/internal/httputil"
)

// multipartOverhead is how much room the multipart envelope gets on top of the
// file cap: boundaries, per-part headers, and the trailing terminator. It is
// deliberately generous — the exact cap on the file itself is enforced by the
// service, which counts the bytes, so this outer limit only has to stop a client
// from streaming forever.
const multipartOverhead = 1 << 20

// handleListFiles lists the caller's shared files.
//
// The response carries the cap and the backend name as well as the rows: the
// console shows "up to 32 MiB, stored locally" before the operator picks a file,
// and a second endpoint for two scalars would be one more thing to keep in sync.
func (d Deps) handleListFiles(w http.ResponseWriter, r *http.Request) {
	if !d.Files.Enabled() {
		httputil.WriteErr(w, files.ErrDisabled)
		return
	}
	list, err := d.Files.List(r.Context(), callerID(r))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"files":    list,
		"maxBytes": d.Files.MaxBytes(),
		"backend":  d.Files.BackendName(),
	})
}

// handleGetFile returns one row's metadata.
func (d Deps) handleGetFile(w http.ResponseWriter, r *http.Request) {
	if !d.Files.Enabled() {
		httputil.WriteErr(w, files.ErrDisabled)
		return
	}
	file, err := d.Files.Get(r.Context(), r.PathValue("id"), callerID(r))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, file)
}

// handleUploadFile accepts one multipart upload and stores its bytes.
//
// agenthub has no global body size middleware, so — exactly like the voice
// endpoint — this handler builds its own bounded reader. The outer limit is the
// file cap plus the envelope; the precise check happens in the service while the
// bytes are counted, so a client cannot win by lying about Content-Length.
func (d Deps) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if !d.Files.Enabled() {
		httputil.WriteErr(w, files.ErrDisabled)
		return
	}
	if owner := callerID(r); owner == "" {
		httputil.WriteErr(w, files.ErrForbidden)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" || !isMultipart(contentType) {
		httputil.WriteErr(w, files.ErrUnsupported)
		return
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		httputil.WriteErr(w, files.ErrUnsupported)
		return
	}

	limit := d.Files.MaxBytes() + multipartOverhead
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	part, err := firstFilePart(multipart.NewReader(r.Body, params["boundary"]))
	if err != nil {
		httputil.WriteErr(w, files.ErrUnsupported)
		return
	}
	defer func() { _ = part.Close() }()

	stored, err := d.Files.Upload(r.Context(), callerID(r), part.FileName(), part.Header.Get("Content-Type"), part)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httputil.WriteErr(w, files.ErrTooLarge)
			return
		}
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, stored)
}

// handleDownloadFile streams the bytes.
//
// The body is written by http.ServeContent, which is what makes a Range request
// work: a 32 MiB recording can be resumed instead of restarted. Content-Type is
// forced and nosniff is set so a stored HTML fragment can never be rendered as a
// page from this origin, and the name travels as an attachment rather than as
// something a browser will try to display inline.
func (d Deps) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	if !d.Files.Enabled() {
		httputil.WriteErr(w, files.ErrDisabled)
		return
	}
	file, body, size, err := d.Files.Download(r.Context(), r.PathValue("id"), callerID(r))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", file.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))

	modTime := time.Now()
	if parsed, perr := time.Parse(time.RFC3339, file.CreatedAt); perr == nil {
		modTime = parsed
	}
	http.ServeContent(w, r, file.Name, modTime, body)
}

// handleDeleteFile removes a row and its bytes.
func (d Deps) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	if !d.Files.Enabled() {
		httputil.WriteErr(w, files.ErrDisabled)
		return
	}
	if err := d.Files.Delete(r.Context(), r.PathValue("id"), callerID(r)); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// firstFilePart returns the first part of a multipart body that actually carries
// a file. A form field is skipped rather than mistaken for the body: a request
// that carries only fields has nothing to store, and treating "note" as a file
// name would put an empty file in somebody's list.
func firstFilePart(mr *multipart.Reader) (*multipart.Part, error) {
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue
		}
		return part, nil
	}
}
