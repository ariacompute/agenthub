package handlers

// The administration endpoints decide who may do what in agenthub, so what
// matters here is not the JSON shape but the gate in front of each route and
// the code a refusal carries: the console localises the code, and a 500 where
// a 409 belongs would tell an operator the wrong thing.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/authz"
)

// recordingAuthz is the authorization store with a memory of what it was asked
// to do. It embeds the no-op so only the calls under test need an answer, and
// each can be made to fail with the error the real store would return.
type recordingAuthz struct {
	noopAuthz
	createdRole   *authz.Role
	updatedRole   *authz.Role
	createdGroup  *authz.Group
	deletedRole   string
	deletedGroup  string
	deletedGrant  string
	createdGrant  *authz.Grant
	roleErr       error
	groupErr      error
	grantErr      error
	deleteRoleErr error
}

func (s *recordingAuthz) CreateRole(_ context.Context, r *authz.Role) error {
	if s.roleErr != nil {
		return s.roleErr
	}
	cp := *r
	s.createdRole = &cp
	return nil
}

func (s *recordingAuthz) UpdateRole(_ context.Context, r *authz.Role) error {
	if s.roleErr != nil {
		return s.roleErr
	}
	cp := *r
	s.updatedRole = &cp
	return nil
}

func (s *recordingAuthz) DeleteRole(_ context.Context, id string) error {
	if s.deleteRoleErr != nil {
		return s.deleteRoleErr
	}
	s.deletedRole = id
	return nil
}

func (s *recordingAuthz) CreateGroup(_ context.Context, g *authz.Group) error {
	if s.groupErr != nil {
		return s.groupErr
	}
	cp := *g
	s.createdGroup = &cp
	return nil
}

func (s *recordingAuthz) UpdateGroup(_ context.Context, g *authz.Group) error {
	if s.groupErr != nil {
		return s.groupErr
	}
	cp := *g
	s.createdGroup = &cp
	return nil
}

func (s *recordingAuthz) DeleteGroup(_ context.Context, id string) error {
	if s.groupErr != nil {
		return s.groupErr
	}
	s.deletedGroup = id
	return nil
}

func (s *recordingAuthz) CreateGrant(_ context.Context, g *authz.Grant) error {
	if s.grantErr != nil {
		return s.grantErr
	}
	cp := *g
	s.createdGrant = &cp
	return nil
}

func (s *recordingAuthz) DeleteGrant(_ context.Context, id string) error {
	if s.grantErr != nil {
		return s.grantErr
	}
	s.deletedGrant = id
	return nil
}

// adminTestDeps wires the identity stack over a recording authorization store,
// so a test can both drive the HTTP route and see what reached the store.
func adminTestDeps(t *testing.T) (Deps, *recordingAuthz) {
	t.Helper()
	d, _ := authTestDeps(t, "")
	az := &recordingAuthz{}
	d.Authz = authz.New(az)
	return d, az
}

// ---------------------------------------------------------------------------
// permissions and roles
// ---------------------------------------------------------------------------

func TestPermissionVocabularyIsListedWithoutPersistence(t *testing.T) {
	// No authorization store: the vocabulary is still worth rendering, because
	// the console builds its matrix from this endpoint.
	d, _ := authTestDeps(t, "")
	d.Auth = nil
	d.Authz = nil
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/permissions", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Permissions []struct {
			Code string `json:"code"`
		} `json:"permissions"`
	}
	decodeJSON(t, w, &body)
	if len(body.Permissions) != len(authz.AllPermissions) {
		t.Fatalf("permissions = %d, want the whole vocabulary (%d)", len(body.Permissions), len(authz.AllPermissions))
	}
	// The newest permission has to be in the list: the console renders the
	// matrix from this endpoint, so a permission the backend enforces but does
	// not report is one nobody can grant.
	found := false
	for _, p := range body.Permissions {
		if p.Code == authz.PermBackupsManage {
			found = true
		}
	}
	if !found {
		t.Fatalf("permissions = %v, want it to include %q", body.Permissions, authz.PermBackupsManage)
	}
}

func TestRoleLifecycleReachesTheStore(t *testing.T) {
	d, az := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/roles", `{"name":"auditors","permissions":["tasks:read"]}`, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", w.Code, w.Body.String())
	}
	if az.createdRole == nil || az.createdRole.Name != "auditors" {
		t.Fatalf("create reached the store as %+v", az.createdRole)
	}

	w = do(t, h, http.MethodPut, "/api/roles/role-1", `{"name":"auditors","permissions":["tasks:read","tasks:write"]}`, authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", w.Code, w.Body.String())
	}
	if az.updatedRole == nil || az.updatedRole.ID != "role-1" {
		t.Fatalf("update reached the store as %+v", az.updatedRole)
	}

	w = do(t, h, http.MethodDelete, "/api/roles/role-1", "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body = %s", w.Code, w.Body.String())
	}
	if az.deletedRole != "role-1" {
		t.Fatalf("deletedRole = %q", az.deletedRole)
	}
}

