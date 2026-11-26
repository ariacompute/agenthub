package handlers

// The user half of the admin screen under a broken identity store, plus the
// request shapes that must be rejected before anything is written. The authz
// half lives in admin_store_failures_test.go; the split keeps each file about
// one dependency.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/httputil"
)

// failingUsers refuses the four reads and writes the admin screen makes. Create
// stays intact: it is how the owner gets bootstrapped before the outage.
type failingUsers struct {
	*memUsers
	listErr   error
	getErr    error
	updateErr error
	deleteErr error
}

func (s *failingUsers) List(ctx context.Context, query string) ([]auth.User, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memUsers.List(ctx, query)
}

func (s *failingUsers) Get(ctx context.Context, id string) (*auth.User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memUsers.Get(ctx, id)
}

func (s *failingUsers) Update(ctx context.Context, u *auth.User) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.memUsers.Update(ctx, u)
}

func (s *failingUsers) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memUsers.Delete(ctx, id)
}

// failingMembership is a working authorization store whose membership writes are
// refused: the account must not be reported as created when its roles were not.
type failingMembership struct {
	noopAuthz
	rolesErr  error
	groupsErr error
}

func (s failingMembership) SetUserRoles(context.Context, string, []string) error {
	return s.rolesErr
}

func (s failingMembership) SetUserGroups(context.Context, string, []string) error {
	return s.groupsErr
}

// adminUserDeps wires the identity store under our control, so an outage can be
// switched on for one route and off again before the next signed-in call (the
// session lookup needs Get to answer).
func adminUserDeps(t *testing.T) (Deps, *failingUsers, *failingSessions) {
	t.Helper()
	d, _ := authTestDeps(t, "")
	users := &failingUsers{memUsers: newMemUsers()}
	sessions := &failingSessions{memSessions: newMemSessions()}
	d.Auth = auth.New(users, sessions, &memOAuthBindings{}, nil, d.Authz,
		auth.Options{CookieName: "agenthub_session", TTL: time.Hour})
	return d, users, sessions
}

