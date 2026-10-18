package handlers

// Sessions are the credential a user lives with, so the contract tested here is
// the one an operator relies on: the list marks the session in use, revoking
// somebody else's device needs a permission, and a revoked session stops
// working immediately.

import (
	"net/http"
	"testing"
)

func TestSessionListMarksTheSessionInUse(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	// A second device: the list has to be able to tell the two apart.
	other := sessionOf(t, h, "owner@example.com", "correct horse")

	w := do(t, h, http.MethodGet, "/api/auth/sessions", "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	decodeJSON(t, w, &body)
	if len(body.Sessions) < 2 {
		t.Fatalf("sessions = %d, want both devices listed", len(body.Sessions))
	}
	current := 0
	for _, s := range body.Sessions {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("current sessions = %d, want exactly one", current)
	}
	if other == owner {
		t.Fatal("the second login reused the first session cookie")
	}
}

// Revoking one device must leave the others alone — that is the whole point of
// "log this device out".
func TestRevokingOwnSessionLeavesTheOthersAlone(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	second := sessionOf(t, h, "owner@example.com", "correct horse")

	w := do(t, h, http.MethodGet, "/api/auth/sessions", "", authHeader(owner))
	var listed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	decodeJSON(t, w, &listed)

	var otherID string
	for _, s := range listed.Sessions {
		if !s.Current {
			otherID = s.ID
			break
		}
	}
	if otherID == "" {
		t.Fatalf("no session other than the current one: %v", listed.Sessions)
	}

	w = do(t, h, http.MethodDelete, "/api/auth/sessions/"+otherID, "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, body = %s", w.Code, w.Body.String())
	}
	if w := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(second)); w.Code != http.StatusUnauthorized {
		t.Fatalf("the revoked session still answers /api/auth/me with %d", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(owner)); w.Code != http.StatusOK {
		t.Fatalf("the current session was revoked too: status = %d", w.Code)
	}
}

// Revoking a session that belongs to somebody else is an administration
// action: without sessions:manage it must be refused, not silently ignored.
func TestRevokingAnotherUsersSessionsNeedsThePermission(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"member@example.com","password":"correct horse"}`, authHeader(owner))
	member := sessionOf(t, h, "member@example.com", "correct horse")

	w := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(member))
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &me)
	if me.User.ID == "" {
		t.Fatalf("/api/auth/me returned no id: %s", w.Body.String())
	}

	// The owner holds every permission, so this one has to work...
	if w := do(t, h, http.MethodDelete, "/api/users/"+me.User.ID+"/sessions", "", authHeader(owner)); w.Code != http.StatusNoContent {
		t.Fatalf("owner revoke: status = %d, body = %s", w.Code, w.Body.String())
	}
	// ...and the member's own session is gone with it.
	if w := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(member)); w.Code != http.StatusUnauthorized {
		t.Fatalf("the member's session survived the revoke: status = %d", w.Code)
	}

	// A second member may not do the same to a third: being signed in is not
	// being an administrator.
	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"third@example.com","password":"correct horse"}`, authHeader(owner))
	third := sessionOf(t, h, "third@example.com", "correct horse")
	do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(third))
	w = do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(third))
	var thirdMe struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &thirdMe)

	member = sessionOf(t, h, "member@example.com", "correct horse")
	w = do(t, h, http.MethodDelete, "/api/users/"+thirdMe.User.ID+"/sessions", "", authHeader(member))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member revoke: status = %d, want 403", w.Code)
	}
}

// Reading a user that does not exist is a 404 with a code, not an empty body:
// the console has to be able to say which of the two happened.
func TestReadingAnUnknownUserIsANotFound(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodGet, "/api/users/user-does-not-exist", "", authHeader(owner))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if got := errCode(t, w); got != "not_found" {
		t.Fatalf("code = %q, want not_found", got)
	}
}

// Logging out has to make the browser forget too: revoking the session server
// side while leaving the cookie set would keep sending a dead credential.
func TestLogoutClearsTheCookie(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/auth/logout", "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, body = %s", w.Code, w.Body.String())
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "agenthub_session" && (c.Value == "" || c.MaxAge < 0) {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout left the cookie in place: %v", w.Result().Cookies())
	}
}