func TestRoleFailuresCarryTheCodeTheConsoleLocalises(t *testing.T) {
	d, az := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	az.roleErr = authz.ErrRoleExists
	w := do(t, h, http.MethodPost, "/api/roles", `{"name":"auditors"}`, authHeader(owner))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate role: status = %d, want 409", w.Code)
	}
	if got := errCode(t, w); got != "role_already_exists" {
		t.Fatalf("code = %q, want role_already_exists", got)
	}

	az.roleErr = authz.ErrUnknownPermission
	w = do(t, h, http.MethodPost, "/api/roles", `{"name":"auditors","permissions":["launch:missiles"]}`, authHeader(owner))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission: status = %d, want 400", w.Code)
	}
	if got := errCode(t, w); got != "unknown_permission" {
		t.Fatalf("code = %q, want unknown_permission", got)
	}

	az.roleErr = nil
	// A built-in role is not a conflict to retry: it is a refusal, so the
	// console can drop the button instead of offering a retry.
	az.deleteRoleErr = authz.ErrBuiltinRole
	w = do(t, h, http.MethodDelete, "/api/roles/role-admin", "", authHeader(owner))
	if w.Code != http.StatusForbidden {
		t.Fatalf("built-in role: status = %d, want 403", w.Code)
	}
	if got := errCode(t, w); got != "builtin_role" {
		t.Fatalf("code = %q, want builtin_role", got)
	}
}

func TestGroupFailuresCarryTheCodeTheConsoleLocalises(t *testing.T) {
	d, az := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	az.groupErr = authz.ErrGroupExists
	w := do(t, h, http.MethodPost, "/api/groups", `{"name":"ops"}`, authHeader(owner))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate group: status = %d, want 409", w.Code)
	}
	if got := errCode(t, w); got != "group_already_exists" {
		t.Fatalf("code = %q, want group_already_exists", got)
	}
}

// ---------------------------------------------------------------------------
// resource grants
// ---------------------------------------------------------------------------

func TestGrantRoutesAreGatedAndMapped(t *testing.T) {
	d, az := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/grants", `{"resourceId":"res-1","principalKind":"user","principalId":"user-1","level":"read"}`, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create grant: status = %d, body = %s", w.Code, w.Body.String())
	}
	if az.createdGrant == nil || az.createdGrant.ResourceID != "res-1" {
		t.Fatalf("grant reached the store as %+v", az.createdGrant)
	}

	// A grant level the backend does not know is a client bug, and the console
	// has to be able to say so.
	az.grantErr = authz.ErrInvalidLevel
	w = do(t, h, http.MethodPost, "/api/grants", `{"resourceId":"res-1","principalKind":"user","principalId":"user-1","level":"own"}`, authHeader(owner))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad level: status = %d, want 400", w.Code)
	}

	az.grantErr = nil
	w = do(t, h, http.MethodDelete, "/api/grants/grant-1", "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete grant: status = %d, body = %s", w.Code, w.Body.String())
	}
	if az.deletedGrant != "grant-1" {
		t.Fatalf("deletedGrant = %q", az.deletedGrant)
	}
}

// ---------------------------------------------------------------------------
// the gate itself
// ---------------------------------------------------------------------------

// A second account with no roles at all must be turned away from every
// administration route: being signed in is not being an administrator.
func TestAdministrationRoutesRefuseAnAccountWithNoPermissions(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"member@example.com","password":"correct horse"}`, authHeader(owner))
	member := sessionOf(t, h, "member@example.com", "correct horse")

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/users", ""},
		{http.MethodPost, "/api/users", `{"email":"x@example.com","password":"correct horse"}`},
		{http.MethodGet, "/api/users/user-1", ""},
		{http.MethodPut, "/api/users/user-1", `{"name":"x"}`},
		{http.MethodDelete, "/api/users/user-1", ""},
		{http.MethodDelete, "/api/users/user-1/sessions", ""},
		{http.MethodGet, "/api/permissions", ""},
		{http.MethodGet, "/api/roles", ""},
		{http.MethodPost, "/api/roles", `{"name":"x"}`},
		{http.MethodPut, "/api/roles/role-1", `{"name":"x"}`},
		{http.MethodDelete, "/api/roles/role-1", ""},
		{http.MethodGet, "/api/groups", ""},
		{http.MethodPost, "/api/groups", `{"name":"x"}`},
		{http.MethodGet, "/api/grants", ""},
		{http.MethodPost, "/api/grants", `{"resourceId":"r","principalKind":"user","principalId":"u","level":"read"}`},
		{http.MethodGet, "/api/backups", ""},
	}
	for _, r := range routes {
		w := do(t, h, r.method, r.path, r.body, authHeader(member))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", r.method, r.path, w.Code)
			continue
		}
		if got := errCode(t, w); got != "forbidden" {
			t.Errorf("%s %s: code = %q, want forbidden", r.method, r.path, got)
		}
	}
}

// The mapping is the contract with the console: an unregistered error would
// degrade to 500 and read as an outage rather than a refusal.
func TestAuthzErrorsMapOntoTheDocumentedStatuses(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{authz.ErrRoleExists, http.StatusConflict, "role_already_exists"},
		{authz.ErrGroupExists, http.StatusConflict, "group_already_exists"},
		{authz.ErrBuiltinRole, http.StatusForbidden, "builtin_role"},
		{authz.ErrUnknownPermission, http.StatusBadRequest, "unknown_permission"},
		{authz.ErrInvalidLevel, http.StatusBadRequest, "invalid_grant"},
		{authz.ErrRoleNotFound, http.StatusNotFound, "not_found"},
		{authz.ErrGroupNotFound, http.StatusNotFound, "not_found"},
		{authz.ErrGrantNotFound, http.StatusNotFound, "not_found"},
		{errors.New("something else entirely"), http.StatusInternalServerError, "internal_error"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeAuthzErr(rec, c.err)
		if rec.Code != c.status {
			t.Errorf("%v: status = %d, want %d", c.err, rec.Code, c.status)
		}
		if got := errCode(t, rec); got != c.code {
			t.Errorf("%v: code = %q, want %q", c.err, got, c.code)
		}
	}
}
