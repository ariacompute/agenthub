package authz

import (
	"context"
	"errors"
	"testing"
)

// brokenStore fails one resolution step at a time. A store outage must surface
// as an error rather than as "no groups, no permissions": the second answer
// would silently downgrade an authorized caller to anonymous.
type brokenStore struct {
	*fakeStore
	failGroups   bool
	failRoles    bool
	failPerms    bool
	failResource bool
	failVisible  bool
}

var errStoreDown = errors.New("store down")

func (s *brokenStore) GroupsOfUser(context.Context, string) ([]string, error) {
	if s.failGroups {
		return nil, errStoreDown
	}
	return s.fakeStore.GroupsOfUser(context.Background(), "")
}

func (s *brokenStore) RolesOfUser(ctx context.Context, user string) ([]string, error) {
	if s.failRoles {
		return nil, errStoreDown
	}
	return s.fakeStore.RolesOfUser(ctx, user)
}

func (s *brokenStore) PermissionsOfRoles(ctx context.Context, roles []string) ([]string, error) {
	if s.failPerms {
		return nil, errStoreDown
	}
	return s.fakeStore.PermissionsOfRoles(ctx, roles)
}

func (s *brokenStore) GrantsForResource(ctx context.Context, id string) ([]Grant, error) {
	if s.failResource {
		return nil, errStoreDown
	}
	return s.fakeStore.GrantsForResource(ctx, id)
}

func (s *brokenStore) GrantsForPrincipal(ctx context.Context, user string, groups []string) ([]Grant, error) {
	if s.failVisible {
		return nil, errStoreDown
	}
	return s.fakeStore.GrantsForPrincipal(ctx, user, groups)
}

func newBroken(fail func(*brokenStore)) *brokenStore {
	s := &brokenStore{fakeStore: &fakeStore{}}
	fail(s)
	return s
}

// Every resolution step reports its own failure; none of them may be swallowed.
func TestLoadPropagatesStoreFailures(t *testing.T) {
	cases := map[string]func(*brokenStore){
		"groups":      func(s *brokenStore) { s.failGroups = true },
		"roles":       func(s *brokenStore) { s.failRoles = true },
		"permissions": func(s *brokenStore) { s.failPerms = true },
	}
	for name, fail := range cases {
		s := newBroken(fail)
		p, err := New(s).Load(context.Background(), "u1", "e", "n", false)
		if !errors.Is(err, errStoreDown) {
			t.Fatalf("%s: err = %v, want the store failure", name, err)
		}
		if p != nil {
			t.Fatalf("%s: principal = %+v, want nil on error", name, p)
		}
	}
}

// A grant lookup that fails must not read as "no grant, therefore closed": the
// caller gets an error and decides, the authorization layer does not guess.
func TestResourceLookupFailuresAreReported(t *testing.T) {
	s := New(newBroken(func(s *brokenStore) { s.failResource = true }))
	p := principal(false, PermResourcesRead)
	if ok, err := s.CanAccessResource(context.Background(), p, "mcp-1", false); !errors.Is(err, errStoreDown) || ok {
		t.Fatalf("read check = %v, %v; want the store failure", ok, err)
	}

	s = New(newBroken(func(s *brokenStore) { s.failVisible = true }))
	if got, err := s.VisibleResourceIDs(context.Background(), p, []string{"mcp-1"}); !errors.Is(err, errStoreDown) || got != nil {
		t.Fatalf("visible = %v, %v; want the store failure", got, err)
	}
}

// Group grants are matched through the principal's group list, which is the
// only place that list is consulted: a reader in the granted group may read,
// a reader outside it may not.
func TestGroupGrantsResolveThroughTheGroupList(t *testing.T) {
	store := &fakeStore{grants: []Grant{
		{ResourceID: "mcp-1", PrincipalKind: PrincipalGroup, PrincipalID: "team-a", Level: LevelRead},
	}}
	s := New(store)

	inside := &Principal{
		UserID:      "u1",
		Groups:      []string{"team-a"},
		Permissions: map[string]bool{PermResourcesRead: true},
	}
	if ok, err := s.CanAccessResource(context.Background(), inside, "mcp-1", false); err != nil || !ok {
		t.Fatalf("group member = %v, %v; want read", ok, err)
	}

	outside := &Principal{
		UserID:      "u2",
		Groups:      []string{"team-b"},
		Permissions: map[string]bool{PermResourcesRead: true},
	}
	if ok, err := s.CanAccessResource(context.Background(), outside, "mcp-1", false); err != nil || ok {
		t.Fatalf("non member = %v, %v; want closed", ok, err)
	}

	// A write grant satisfies a read check, so a writer never loses read access.
	writeGrant := New(&fakeStore{grants: []Grant{
		{ResourceID: "mcp-1", PrincipalKind: PrincipalUser, PrincipalID: "u1", Level: LevelWrite},
	}})
	if ok, _ := writeGrant.CanAccessResource(context.Background(), principal(false, PermResourcesRead), "mcp-1", false); !ok {
		t.Fatal("a write grant must imply read")
	}
}

// VisibleResourceIDs keeps the highest level seen across grants, and a record
// outside the requested set is never added to the answer.
func TestVisibleResourceIDsKeepsTheHighestLevel(t *testing.T) {
	s := New(&fakeStore{grants: []Grant{
		{ResourceID: "mcp-1", PrincipalKind: PrincipalUser, PrincipalID: "u1", Level: LevelWrite},
		{ResourceID: "mcp-1", PrincipalKind: PrincipalUser, PrincipalID: "u1", Level: LevelRead},
		{ResourceID: "elsewhere", PrincipalKind: PrincipalUser, PrincipalID: "u1", Level: LevelWrite},
	}})
	got, err := s.VisibleResourceIDs(context.Background(), principal(false, PermResourcesRead), []string{"mcp-1"})
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if got["mcp-1"] != LevelWrite {
		t.Fatalf("level = %q, want write", got["mcp-1"])
	}
	if len(got) != 1 {
		t.Fatalf("answer = %v, want only the requested record", got)
	}
}

// A nil service is the in-memory/test shape: the owner still sees everything and
// a plain caller sees nothing, without a nil dereference.
func TestNilServiceAnswersEveryQuestion(t *testing.T) {
	var s *Service
	p := principal(true)
	if ok, err := s.CanAccessResource(context.Background(), p, "mcp-1", true); err != nil || !ok {
		t.Fatalf("nil service owner = %v, %v", ok, err)
	}
	got, err := s.VisibleResourceIDs(context.Background(), p, []string{"mcp-1"})
	if err != nil || got["mcp-1"] != LevelWrite {
		t.Fatalf("nil service visible = %v, %v", got, err)
	}
	if _, err := s.Load(context.Background(), "u1", "e", "n", false); err != nil {
		t.Fatalf("nil service load: %v", err)
	}
}
