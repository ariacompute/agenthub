package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The JSON-RPC envelope is the frozen contract every SDK depends on, so the
// writers are pinned: which status each error class carries, and the exact
// shape of a result.

func TestNewResponseCarriesTheVersion(t *testing.T) {
	res := NewResponse("req-1", map[string]any{"ok": true})
	if res.JSONRPC != JSONRPCVersion {
		t.Fatalf("jsonrpc = %q, want %q", res.JSONRPC, JSONRPCVersion)
	}
	if res.ID != "req-1" || res.Error != nil {
		t.Fatalf("response = %+v", res)
	}
	// An id may be a number or null: JSON-RPC allows all three.
	if got := NewResponse(nil, nil).ID; got != nil {
		t.Fatalf("null id = %v", got)
	}
	if got := NewResponse(float64(7), nil).ID; got != float64(7) {
		t.Fatalf("numeric id = %v", got)
	}
}

// A well formed envelope always answers 200 — protocol failures live in the
// error object — with two deliberate exceptions.
func TestWriteResultAndErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteResult(rec, "req-1", map[string]any{"status": "completed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body = %q: %v", rec.Body.String(), err)
	}
	if body.JSONRPC != JSONRPCVersion || body.ID != "req-1" || body.Error != nil {
		t.Fatalf("body = %+v", body)
	}

	cases := []struct {
		code int
		want int
	}{
		{CodeParseError, http.StatusBadRequest},
		{CodeInvalidRequest, http.StatusBadRequest},
		{CodeMethodNotFound, http.StatusNotFound},
		{CodeInternalError, http.StatusOK},
		{CodeTaskNotFound, http.StatusOK},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		WriteRPCError(rec, "req-1", NewError(c.code, "boom"))
		if rec.Code != c.want {
			t.Fatalf("code %d: status = %d, want %d", c.code, rec.Code, c.want)
		}
		var got Response
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Error == nil || got.Error.Code != c.code {
			t.Fatalf("error object = %+v, want code %d", got.Error, c.code)
		}
		// A result and an error are mutually exclusive.
		if got.Result != nil {
			t.Fatalf("result alongside an error: %+v", got.Result)
		}
	}
}

// A domain error is mapped onto its JSON-RPC form; anything else becomes an
// internal error rather than leaking a Go message over the wire.
func TestWriteRPCErrMapsDomainErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteRPCErr(rec, "req-1", NewError(CodeTaskNotFound, "task_not_found"))
	var got Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == nil || got.Error.Code != CodeTaskNotFound {
		t.Fatalf("error = %+v", got.Error)
	}

	rec = httptest.NewRecorder()
	WriteRPCErr(rec, "req-1", errBoom{})
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == nil || got.Error.Code != CodeInternalError {
		t.Fatalf("unmapped error = %+v", got.Error)
	}
	if got.Error.Message != MsgInternalError {
		t.Fatalf("message = %q, want the generic one", got.Error.Message)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "connection reset by peer" }

// Data parts are how an adapter returns structured output, and either accepted
// field name for a push configuration has to resolve.
func TestPartAndPushConfigHelpers(t *testing.T) {
	part := NewDataPart(map[string]any{"provider": "mock"})
	if part.Type != PartTypeData || part.Text != "" {
		t.Fatalf("part = %+v", part)
	}
	if part.Data["provider"] != "mock" {
		t.Fatalf("data = %v", part.Data)
	}
	if got := NewTextPart("hello"); got.Type != PartTypeText || got.Text != "hello" {
		t.Fatalf("text part = %+v", got)
	}

	canonical := &PushNotificationConfig{URL: "https://cb.test/a"}
	alias := &PushNotificationConfig{URL: "https://cb.test/b"}
	params := TaskPushNotificationConfigParams{PushNotificationAlias: alias}
	if got := params.Config(); got != alias {
		t.Fatalf("config = %v, want the alias", got)
	}
	// The canonical field wins when both are present.
	params.PushNotification = canonical
	if got := params.Config(); got != canonical {
		t.Fatalf("config = %v, want the canonical field", got)
	}
	var empty TaskPushNotificationConfigParams
	if got := empty.Config(); got != nil {
		t.Fatalf("config = %v, want nil", got)
	}
}