func TestUserAdministrationReportsAFailingUserStore(t *testing.T) {
	d, users, _ := adminUserDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	w := do(t, h, http.MethodPost, "/api/users",
		`{"email":"bob@example.com","password":"correct horse","name":"Bob"}`, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create bob: status = %d, body = %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &created)

	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(owner)), &me)

	// A list failure must not look like "no users": an empty catalogue is what
	// makes an operator create a duplicate account.
	users.listErr = errStoreDown
	w = do(t, h, http.MethodGet, "/api/users?q=bob", "", authHeader(owner))
	if w.Code != http.StatusInternalServerError || decodeErrorCode(t, w) != string(httputil.CodeInternal) {
		t.Fatalf("list users: status = %d, body = %s", w.Code, w.Body.String())
	}
	users.listErr = nil

	// Get, update and delete all read the row first; the failure must surface as
	// a 5xx and never as a 404 that reads "already gone". These three are called
	// directly because authentication itself needs Get: the point of this block
	// is the read *inside* the handler, not the session lookup.
	users.getErr = errStoreDown
	getReq := httptest.NewRequest(http.MethodGet, "/api/users/"+created.User.ID, nil)
	getReq.SetPathValue("id", created.User.ID)
	w = httptest.NewRecorder()
	d.handleGetUser(w, getReq)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("get user: status = %d, body = %s", w.Code, w.Body.String())
	}

	putReq := httptest.NewRequest(http.MethodPut, "/api/users/"+created.User.ID, strings.NewReader(`{"name":"Bob"}`))
	putReq.SetPathValue("id", created.User.ID)
	w = httptest.NewRecorder()
	d.handleUpdateUser(w, putReq)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update user: status = %d, body = %s", w.Code, w.Body.String())
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/users/"+created.User.ID, nil)
	delReq.SetPathValue("id", created.User.ID)
	w = httptest.NewRecorder()
	d.handleDeleteUser(w, delReq)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete user: status = %d, body = %s", w.Code, w.Body.String())
	}
	users.getErr = nil

	users.updateErr = errStoreDown
	w = do(t, h, http.MethodPut, "/api/users/"+created.User.ID, `{"name":"Bob"}`, authHeader(owner))
	if w.Code != http.StatusInternalServerError || decodeErrorCode(t, w) != string(httputil.CodeInternal) {
		t.Fatalf("update store failure: status = %d, body = %s", w.Code, w.Body.String())
	}
	users.updateErr = nil

	users.deleteErr = errStoreDown
	w = do(t, h, http.MethodDelete, "/api/users/"+created.User.ID, "", authHeader(owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete store failure: status = %d, body = %s", w.Code, w.Body.String())
	}
	users.deleteErr = nil

	// Pre-provisioning writes the account and then disables it; a failed second
	// write must not be reported as a created account.
	users.updateErr = errStoreDown
	w = do(t, h, http.MethodPost, "/api/users",
		`{"email":"carol@example.com","password":"correct horse","status":"disabled"}`, authHeader(owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("pre-provisioned create: status = %d, body = %s", w.Code, w.Body.String())
	}
	users.updateErr = nil

	// The last owner cannot be disabled: nobody would be left to undo it.
	w = do(t, h, http.MethodPut, "/api/users/"+me.User.ID, `{"status":"disabled"}`, authHeader(owner))
	if w.Code != http.StatusBadRequest || decodeErrorCode(t, w) != string(httputil.CodeBadRequest) {
		t.Fatalf("disable the owner: status = %d, body = %s", w.Code, w.Body.String())
	}
	// Nor deleted.
	w = do(t, h, http.MethodDelete, "/api/users/"+me.User.ID, "", authHeader(owner))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("delete the owner: status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestUserMembershipFailuresReachTheCaller: roles and groups are written after
// the account, so a membership failure is the case where "created" would be a
// lie. The three variants are "no store at all" (membership is skipped by
// design), "roles refused" and "groups refused".
func TestUserMembershipFailuresReachTheCaller(t *testing.T) {
	d, _, _ := adminUserDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	// No authorization store: the account is still provisioned, and the console
	// shows it without memberships.
	d.Authz = nil
	h = Register(d)
	w := do(t, h, http.MethodPost, "/api/users",
		`{"email":"dave@example.com","password":"correct horse","roles":["viewer"]}`, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create without an authz store: status = %d, body = %s", w.Code, w.Body.String())
	}

	d.Authz = authz.New(failingMembership{rolesErr: errors.New("roles write failed")})
	h = Register(d)
	w = do(t, h, http.MethodPost, "/api/users",
		`{"email":"erin@example.com","password":"correct horse","roles":["viewer"]}`, authHeader(owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create with a failing role write: status = %d, body = %s", w.Code, w.Body.String())
	}

	d.Authz = authz.New(failingMembership{groupsErr: errors.New("groups write failed")})
	h = Register(d)
	w = do(t, h, http.MethodPost, "/api/users",
		`{"email":"frank@example.com","password":"correct horse","groups":["ops"]}`, authHeader(owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create with a failing group write: status = %d, body = %s", w.Code, w.Body.String())
	}

	// The same failure on the update path.
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, do(t, h, http.MethodPost, "/api/users",
		`{"email":"gina@example.com","password":"correct horse"}`, authHeader(owner)), &created)
	w = do(t, h, http.MethodPut, "/api/users/"+created.User.ID, `{"groups":["ops"]}`, authHeader(owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update with a failing group write: status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestUserDeletionSurvivesARevokeFailure: logout-everywhere is best effort after
// the row is gone. The account is disabled either way; failing the request would
// invite the operator to retry a delete that already happened.
func TestUserDeletionSurvivesARevokeFailure(t *testing.T) {
	d, _, sessions := adminUserDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, do(t, h, http.MethodPost, "/api/users",
		`{"email":"hank@example.com","password":"correct horse"}`, authHeader(owner)), &created)
	// The account needs a live session for the revoke below to be attempted at
	// all; "no sessions" answers ErrNotFound and is not the failure under test.
	sessionOf(t, h, "hank@example.com", "correct horse")

	sessions.revokeErr = errors.New("session store is down")
	w := do(t, h, http.MethodDelete, "/api/users/"+created.User.ID, "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete with a failing revoke: status = %d, body = %s", w.Code, w.Body.String())
	}

	// The explicit "kick this user" action, by contrast, is the whole point of
	// the request: a failure there must be reported.
	w = do(t, h, http.MethodDelete, "/api/users/"+created.User.ID+"/sessions", "", authHeader(owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("revoke sessions: status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestAdministrationRejectsUndecodableBodies: a truncated body is a 400 before
// any store is consulted, on every create and update route.
func TestAdministrationRejectsUndecodableBodies(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	for _, c := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/users"},
		{http.MethodPost, "/api/roles"},
		{http.MethodPut, "/api/roles/role-1"},
		{http.MethodPost, "/api/groups"},
		{http.MethodPut, "/api/groups/group-1"},
		{http.MethodPost, "/api/grants"},
	} {
		w := do(t, h, c.method, c.path, `{"name":`, authHeader(owner))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400 (body %s)", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// TestPermissionVocabularyWithAStore: without persistence the matrix is rendered
// from authz.AllPermissions; with a store it comes from the store. Both answer
// 200, and the second one is what the console edits against.
func TestPermissionVocabularyWithAStore(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodGet, "/api/permissions", "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	decodeJSON(t, w, &body)
	if _, ok := body["permissions"]; !ok {
		t.Fatalf("body = %v, want a permissions key", body)
	}
}
