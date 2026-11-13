package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A principal that resolved but carries no user id is not an identity.
//
// The ownership keys of every per-owner store call are user ids, and an empty
// one means "the row has no owner", never "any owner" — so this shape has to be
// refused once, at the door, instead of being trusted downstream. The rule is
// Principal.Anonymous(), not "is the pointer nil": those differ exactly here.
func TestAuthenticatedUsesTheSameRuleAsAnonymous(t *testing.T) {
	as := func(p *authz.Principal) *http.Request {
		return withPrincipal(httptest.NewRequest(http.MethodGet, "/api/channels", nil), p)
	}
	if authenticated(as(nil)) {
		t.Fatal("a request with no principal is not authenticated")
	}
	if authenticated(as(&authz.Principal{Email: "ghost@example.com", IsOwner: true})) {
		t.Fatal("a principal without a user id is not an identity, owner flag or not")
	}
	if !authenticated(as(reader("u-1"))) {
		t.Fatal("a principal with a user id is authenticated")
	}
}

// The middleware path: a session-less request to a guarded route is 401 with the
// machine readable code, and the handler never runs.
func TestRequireAuthRefusesASessionLessRequest(t *testing.T) {
	d, _ := authTestDeps(t, "")
	d.Channels = newFakeChannels()
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/channels", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d (%s), want 401", w.Code, w.Body.String())
	}
	var body capErrBody
	decodeJSON(t, w, &body)
	if body.Code != "unauthenticated" {
		t.Fatalf("code = %q, want unauthenticated", body.Code)
	}
}

// creatorGrantStore records the grants a registration writes. Embedding
// noopAuthz keeps the rest of the port inert.
type creatorGrantStore struct {
	noopAuthz
	grants []authz.Grant
	err    error
}

func (s *creatorGrantStore) CreateGrant(_ context.Context, g *authz.Grant) error {
	s.grants = append(s.grants, *g)
	return s.err
}

func createAs(d Deps, p *authz.Principal) *httptest.ResponseRecorder {
	// (namespace, name, version) is the dedupe key, so repeated registrations
	// in one test need distinct names.
	name := "ledger" + strings.Repeat("x", len(d.Reg.List("", ""))+1)
	body := `{"kind":"model","name":"` + name + `","version":"1.0.0","visibility":"public","namespace":"default"}`
	r := withPrincipal(httptest.NewRequest(http.MethodPost, "/api/resources", strings.NewReader(body)), p)
	w := httptest.NewRecorder()
	d.handleCreateResource(w, r)
	return w
}

// A key with every scope is still not the owner: without a creator grant its
// own PUT / probe / curation / DELETE would all be answered 404 by
// CanAccessResource, which treats a record with no grants as closed.
func TestCreateResourceGrantsItsCreator(t *testing.T) {
	az := &creatorGrantStore{}
	d, reg := accessDeps(t, az)
	p := &authz.Principal{UserID: "u-1", Permissions: map[string]bool{authz.PermResourcesWrite: true}}

	if w := createAs(d, p); w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want 201", w.Code, w.Body.String())
	}
	if len(az.grants) != 1 {
		t.Fatalf("grants = %+v, want exactly one", az.grants)
	}
	g := az.grants[0]
	list := reg.List("", "")
	if len(list) != 1 || g.ResourceID != list[0].ID || g.PrincipalKind != authz.PrincipalUser ||
		g.PrincipalID != "u-1" || g.Level != authz.LevelWrite {
		t.Fatalf("creator grant = %+v, records = %+v", g, list)
	}

	// Owners bypass grants, anonymous callers have no principal, and an
	// instance without an authz store keeps the pre-identity behaviour.
	before := len(az.grants)
	owner := &authz.Principal{UserID: "owner", IsOwner: true}
	if w := createAs(d, owner); w.Code != http.StatusCreated {
		t.Fatalf("owner create = %d", w.Code)
	}
	if w := createAs(d, nil); w.Code != http.StatusCreated {
		t.Fatalf("anonymous create = %d", w.Code)
	}
	if len(az.grants) != before {
		t.Fatal("an owner or anonymous caller must not produce a grant")
	}
	bare := Deps{Reg: registry.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if w := createAs(bare, p); w.Code != http.StatusCreated {
		t.Fatalf("create without authz = %d", w.Code)
	}
}

// A failed grant must not leave a record the creator cannot manage.
func TestCreateResourceRollsBackWhenCreatorGrantFails(t *testing.T) {
	az := &creatorGrantStore{err: errors.New("grant store down")}
	d, reg := accessDeps(t, az)
	p := &authz.Principal{UserID: "u-1", Permissions: map[string]bool{authz.PermResourcesWrite: true}}

	w := createAs(d, p)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if len(az.grants) != 1 {
		t.Fatalf("the grant was never attempted: %+v", az.grants)
	}
	if _, ok := reg.Get(az.grants[0].ResourceID); ok {
		t.Fatal("the half-created record must be rolled back")
	}
}
