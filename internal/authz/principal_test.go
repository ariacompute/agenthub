package authz

import (
	"context"
	"testing"
)

// The two built-in principals decide what a caller may do before any store is
// consulted: the open one a harness without identities runs as, and the
// anonymous one an agent that never logged in gets.

func TestDevPrincipalHoldsEveryPermission(t *testing.T) {
	p := DevPrincipal()
	if p.Anonymous() {
		t.Fatal("the dev principal is not anonymous")
	}
	if !p.IsOwner {
		t.Fatal("the dev principal must be the owner")
	}
	// Every permission in the vocabulary, not just the ones that existed when
	// this was written: a new permission is visible to the dev principal by
	// default, which is what keeps a fresh instance recoverable.
	for _, perm := range AllPermissions {
		if !p.Can(perm) {
			t.Fatalf("dev principal cannot %q", perm)
		}
	}
	// And nothing outside the vocabulary is granted by accident.
	if p.Can("everything:always") {
		t.Fatal("an unknown permission must not be granted")
	}
}

func TestAnonymousPrincipalMayOnlyDiscover(t *testing.T) {
	p := AnonymousPrincipal()
	if !p.Can(PermResourcesRead) {
		t.Fatal("an anonymous caller may read the public catalogue")
	}
	for _, perm := range []string{PermResourcesWrite, PermTasksRead, PermTasksWrite,
		PermUsersManage, PermRolesManage, PermGrantsManage, PermSessionsManage,
		PermBackupsManage, PermRegistriesManage, PermRegistryCurate, PermSourcesManage} {
		if p.Can(perm) {
			t.Fatalf("an anonymous caller must not hold %q", perm)
		}
	}
	// A nil principal is the same thing as no caller at all.
	var nilPrincipal *Principal
	if !nilPrincipal.Anonymous() {
		t.Fatal("a nil principal must be anonymous")
	}
	if nilPrincipal.Can(PermResourcesRead) {
		t.Fatal("a nil principal holds nothing")
	}
}

// The permission vocabulary is the contract with the console's matrix: every
// route guard asks for one of these, and an unknown code is refused rather than
// silently granted.
func TestPermissionVocabularyIsClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, perm := range AllPermissions {
		if !ValidPermission(perm) {
			t.Fatalf("%q is listed but not valid", perm)
		}
		if seen[perm] {
			t.Fatalf("%q is listed twice", perm)
		}
		seen[perm] = true
	}
	for _, unknown := range []string{"", "resources:delete", "everything", "RESOURCES:READ"} {
		if ValidPermission(unknown) {
			t.Fatalf("%q must not be a valid permission", unknown)
		}
	}
}

// The service is inert without a store, which is what lets the pure rules be
// tested without a database.
func TestStoreIsInertWithoutOne(t *testing.T) {
	var nilService *Service
	if nilService.Store() != nil {
		t.Fatal("a nil service exposes no store")
	}
	svc := New(nil)
	if svc.Store() != nil {
		t.Fatal("a service without a store exposes none")
	}
	// Loading still builds a principal: it just carries only what it is given.
	p, err := svc.Load(context.Background(), "user-1", "u@example.com", "U", false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.UserID != "user-1" || p.IsOwner {
		t.Fatalf("principal = %+v", p)
	}
	// An owner needs no store: every resource is theirs to see.
	owner, err := svc.Load(context.Background(), "user-2", "o@example.com", "O", true)
	if err != nil {
		t.Fatalf("load owner: %v", err)
	}
	visible, err := svc.VisibleResourceIDs(context.Background(), owner, []string{"res-a", "res-b"})
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if visible["res-a"] != LevelWrite || visible["res-b"] != LevelWrite {
		t.Fatalf("visible = %v", visible)
	}
	// A caller who cannot read the catalogue sees nothing at all.
	stranger := &Principal{UserID: "user-3", Permissions: map[string]bool{}}
	visible, err = svc.VisibleResourceIDs(context.Background(), stranger, []string{"res-a"})
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("visible = %v, want nothing", visible)
	}
}
