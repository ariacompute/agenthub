package handlers

// The list and membership routes are what the admin screen paints itself from.
// They are read-mostly and therefore easy to leave untested, which is exactly how
// a renamed field or a silently empty list reaches production.

import (
	"context"
	"net/http"
	"testing"

	"agenthub/internal/authz"
)

// listAuthz answers the three list calls from fixed slices, and records the
// membership writes so the user drawer can be asserted end to end.
type listAuthz struct {
	recordingAuthz
	roles   []authz.Role
	groups  []authz.Group
	grants  []authz.Grant
	listErr error

	lastGrantResource string
	userRoles         map[string][]string
	userGroups        map[string][]string
}

func (s *listAuthz) ListRoles(context.Context) ([]authz.Role, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.roles, nil
}

func (s *listAuthz) ListGroups(context.Context) ([]authz.Group, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.groups, nil
}

func (s *listAuthz) ListGrants(_ context.Context, resourceID string) ([]authz.Grant, error) {
	s.lastGrantResource = resourceID
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.grants, nil
}

func (s *listAuthz) SetUserRoles(_ context.Context, userID string, roles []string) error {
	if s.userRoles == nil {
		s.userRoles = map[string][]string{}
	}
	s.userRoles[userID] = roles
	return nil
}

func (s *listAuthz) SetUserGroups(_ context.Context, userID string, groups []string) error {
	if s.userGroups == nil {
		s.userGroups = map[string][]string{}
	}
	s.userGroups[userID] = groups
	return nil
}

func listAdminDeps(t *testing.T) (Deps, *listAuthz) {
	t.Helper()
	d, _ := authTestDeps(t, "")
	az := &listAuthz{}
	d.Authz = authz.New(az)
	return d, az
}

func TestAdministrationListsReachTheStore(t *testing.T) {
	d, az := listAdminDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	az.roles = []authz.Role{{ID: "role-auditors", Name: "auditors", Permissions: []string{authz.PermTasksRead}}}
	w := do(t, h, http.MethodGet, "/api/roles", "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("roles: status = %d (%s)", w.Code, w.Body.String())
	}
	var roles struct {
		Roles []authz.Role `json:"roles"`
	}
	decodeJSON(t, w, &roles)
	if len(roles.Roles) != 1 || roles.Roles[0].Name != "auditors" {
		t.Fatalf("roles = %+v", roles.Roles)
	}

	az.groups = []authz.Group{{ID: "grp-ops", Name: "ops"}}
	w = do(t, h, http.MethodGet, "/api/groups", "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("groups: status = %d (%s)", w.Code, w.Body.String())
	}
	var groups struct {
		Groups []authz.Group `json:"groups"`
	}
	decodeJSON(t, w, &groups)
	if len(groups.Groups) != 1 || groups.Groups[0].Name != "ops" {
		t.Fatalf("groups = %+v", groups.Groups)
	}
}

func TestGrantListPassesTheResourceFilterThrough(t *testing.T) {
	d, az := listAdminDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	az.grants = []authz.Grant{{ID: "grant-1", ResourceID: "res-1", Level: authz.LevelRead}}
	w := do(t, h, http.MethodGet, "/api/grants?resource=res-1", "", authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("grants: status = %d (%s)", w.Code, w.Body.String())
	}
	if az.lastGrantResource != "res-1" {
		t.Fatalf("the resource filter must reach the store, got %q", az.lastGrantResource)
	}
	var body struct {
		Grants []authz.Grant `json:"grants"`
	}
	decodeJSON(t, w, &body)
	if len(body.Grants) != 1 || body.Grants[0].ResourceID != "res-1" {
		t.Fatalf("grants = %+v", body.Grants)
	}
}

func TestListRoutesAnswerEmptyWithoutAStore(t *testing.T) {
	// A fresh instance has no authorization tables yet; the console must still be
	// able to render its (empty) matrix rather than show a failure.
	d, _ := authTestDeps(t, "")
	d.Authz = nil
	h := Register(d)
	owner := backupOwner(t, h)

	for _, path := range []string{"/api/roles", "/api/groups", "/api/grants"} {
		w := do(t, h, http.MethodGet, path, "", authHeader(owner))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", path, w.Code, w.Body.String())
		}
	}
}

func TestListFailureIsReportedNotSwallowed(t *testing.T) {
	d, az := listAdminDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	az.listErr = authz.ErrRoleNotFound
	w := do(t, h, http.MethodGet, "/api/roles", "", authHeader(owner))
	if w.Code == http.StatusOK {
		t.Fatalf("a failed list must not answer 200: %s", w.Body.String())
	}
	if got := errCode(t, w); got == "" {
		t.Fatalf("a failure must carry a machine code, got %s", w.Body.String())
	}
}

func TestDeleteGroupReachesTheStore(t *testing.T) {
	d, az := listAdminDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodDelete, "/api/groups/grp-ops", "", authHeader(owner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d (%s)", w.Code, w.Body.String())
	}
	if az.deletedGroup != "grp-ops" {
		t.Fatalf("deletedGroup = %q", az.deletedGroup)
	}

	az.groupErr = authz.ErrGroupNotFound
	w = do(t, h, http.MethodDelete, "/api/groups/grp-missing", "", authHeader(owner))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing group: status = %d, want 404", w.Code)
	}
}

func TestUserUpdateWritesBothMembershipLists(t *testing.T) {
	d, az := listAdminDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	// Create a second account to edit, then replace its roles and groups.
	w := do(t, h, http.MethodPost, "/api/users", `{"email":"ops@example.com","name":"Ops","password":"correct horse battery","status":"active"}`, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: status = %d (%s)", w.Code, w.Body.String())
	}
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	id := created.User.ID
	if id == "" {
		t.Fatal("the created user must have an id")
	}

	w = do(t, h, http.MethodPut, "/api/users/"+id, `{"roles":["role-auditors"],"groups":["grp-ops"]}`, authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("update user: status = %d (%s)", w.Code, w.Body.String())
	}
	if got := az.userRoles[id]; len(got) != 1 || got[0] != "role-auditors" {
		t.Fatalf("roles reached the store as %v", got)
	}
	if got := az.userGroups[id]; len(got) != 1 || got[0] != "grp-ops" {
		t.Fatalf("groups reached the store as %v", got)
	}

	// An empty list clears; an omitted one means "leave alone". The two must not
	// be confused, or saving the drawer silently drops every group.
	w = do(t, h, http.MethodPut, "/api/users/"+id, `{"groups":[]}`, authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("update user: status = %d (%s)", w.Code, w.Body.String())
	}
	if got := az.userGroups[id]; len(got) != 0 {
		t.Fatalf("an empty list must clear the groups, got %v", got)
	}
	if got := az.userRoles[id]; len(got) != 1 {
		t.Fatalf("omitting roles must leave them alone, got %v", got)
	}
}
