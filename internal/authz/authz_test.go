package authz

import (
	"context"
	"testing"
)

// fakeStore is the smallest store that answers the resolution queries; every
// other method is inherited from the zero value and simply does nothing.
type fakeStore struct {
	groups     map[string][]string
	roles      map[string][]string
	rolePerms  map[string][]string
	grants     []Grant
	restricted map[string]bool
}

func (s *fakeStore) GroupsOfUser(context.Context, string) ([]string, error) { return nil, nil }
func (s *fakeStore) RolesOfUser(_ context.Context, user string) ([]string, error) {
	return s.roles[user], nil
}
func (s *fakeStore) PermissionsOfRoles(_ context.Context, roles []string) ([]string, error) {
	out := []string{}
	for _, r := range roles {
		out = append(out, s.rolePerms[r]...)
	}
	return out, nil
}
func (s *fakeStore) GrantsForResource(_ context.Context, id string) ([]Grant, error) {
	out := []Grant{}
	for _, g := range s.grants {
		if g.ResourceID == id {
			out = append(out, g)
		}
	}
	return out, nil
}
func (s *fakeStore) GrantsForPrincipal(_ context.Context, user string, _ []string) ([]Grant, error) {
	out := []Grant{}
	for _, g := range s.grants {
		if g.PrincipalKind == PrincipalUser && g.PrincipalID == user {
			out = append(out, g)
		}
	}
	return out, nil
}
func (s *fakeStore) RestrictedResourceIDs(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		if s.restricted[id] {
			out[id] = true
		}
	}
	return out, nil
}
func (s *fakeStore) ListPermissions(context.Context) ([]Permission, error) { return nil, nil }
func (s *fakeStore) ListRoles(context.Context) ([]Role, error)             { return nil, nil }
func (s *fakeStore) CreateRole(context.Context, *Role) error               { return nil }
func (s *fakeStore) UpdateRole(context.Context, *Role) error               { return nil }
func (s *fakeStore) DeleteRole(context.Context, string) error              { return nil }
func (s *fakeStore) SetRolePermissions(context.Context, string, []string) error {
	return nil
}
func (s *fakeStore) ListGroups(context.Context) ([]Group, error)           { return nil, nil }
func (s *fakeStore) CreateGroup(context.Context, *Group) error             { return nil }
func (s *fakeStore) UpdateGroup(context.Context, *Group) error             { return nil }
func (s *fakeStore) DeleteGroup(context.Context, string) error             { return nil }
func (s *fakeStore) SetUserRoles(context.Context, string, []string) error  { return nil }
func (s *fakeStore) SetUserGroups(context.Context, string, []string) error { return nil }
func (s *fakeStore) ListGrants(context.Context, string) ([]Grant, error)   { return s.grants, nil }
func (s *fakeStore) CreateGrant(context.Context, *Grant) error             { return nil }
func (s *fakeStore) DeleteGrant(context.Context, string) error             { return nil }

func principal(owner bool, perms ...string) *Principal {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	return &Principal{UserID: "u1", Email: "u1@example.com", IsOwner: owner, Permissions: set}
}

// TestOwnerHoldsEveryPermission is the lockout guard: whatever a deployment
// does to its roles, the bootstrap owner can always fix it again.
func TestOwnerHoldsEveryPermission(t *testing.T) {
	p := principal(true)
	for _, code := range AllPermissions {
		if !p.Can(code) {
			t.Fatalf("owner must hold %q", code)
		}
	}
	if p.Can("not:a:permission") {
		t.Fatal("owner must not invent permission codes")
	}
	if principal(false).Can(PermResourcesRead) {
		t.Fatal("a principal without roles holds no permission")
	}
	if (&Principal{}).Anonymous() != true {
		t.Fatal("an empty principal is anonymous")
	}
}

// TestResourceWithoutGrantsIsOpen is the compatibility rule that keeps existing
// registries working after the grants table appears.
func TestResourceWithoutGrantsIsOpen(t *testing.T) {
	s := New(&fakeStore{})
	ok, err := s.CanAccessResource(context.Background(), principal(false, PermResourcesRead), "mcp-1", false)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !ok {
		t.Fatal("a resource without grants must stay readable")
	}

	// Without the matching permission nothing is visible, grants or not.
	ok, _ = s.CanAccessResource(context.Background(), principal(false), "mcp-1", false)
	if ok {
		t.Fatal("resources:read is required")
	}
}

