package handlers

// The administration routes when the authorization store is down.
//
// These are the console's most dangerous buttons: a delete that silently
// succeeded, or a create reported as failed after it was written, leaves an
// operator unsure whether a permission was really granted. The contract is the
// same as everywhere else — a failed store is a 5xx with a machine code, never a
// 200 that pretends the write happened.

import (
	"context"
	"net/http"
	"testing"

	"agenthub/internal/authz"
)

// failingAuthz is the authorization port with every write refused. It embeds the
// inert half so only the methods under test have to be listed, and returns the
// same error the GORM store would surface when the connection is gone.
type failingAuthz struct{ noopAuthz }

func (failingAuthz) ListPermissions(context.Context) ([]authz.Permission, error) {
	return nil, errStoreDown
}
func (failingAuthz) ListRoles(context.Context) ([]authz.Role, error)   { return nil, errStoreDown }
func (failingAuthz) CreateRole(context.Context, *authz.Role) error     { return errStoreDown }
func (failingAuthz) UpdateRole(context.Context, *authz.Role) error     { return errStoreDown }
func (failingAuthz) DeleteRole(context.Context, string) error          { return errStoreDown }
func (failingAuthz) ListGroups(context.Context) ([]authz.Group, error) { return nil, errStoreDown }
func (failingAuthz) CreateGroup(context.Context, *authz.Group) error   { return errStoreDown }
func (failingAuthz) UpdateGroup(context.Context, *authz.Group) error   { return errStoreDown }
func (failingAuthz) DeleteGroup(context.Context, string) error         { return errStoreDown }
func (failingAuthz) ListGrants(context.Context, string) ([]authz.Grant, error) {
	return nil, errStoreDown
}
func (failingAuthz) CreateGrant(context.Context, *authz.Grant) error { return errStoreDown }
func (failingAuthz) DeleteGrant(context.Context, string) error       { return errStoreDown }

// TestAdministrationReportsAFailingAuthzStore walks every route that reads or
// writes roles, groups and grants. A handler that swallowed the error would show
// an empty catalogue, and the next create would fail with "already exists".
func TestAdministrationReportsAFailingAuthzStore(t *testing.T) {
	d, _ := adminTestDeps(t)
	h := Register(d)
	owner := backupOwner(t, h)

	// Swap in the failing port after the owner exists: bootstrapping needs a
	// working store, and only the routes under test should see the outage.
	d.Authz = authz.New(failingAuthz{})
	h = Register(d)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"permissions", http.MethodGet, "/api/permissions", ""},
		{"roles", http.MethodGet, "/api/roles", ""},
		{"role create", http.MethodPost, "/api/roles", `{"name":"auditor","permissions":["audit:read"]}`},
		{"role update", http.MethodPut, "/api/roles/role-1", `{"name":"auditor"}`},
		{"role delete", http.MethodDelete, "/api/roles/role-1", ""},
		{"groups", http.MethodGet, "/api/groups", ""},
		{"group create", http.MethodPost, "/api/groups", `{"name":"ops"}`},
		{"group update", http.MethodPut, "/api/groups/group-1", `{"name":"ops-eu"}`},
		{"group delete", http.MethodDelete, "/api/groups/group-1", ""},
		{"grants", http.MethodGet, "/api/grants", ""},
		{"grant create", http.MethodPost, "/api/grants",
			`{"resourceId":"res-1","principalKind":"user","principalId":"user-1","level":"read"}`},
		{"grant delete", http.MethodDelete, "/api/grants/grant-1", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, tc.body, authHeader(owner))
			if w.Code < http.StatusInternalServerError {
				t.Fatalf("%s %s: status = %d, want 5xx (body %s)",
					tc.method, tc.path, w.Code, w.Body.String())
			}
			if code := errCode(t, w); code == "" {
				t.Fatalf("%s %s: a 5xx must carry a code, body = %s",
					tc.method, tc.path, w.Body.String())
			}
		})
	}
}
