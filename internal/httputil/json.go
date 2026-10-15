// Package httputil centralises JSON responses, machine readable errors and SSE
// streaming for every HTTP endpoint in agenthub.
//
// Errors never carry human prose: the wire format is always {"code":"..."} so
// the console can map a stable identifier onto its own localised copy.
package httputil

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// ErrorBody is the only error shape produced by agenthub.
type ErrorBody struct {
	Code string `json:"code"`
}

// Code is the machine readable identifier used by every agenthub error.
type Code string

const (
	CodeBadRequest       Code = "bad_request"
	CodeUnauthorized     Code = "unauthorized"
	CodeNotFound         Code = "not_found"
	CodeMethodNotAllowed Code = "method_not_allowed"
	CodeInternal         Code = "internal_error"
	CodePayloadTooLarge  Code = "payload_too_large"
)

// Error is a sentinel error carrying a machine readable code.
type Error struct{ Code Code }

func (e *Error) Error() string { return string(e.Code) }

// AsError unwraps any error into an *Error with a stable code.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal}
}

// WriteJSON serialises v with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: encode response", "error", err)
	}
}

// WriteError writes {"code": ...} with the matching status code.
func WriteError(w http.ResponseWriter, status int, code Code) {
	WriteJSON(w, status, ErrorBody{Code: string(code)})
}

// WriteErr maps a domain error onto an HTTP status plus its machine readable code.
func WriteErr(w http.ResponseWriter, err error) {
	e := AsError(err)
	switch e.Code {
	case CodeBadRequest:
		WriteError(w, http.StatusBadRequest, e.Code)
	case CodeUnauthorized:
		WriteError(w, http.StatusUnauthorized, e.Code)
	case CodeNotFound:
		WriteError(w, http.StatusNotFound, e.Code)
	case CodeMethodNotAllowed:
		WriteError(w, http.StatusMethodNotAllowed, e.Code)
	case CodePayloadTooLarge:
		WriteError(w, http.StatusRequestEntityTooLarge, e.Code)
	default:
		WriteError(w, http.StatusInternalServerError, e.Code)
	}
}

// Decode reads a bounded JSON body into dst. Oversized or malformed bodies
// always surface as an *Error with a machine readable code.
func Decode(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{Code: CodePayloadTooLarge}
		}
		if errors.Is(err, io.EOF) {
			return &Error{Code: CodeBadRequest}
		}
		return &Error{Code: CodeBadRequest}
	}
	return nil
}
