package registry

import (
	"errors"

	"agenthub/internal/httputil"
)

// fieldError reports a machine readable code plus the field that caused it.
//
// The code is what travels over the wire and what the console localises; the
// field only exists so logs and the control plane can say *which* input was
// wrong without inventing a code per field. It unwraps to a plain
// *httputil.Error, so every existing code path — httputil.AsError, the handler
// status mapping, the SDK error vocabulary — keeps working unchanged.
type fieldError struct {
	code  httputil.Code
	field string
}

func newFieldError(code httputil.Code, field string) error {
	return &fieldError{code: code, field: field}
}

func (e *fieldError) Error() string {
	if e.field == "" {
		return string(e.code)
	}
	return string(e.code) + ":" + e.field
}

// Unwrap makes the error chain resolve to the wire code.
func (e *fieldError) Unwrap() error { return &httputil.Error{Code: e.code} }

// FieldOf returns the field an error points at, or "" when the failure is not
// about one particular input.
func FieldOf(err error) string {
	var fe *fieldError
	if errors.As(err, &fe) {
		return fe.field
	}
	return ""
}
