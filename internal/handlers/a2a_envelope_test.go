package handlers

// The JSON-RPC envelope, before any method runs.
//
// A2A peers are other people's programs: the first thing they get wrong is the
// envelope, and the answer has to be a JSON-RPC error with the standard code —
// not an HTTP 400 with a page, and not a 500. These are the four envelope
// refusals plus the one that needs no envelope at all: a method that reads an
// existing task, called with nobody signed in.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agenthub/internal/authz"
)

func TestJSONRPCEnvelopeRefusals(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	cases := []struct {
		name   string
		body   string
		status int
		code   float64
	}{
		{
			name:   "a body that is not JSON",
			body:   `{`,
			status: http.StatusBadRequest,
			code:   -32700, // parse_error
		},
		{
			name:   "a version that is not 2.0",
			body:   `{"jsonrpc":"1.0","id":1,"method":"tasks/get","params":{"id":"t"}}`,
			status: http.StatusBadRequest,
			code:   -32600, // invalid_request
		},
		{
			name:   "a method nobody implements",
			body:   `{"jsonrpc":"2.0","id":2,"method":"tasks/explode","params":{}}`,
			status: http.StatusNotFound,
			code:   -32601, // method_not_found
		},
		{
			name: "params of the wrong shape",
			body: `{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":"nope"}`,
			// A params error is answered in the envelope with 200: the call
			// arrived and was understood, only its argument was wrong.
			status: http.StatusOK,
			code:   -32602, // invalid_params
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, http.MethodPost, "/a2a", tc.body, nil)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.status, w.Body.String())
			}
			var resp struct {
				Error *struct {
					Code float64 `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body is not JSON-RPC: %v (%s)", err, w.Body.String())
			}
			if resp.Error == nil {
				t.Fatalf("a refused call must carry an error object: %s", w.Body.String())
			}
			if resp.Error.Code != tc.code {
				t.Fatalf("code = %v, want %v", resp.Error.Code, tc.code)
			}
		})
	}
}

// Reading, cancelling or repointing a task belongs to somebody: without a
// principal the answer is the A2A unauthenticated code, not an empty task.
func TestRequireCallerRefusesAnAnonymousTaskMethod(t *testing.T) {
	d := testDeps(t, openConfig(""))
	r, err := http.NewRequest(http.MethodPost, "/a2a", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	w := sandboxRecorder()

	if d.requireCaller(w, 7, r) {
		t.Fatal("an anonymous caller was let through")
	}
	if !strings.Contains(w.Body.String(), "-32009") {
		t.Fatalf("body = %s, want the unauthenticated code", w.Body.String())
	}

	// With the dev principal installed the same call passes.
	r2 := sandboxRequest(http.MethodPost, "/a2a", "", &authz.Principal{UserID: "owner"}, "")
	w2 := sandboxRecorder()
	if !d.requireCaller(w2, 7, r2) {
		t.Fatalf("a signed in caller was refused: %s", w2.Body.String())
	}
}
