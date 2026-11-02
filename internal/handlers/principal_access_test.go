package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/authz"
	"agenthub/internal/registry"
)

// brokenAuthz is the authorization store with the two resolution calls down.
// An outage must deny, never widen: the whole point of asking is that the
// answer decides whether a stranger reads the row.
type brokenAuthz struct{ noopAuthz }

func (brokenAuthz) GrantsForResource(context.Context, string) ([]authz.Grant, error) {
	return nil, errors.New("authz is down")
}

func (brokenAuthz) GrantsForPrincipal(context.Context, string, []string) ([]authz.Grant, error) {
	return nil, errors.New("authz is down")
}

func accessDeps(t *testing.T, store authz.AuthzStore, entries ...*registry.Entry) (Deps, *registry.Registry) {
	t.Helper()
	reg := registry.New()
	for _, e := range entries {
		stored, err := reg.Upsert(e)
		if err != nil {
			t.Fatalf("seed %q: %v", e.Name, err)
		}
		*e = *stored // the caller holds the id it will assert against
	}
	return Deps{
		Reg:   reg,
		Authz: authz.New(store),
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, reg
}

func reader(id string) *authz.Principal {
	return &authz.Principal{UserID: id, Permissions: map[string]bool{authz.PermResourcesRead: true}}
}

func TestCanAccessIsGrantOrPublication(t *testing.T) {
	az := newMemAuthz()
	private := &registry.Entry{Name: "ledger", Kind: registry.KindSkill, Visibility: registry.VisibilityRestricted}
	public := &registry.Entry{Name: "handbook", Kind: registry.KindSkill, Visibility: registry.VisibilityPublic}
	d, reg := accessDeps(t, az, private, public)
	stored, _ := reg.Get(private.ID)
	published, _ := reg.Get(public.ID)

	as := func(p *authz.Principal) *http.Request {
		return withPrincipal(httptest.NewRequest(http.MethodGet, "/api/resources", nil), p)
	}
	user := reader("u-1")

	// No grant, not published: invisible, which is the new default.
	if d.canAccess(as(user), stored.ID, false) {
		t.Fatal("a restricted record without a grant must be refused")
	}
	// With a read grant it opens for read but stays closed for write.
	az.grant("u-1", stored.ID, authz.LevelRead)
	if !d.canAccess(as(user), stored.ID, false) {
		t.Fatal("a granted record must be readable")
	}
	if d.canAccess(as(user), stored.ID, true) {
		t.Fatal("a read grant must not carry write")
	}
	// Somebody else still cannot see it.
	if d.canAccess(as(reader("u-2")), stored.ID, false) {
		t.Fatal("a grant is per principal")
	}

	// Publication needs no grant at all, and covers anonymous callers.
	if !d.canAccess(as(reader("u-3")), published.ID, false) {
		t.Fatal("a published record must be readable")
	}
	if !d.canAccess(as(nil), published.ID, false) {
		t.Fatal("an anonymous peer must read a published record")
	}
	// Publishing says nothing about writing.
	if d.canAccess(as(reader("u-3")), published.ID, true) {
		t.Fatal("public is not writable")
	}

	// The platform owner bypasses both.
	owner := &authz.Principal{UserID: "owner", IsOwner: true}
	if !d.canAccess(as(owner), stored.ID, true) {
		t.Fatal("the owner must reach every record")
	}
}

func TestCanAccessWithoutAnAuthzService(t *testing.T) {
	// An instance with no identity stack (demo mode, unit tests) has nothing to
	// ask: every caller is the dev principal.
	d := Deps{}
	if !d.canAccess(httptest.NewRequest(http.MethodGet, "/", nil), "res-1", true) {
		t.Fatal("no authz service means no gate")
	}
}

func TestCanAccessDeniesWhenTheStoreFails(t *testing.T) {
	d, _ := accessDeps(t, brokenAuthz{})
	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/api/resources", nil), reader("u-1"))
	if d.canAccess(req, "res-1", false) {
		t.Fatal("a failing authorization store must deny")
	}
}

func TestVisibleEntriesUnionsPublicationAndGrants(t *testing.T) {
	az := newMemAuthz()
	private := &registry.Entry{Name: "ledger", Kind: registry.KindSkill, Visibility: registry.VisibilityRestricted}
	granted := &registry.Entry{Name: "payroll", Kind: registry.KindSkill, Visibility: registry.VisibilityRestricted}
	public := &registry.Entry{Name: "handbook", Kind: registry.KindAgent, Visibility: registry.VisibilityPublic}
	d, reg := accessDeps(t, az, private, granted, public)
	all := reg.List("", "")
	byName := map[string]string{}
	for _, e := range all {
		byName[e.Name] = e.ID
	}
	az.grant("u-1", byName["payroll"], authz.LevelRead)

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/api/resources", nil), reader("u-1"))
	visible := d.visibleEntries(req, all)

	seen := map[string]bool{}
	for _, e := range visible {
		seen[e.Name] = true
	}
	if seen["ledger"] {
		t.Fatal("a restricted record with no grant must not be listed")
	}
	if !seen["payroll"] || !seen["handbook"] {
		t.Fatalf("visible = %v, want the granted and the published record", seen)
	}
}

func TestVisibleEntriesFailsClosed(t *testing.T) {
	d, reg := accessDeps(t, brokenAuthz{})
	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/api/resources", nil), reader("u-1"))
	if got := d.visibleEntries(req, reg.List("", "")); len(got) != 0 {
		t.Fatalf("visible = %v, want nothing when the store is down", got)
	}
}
