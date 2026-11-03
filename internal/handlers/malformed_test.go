package handlers

// A body that is not JSON is the caller's fault, and every write route says so in
// the same words: 400 with `bad_request`, never a 500 and never an empty success.
//
// This is one table instead of a case per handler because the answer is meant to
// be identical: `httputil.Decode` maps a malformed body, an unknown field and an
// empty body to one code, so a route that answers something else has skipped the
// decode and will happily act on zero values.

import (
	"net/http"
	"testing"
)

func TestMalformedBodiesNeverBecomeAnEmptySuccess(t *testing.T) {
	d := testDeps(t, openConfig(""))
	// The thread store is wired because two of the routes below need it to be
	// reachable at all; the rest of the cases never touch it.
	d.Threads = newMemThreads()
	h := Register(d)

	// Two answers are correct here and the table spells out which is which: a
	// wired surface refuses the body (400), and a surface whose feature is off
	// refuses the call before it reads one (404 for an absent store, 503 for a
	// disabled feature). What is never correct is a 2xx with an empty body, which
	// is what dropping the decode error used to produce.
	cases := []struct {
		name   string
		method string
		path   string
		status int
		code   string
	}{
		{"create a resource", http.MethodPost, "/api/resources", http.StatusBadRequest, "bad_request"},
		{"create a namespace", http.MethodPost, "/api/registries", http.StatusBadRequest, "bad_request"},
		{"create a thread", http.MethodPost, "/api/threads", http.StatusBadRequest, "bad_request"},

		// Channels are off in this build, so the refusal comes first and is about
		// the feature rather than the body.
		{"create a channel", http.MethodPost, "/api/channels", http.StatusServiceUnavailable, "channel_disabled"},
		// The scheduler, the sandbox control plane and its nodes are services this
		// Deps does not have.
		{"create a computer", http.MethodPost, "/api/computers", http.StatusServiceUnavailable, "feature_unavailable"},
		{"create a schedule", http.MethodPost, "/api/schedules", http.StatusServiceUnavailable, "feature_unavailable"},
		{"create a trigger", http.MethodPost, "/api/router/triggers", http.StatusServiceUnavailable, "feature_unavailable"},
		{"register a node", http.MethodPost, "/api/sandbox-nodes", http.StatusServiceUnavailable, "feature_unavailable"},
		// Administration has no store to write to.
		{"create a user", http.MethodPost, "/api/users", http.StatusNotFound, "not_found"},
		{"create a role", http.MethodPost, "/api/roles", http.StatusNotFound, "not_found"},
		{"create a group", http.MethodPost, "/api/groups", http.StatusNotFound, "not_found"},
		{"create a grant", http.MethodPost, "/api/grants", http.StatusNotFound, "not_found"},
		{"create an api key", http.MethodPost, "/api/api-keys", http.StatusNotFound, "not_found"},
		{"sign up", http.MethodPost, "/api/auth/register", http.StatusNotFound, "not_found"},
		{"sign in", http.MethodPost, "/api/auth/login", http.StatusNotFound, "not_found"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, `{`, nil)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.status, w.Body.String())
			}
			if code := errCode(t, w); code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

// A message is appended to a thread that has to exist first, which is why this
// case builds one: the decode runs after the ownership lookup, and an earlier
// version answered an empty 200 because the error was dropped on the floor.
func TestPostingAMalformedMessageIsRefused(t *testing.T) {
	d := testDeps(t, openConfig(""))
	d.Threads = newMemThreads()
	h := Register(d)

	created := do(t, h, http.MethodPost, "/api/threads", `{"title":"t"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create thread: status = %d, body = %s", created.Code, created.Body.String())
	}
	var thread struct {
		ID string `json:"id"`
	}
	decodeJSON(t, created, &thread)

	w := do(t, h, http.MethodPost, "/api/threads/"+thread.ID+"/messages", `{"body":`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", w.Code, w.Body.String())
	}
	if code := errCode(t, w); code != "bad_request" {
		t.Fatalf("code = %q, want bad_request", code)
	}
}

// The same rule for an update, where the row has to exist first: the resource is
// created through the API, then handed a body the decoder cannot read.
func TestUpdatingWithAMalformedBodyChangesNothing(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	created := do(t, h, http.MethodPost, "/api/resources", `{"kind":"model","name":"broken"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", created.Code, created.Body.String())
	}
	var entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	decodeJSON(t, created, &entry)

	w := do(t, h, http.MethodPut, "/api/resources/"+entry.ID, `{"kind":`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if code := errCode(t, w); code != "bad_request" {
		t.Fatalf("code = %q, want bad_request", code)
	}

	// The record is untouched: a failed decode must not reach the store.
	after := do(t, h, http.MethodGet, "/api/resources/"+entry.ID, "", nil)
	if after.Code != http.StatusOK {
		t.Fatalf("read back: status = %d", after.Code)
	}
	var stored struct {
		Name string `json:"name"`
	}
	decodeJSON(t, after, &stored)
	if stored.Name != entry.Name {
		t.Fatalf("name = %q after a rejected update, want %q", stored.Name, entry.Name)
	}
}