// TestGrantLevelsSeparateReadFromWrite pins the two axes: a granted resource is
// invisible to everybody else, and read does not imply write.
func TestGrantLevelsSeparateReadFromWrite(t *testing.T) {
	s := New(&fakeStore{
		grants: []Grant{
			{ResourceID: "mcp-1", PrincipalKind: PrincipalUser, PrincipalID: "u1", Level: LevelRead},
		},
		restricted: map[string]bool{"mcp-1": true},
	})
	p := principal(false, PermResourcesRead, PermResourcesWrite)

	if ok, _ := s.CanAccessResource(context.Background(), p, "mcp-1", false); !ok {
		t.Fatal("the granted user may read")
	}
	if ok, _ := s.CanAccessResource(context.Background(), p, "mcp-1", true); ok {
		t.Fatal("a read grant must not allow writes")
	}
	other := &Principal{UserID: "u2", Permissions: map[string]bool{PermResourcesRead: true, PermResourcesWrite: true}}
	if ok, _ := s.CanAccessResource(context.Background(), other, "mcp-1", false); ok {
		t.Fatal("a resource granted to somebody else is invisible")
	}
	// The owner bypasses grants entirely.
	if ok, _ := s.CanAccessResource(context.Background(), principal(true), "mcp-1", true); !ok {
		t.Fatal("the owner bypasses grants")
	}
}

// TestVisibleResourceIDsNeverLeaksGrantedResources is the listing counterpart:
// what a caller cannot read must not show up in a list, an export or the Agent
// Card, and it must not inflate the counters either.
func TestVisibleResourceIDsNeverLeaksGrantedResources(t *testing.T) {
	s := New(&fakeStore{
		grants: []Grant{
			{ResourceID: "secret", PrincipalKind: PrincipalUser, PrincipalID: "u2", Level: LevelRead},
		},
		restricted: map[string]bool{"secret": true},
	})
	visible, err := s.VisibleResourceIDs(context.Background(), principal(false, PermResourcesRead), []string{"open", "secret"})
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if _, ok := visible["open"]; !ok {
		t.Fatal("the unrestricted resource must be visible")
	}
	if _, ok := visible["secret"]; ok {
		t.Fatal("a resource granted to somebody else must not be visible")
	}

	// Without resources:read the answer is empty, not "everything".
	empty, err := s.VisibleResourceIDs(context.Background(), principal(false), []string{"open", "secret"})
	if err != nil || len(empty) != 0 {
		t.Fatalf("no permission must see nothing, got %v (err %v)", empty, err)
	}
}

// TestValidationVocabulary keeps the admin API from storing shapes the
// resolution code would never understand.
func TestValidationVocabulary(t *testing.T) {
	if !ValidLevel(LevelRead) || !ValidLevel(LevelWrite) || ValidLevel("admin") {
		t.Fatal("levels are read|write")
	}
	if !ValidPrincipalKind(PrincipalUser) || !ValidPrincipalKind(PrincipalGroup) || ValidPrincipalKind("team") {
		t.Fatal("principal kinds are user|group")
	}
	for _, code := range AllPermissions {
		if !ValidPermission(code) {
			t.Fatalf("%q must be a known permission", code)
		}
	}
	if ValidPermission("everything") {
		t.Fatal("unknown codes must be rejected")
	}
}

// TestLoadResolvesGroupsRolesPermissions covers the one-query-per-step
// resolution: a user is expanded into groups, roles and then permissions.
func TestLoadResolvesGroupsRolesPermissions(t *testing.T) {
	s := New(&fakeStore{
		roles:     map[string][]string{"u1": {"role-viewer"}},
		rolePerms: map[string][]string{"role-viewer": {PermResourcesRead, PermTasksRead}},
	})
	p, err := s.Load(context.Background(), "u1", "u1@example.com", "U", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !p.Can(PermResourcesRead) || !p.Can(PermTasksRead) {
		t.Fatalf("permissions = %v", p.Permissions)
	}
	if p.Can(PermUsersManage) {
		t.Fatal("viewer must not manage users")
	}
	if len(p.Roles) != 1 || p.Roles[0] != "role-viewer" {
		t.Fatalf("roles = %v", p.Roles)
	}

	// No store at all (unit tests): an identity with an empty permission set
	// rather than a nil map nobody can read.
	bare, err := New(nil).Load(context.Background(), "u1", "e", "n", false)
	if err != nil {
		t.Fatalf("load without store: %v", err)
	}
	if bare.Permissions == nil {
		t.Fatal("permissions map must be initialised")
	}
}
