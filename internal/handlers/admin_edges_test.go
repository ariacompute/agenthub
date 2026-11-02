package handlers

// The refusals an administrator can actually cause, as opposed to the ones the
// gate produces.
//
// `api_admin_test.go` covers who may reach these routes; this file covers what
// happens once they have: an id that names nobody, an address the mail layer
// would refuse, a password shorter than the policy, a status the API does not
// have a meaning for. Each of those is a 4xx with a code the console switches
// on, and each would otherwise be discovered by an operator reading a 500.

import (
	"net/http"
	"testing"
)

// createUser posts one account and returns its id. The owner's session is
// reused so the call carries the permission the route requires.
func createUser(t *testing.T, h http.Handler, owner, email, password string) string {
	t.Helper()
	body := `{"email":"` + email + `","password":"` + password + `"}`
	w := do(t, h, http.MethodPost, "/api/users", body, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create user %q: status = %d, body = %s", email, w.Code, w.Body.String())
	}
	var out struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &out)
	if out.User.ID == "" {
		t.Fatalf("create user returned no id: %s", w.Body.String())
	}
	return out.User.ID
}

// TestAdminRefusesAnUnknownUserId pins the difference between "no such account"
// and "the database is down": the console offers a retry for one and a link back
// for the other, so they must not look alike.
func TestAdminRefusesAnUnknownUserId(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"read", http.MethodGet, "/api/users/does-not-exist", ""},
		{"update", http.MethodPut, "/api/users/does-not-exist", `{"name":"x"}`},
		{"delete", http.MethodDelete, "/api/users/does-not-exist", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, tc.body, authHeader(owner))
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
			}
			if code := errCode(t, w); code != "not_found" {
				t.Fatalf("code = %q, want not_found", code)
			}
		})
	}

	// Revoking is the exception: "this account has no sessions" is the answer,
	// not "this account does not exist", so a signed-out user stays deletable
	// even after the console has already revoked everything.
	w := do(t, h, http.MethodDelete, "/api/users/does-not-exist/sessions", "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke for an unknown id: status = %d, want 204 (body %s)", w.Code, w.Body.String())
	}
}

// TestAdminRefusesCredentialsThatWouldNotWork covers the three bodies the
// creation and update routes reject before they touch the store: an address the
// login form could never produce, a password below the policy, and a status the
// account lifecycle has no state for.
func TestAdminRefusesCredentialsThatWouldNotWork(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)
	id := createUser(t, h, owner, "operator@example.com", "correct horse")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{
			name:   "create with an address that is not one",
			method: http.MethodPost, path: "/api/users",
			body:   `{"email":"not-an-address","password":"correct horse"}`,
			status: http.StatusBadRequest, code: "invalid_email",
		},
		{
			name:   "create with a password below the policy",
			method: http.MethodPost, path: "/api/users",
			body:   `{"email":"short@example.com","password":"short"}`,
			status: http.StatusBadRequest, code: "weak_password",
		},
		{
			name:   "create with a status that does not exist",
			method: http.MethodPost, path: "/api/users",
			body:   `{"email":"odd@example.com","password":"correct horse","status":"pending"}`,
			status: http.StatusBadRequest, code: "bad_request",
		},
		{
			name:   "update to an address that is not one",
			method: http.MethodPut, path: "/api/users/" + id,
			body:   `{"email":"also bad"}`,
			status: http.StatusBadRequest, code: "invalid_email",
		},
		{
			name:   "update to a password below the policy",
			method: http.MethodPut, path: "/api/users/" + id,
			body:   `{"password":"short"}`,
			status: http.StatusBadRequest, code: "weak_password",
		},
		{
			name:   "update to a status that does not exist",
			method: http.MethodPut, path: "/api/users/" + id,
			body:   `{"status":"pending"}`,
			status: http.StatusBadRequest, code: "bad_request",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, tc.body, authHeader(owner))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.status, w.Body.String())
			}
			if code := errCode(t, w); code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
		})
	}

	// None of the refusals above may have written anything: the account must
	// still be readable under its original address.
	w := do(t, h, http.MethodGet, "/api/users/"+id, "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("user vanished after a refused update: status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestAdminChangesStatusAndPasswordTogether keeps the two writes that share one
// request from interfering: a password reset on a disabled account must keep it
// disabled rather than quietly re-enabling it, because that is the shape the
// console's drawer sends.
func TestAdminChangesStatusAndPasswordTogether(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)
	id := createUser(t, h, owner, "mixed@example.com", "correct horse")

	w := do(t, h, http.MethodPut, "/api/users/"+id,
		`{"status":"disabled","password":"another horse"}`, authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", w.Code, w.Body.String())
	}
	var out struct {
		User struct {
			Status string `json:"status"`
		} `json:"user"`
	}
	decodeJSON(t, w, &out)
	if out.User.Status != "disabled" {
		t.Fatalf("status = %q, want disabled", out.User.Status)
	}
}

// Deleting an account disables it: the tasks, spans and grants it produced are
// part of the audit trail, so the row has to survive the deletion.
func TestDeletingAnAccountDisablesIt(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)
	id := createUser(t, h, owner, "leaver@example.com", "correct horse")

	if w := do(t, h, http.MethodDelete, "/api/users/"+id, "", authHeader(owner)); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body = %s", w.Code, w.Body.String())
	}

	w := do(t, h, http.MethodGet, "/api/users/"+id, "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("the row must survive deletion: status = %d", w.Code)
	}
	var out struct {
		User struct {
			Status string `json:"status"`
		} `json:"user"`
	}
	decodeJSON(t, w, &out)
	if out.User.Status != "disabled" {
		t.Fatalf("status = %q, want disabled", out.User.Status)
	}
}
