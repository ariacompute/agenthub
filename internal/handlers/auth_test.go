package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"io"
	"log/slog"

	"agenthub/internal/adapter"
	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

// testLogger keeps the handler logs out of the test output.
func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---------------------------------------------------------------------------
// in-memory stores: the identity layer is exercised through the real HTTP
// surface, but without PostgreSQL, exactly like the rest of the unit tests.
// ---------------------------------------------------------------------------

type memUsers struct {
	mu      sync.Mutex
	byID    map[string]*auth.User
	byEmail map[string]string
}

func newMemUsers() *memUsers {
	return &memUsers{byID: map[string]*auth.User{}, byEmail: map[string]string{}}
}

func (s *memUsers) Create(_ context.Context, u *auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byEmail[u.Email]; exists {
		return fmt.Errorf("23505 duplicate email")
	}
	cp := *u
	s.byID[u.ID] = &cp
	s.byEmail[u.Email] = u.ID
	return nil
}

func (s *memUsers) Get(_ context.Context, id string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

func (s *memUsers) GetByEmail(_ context.Context, email string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byEmail[strings.ToLower(strings.TrimSpace(email))]
	if !ok {
		return nil, nil
	}
	cp := *s.byID[id]
	return &cp, nil
}

func (s *memUsers) Update(_ context.Context, u *auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[u.ID]
	if !ok {
		return nil
	}
	if old.Email != u.Email {
		delete(s.byEmail, old.Email)
		s.byEmail[u.Email] = u.ID
	}
	cp := *u
	s.byID[u.ID] = &cp
	return nil
}

func (s *memUsers) List(_ context.Context, query string) ([]auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []auth.User{}
	q := strings.ToLower(strings.TrimSpace(query))
	for _, u := range s.byID {
		if q == "" || strings.Contains(strings.ToLower(u.Email), q) || strings.Contains(strings.ToLower(u.Name), q) {
			out = append(out, *u)
		}
	}
	return out, nil
}

func (s *memUsers) Count(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.byID)), nil
}

func (s *memUsers) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.byID[id]; ok {
		u.Status = auth.StatusDisabled
		now := time.Now().UTC()
		u.DisabledAt = &now
	}
	return nil
}
func (s *memUsers) RecordLoginFailure(_ context.Context, _ string) error { return nil }
func (s *memUsers) ResetLoginAttempts(_ context.Context, _ string) error { return nil }
func (s *memUsers) LockedUntil(_ context.Context, _ string) (*time.Time, error) {
	return nil, nil
}

type memSessions struct {
	mu      sync.Mutex
	byHash  map[string]*auth.Session
	tokenOf map[string]string
}

func newMemSessions() *memSessions {
	return &memSessions{byHash: map[string]*auth.Session{}, tokenOf: map[string]string{}}
}

func (s *memSessions) Create(_ context.Context, sess *auth.Session, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.byHash[tokenHash] = &cp
	return nil
}

func (s *memSessions) ByTokenHash(_ context.Context, tokenHash string) (*auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byHash[tokenHash]
	if !ok {
		return nil, nil
	}
	cp := *sess
	return &cp, nil
}

func (s *memSessions) Touch(_ context.Context, id string, _ time.Time) error { return nil }

func (s *memSessions) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, sess := range s.byHash {
		if sess.ID == id {
			sess.RevokedAt = &now
		}
	}
	return nil
}

func (s *memSessions) RevokeForUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, sess := range s.byHash {
		if sess.UserID == userID && sess.RevokedAt == nil {
			sess.RevokedAt = &now
		}
	}
	return nil
}

func (s *memSessions) ListForUser(_ context.Context, userID string) ([]auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []auth.Session{}
	for _, sess := range s.byHash {
		if sess.UserID == userID {
			out = append(out, *sess)
		}
	}
	return out, nil
}

func (s *memSessions) DeleteExpired(_ context.Context, now time.Time) error { return nil }

// noopAuthz is the inert half of the authorization port: a test only overrides
// the lookups it cares about.
type noopAuthz struct{}

func (noopAuthz) GroupsOfUser(context.Context, string) ([]string, error) { return nil, nil }
func (noopAuthz) RolesOfUser(context.Context, string) ([]string, error)  { return nil, nil }
func (noopAuthz) PermissionsOfRoles(context.Context, []string) ([]string, error) {
	return nil, nil
}
func (noopAuthz) GrantsForResource(context.Context, string) ([]authz.Grant, error) { return nil, nil }
func (noopAuthz) GrantsForPrincipal(context.Context, string, []string) ([]authz.Grant, error) {
	return nil, nil
}
func (noopAuthz) RestrictedResourceIDs(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}
func (noopAuthz) ListPermissions(context.Context) ([]authz.Permission, error) { return nil, nil }
func (noopAuthz) ListRoles(context.Context) ([]authz.Role, error)             { return nil, nil }
func (noopAuthz) CreateRole(context.Context, *authz.Role) error               { return nil }
func (noopAuthz) UpdateRole(context.Context, *authz.Role) error               { return nil }
func (noopAuthz) DeleteRole(context.Context, string) error                    { return nil }
func (noopAuthz) SetRolePermissions(context.Context, string, []string) error  { return nil }
func (noopAuthz) ListGroups(context.Context) ([]authz.Group, error)           { return nil, nil }
func (noopAuthz) CreateGroup(context.Context, *authz.Group) error             { return nil }
func (noopAuthz) UpdateGroup(context.Context, *authz.Group) error             { return nil }
func (noopAuthz) DeleteGroup(context.Context, string) error                   { return nil }
func (noopAuthz) SetUserRoles(context.Context, string, []string) error        { return nil }
func (noopAuthz) SetUserGroups(context.Context, string, []string) error       { return nil }
func (noopAuthz) ListGrants(context.Context, string) ([]authz.Grant, error)   { return nil, nil }
func (noopAuthz) CreateGrant(context.Context, *authz.Grant) error             { return nil }
func (noopAuthz) DeleteGrant(context.Context, string) error                   { return nil }

// memAuthz resolves roles and grants from plain maps.
type memAuthz struct {
	noopAuthz
	mu       sync.Mutex
	roles    map[string][]string // user → roles
	rolePerm map[string][]string // role → permissions
	grants   []authz.Grant
}

func newMemAuthz() *memAuthz {
	return &memAuthz{roles: map[string][]string{}, rolePerm: map[string][]string{}}
}

func (s *memAuthz) RolesOfUser(_ context.Context, userID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.roles[userID]...), nil
}

func (s *memAuthz) PermissionsOfRoles(_ context.Context, roleIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, r := range roleIDs {
		out = append(out, s.rolePerm[r]...)
	}
	return out, nil
}

func (s *memAuthz) GrantsForResource(_ context.Context, resourceID string) ([]authz.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []authz.Grant{}
	for _, g := range s.grants {
		if g.ResourceID == resourceID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *memAuthz) GrantsForPrincipal(_ context.Context, userID string, groups []string) ([]authz.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []authz.Grant{}
	for _, g := range s.grants {
		if g.PrincipalKind == authz.PrincipalUser && g.PrincipalID == userID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *memAuthz) RestrictedResourceIDs(_ context.Context, ids []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for _, g := range s.grants {
		for _, id := range ids {
			if g.ResourceID == id {
				out[id] = true
			}
		}
	}
	return out, nil
}

func (s *memAuthz) ListRoles(context.Context) ([]authz.Role, error) { return []authz.Role{}, nil }
func (s *memAuthz) ListGroups(context.Context) ([]authz.Group, error) {
	return []authz.Group{}, nil
}
func (s *memAuthz) ListGrants(_ context.Context, resourceID string) ([]authz.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []authz.Grant{}
	for _, g := range s.grants {
		if resourceID == "" || g.ResourceID == resourceID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *memAuthz) CreateGrant(_ context.Context, g *authz.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.ID == "" {
		g.ID = "grant-" + g.ResourceID + "-" + g.PrincipalID
	}
	s.grants = append(s.grants, *g)
	return nil
}

func (s *memAuthz) DeleteGrant(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.grants[:0:0]
	for _, g := range s.grants {
		if g.ID != id {
			kept = append(kept, g)
		}
	}
	s.grants = kept
	return nil
}

func (s *memAuthz) grant(userID, resourceID, level string) {
	s.CreateGrant(context.Background(), &authz.Grant{
		ID:            "grant-" + resourceID + "-" + userID,
		ResourceID:    resourceID,
		PrincipalKind: authz.PrincipalUser,
		PrincipalID:   userID,
		Level:         level,
	})
}

// authTestDeps wires a real identity stack over in-memory stores, which is the
// closest a unit test gets to a running agenthub.
func authTestDeps(t *testing.T, staticDir string, entries ...*registry.Entry) (Deps, *memAuthz) {
	t.Helper()
	cfg := openConfig(staticDir)
	cfg.SessionTTL = time.Hour
	cfg.CookieSecure = false
	reg := registry.New()
	for _, e := range entries {
		stored, err := reg.Upsert(e)
		if err != nil {
			t.Fatalf("seed %q: %v", e.Name, err)
		}
		*e = *stored // the caller holds the id it will grant against
	}
	h := hub.New(cfg, reg, adapter.Options{
		CLITimeout:     time.Second,
		CLIAllowlist:   []string{"echo"},
		MaxOutputBytes: 1024,
	}, &hub.MockNotifier{}, hub.Persistence{})
	az := newMemAuthz()
	svc := auth.New(newMemUsers(), newMemSessions(), nil, nil, authz.New(az), auth.Options{
		CookieName: "agenthub_session",
		TTL:        cfg.SessionTTL,
	})
	return Deps{
		Cfg:   cfg,
		Hub:   h,
		Reg:   reg,
		Log:   testLogger(),
		Auth:  svc,
		Authz: authz.New(az),
	}, az
}

// sessionOf logs in over HTTP and returns the session cookie, so the tests
// exercise the real cookie contract instead of reaching into the service.
func sessionOf(t *testing.T, h http.Handler, email, password string) string {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	w := do(t, h, http.MethodPost, "/api/auth/login", body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login %q: status = %d, body = %s", email, w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "agenthub_session" && c.Value != "" {
			if !c.HttpOnly {
				t.Fatal("session cookie must be HttpOnly")
			}
			return c.Value
		}
	}
	t.Fatalf("login %q set no session cookie: %s", email, w.Body.String())
	return ""
}

func authHeader(token string) map[string]string {
	return map[string]string{"X-AgentHub-Session": token}
}

// ---------------------------------------------------------------------------
// registration and login
// ---------------------------------------------------------------------------

func TestRegisterThenLogin(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)

	// The first account of an instance is the owner: without it a fresh
	// deployment would have nobody able to grant permissions back.
	w := do(t, h, http.MethodPost, "/api/auth/register", `{"email":"Owner@Example.com","password":"correct horse","name":"Owner"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		User struct {
			ID      string `json:"id"`
			Email   string `json:"email"`
			IsOwner bool   `json:"isOwner"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	if created.User.Email != "owner@example.com" {
		t.Fatalf("email = %q, want normalised", created.User.Email)
	}
	if !created.User.IsOwner {
		t.Fatal("first registered account must be the owner")
	}

	// The same address twice is a conflict, not a silent overwrite.
	if w := do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil); w.Code != http.StatusConflict {
		t.Fatalf("duplicate register: status = %d, want 409", w.Code)
	}

	for _, tc := range []struct{ name, body, code string }{
		{"malformed email", `{"email":"nope","password":"correct horse"}`, "invalid_email"},
		{"short password", `{"email":"a@example.com","password":"short"}`, "weak_password"},
	} {
		w := do(t, h, http.MethodPost, "/api/auth/register", tc.body, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, w.Code)
		}
		var err struct {
			Code string `json:"code"`
		}
		decodeJSON(t, w, &err)
		if err.Code != tc.code {
			t.Fatalf("%s: code = %q, want %q", tc.name, err.Code, tc.code)
		}
	}

	token := sessionOf(t, h, "owner@example.com", "correct horse")
	if w := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(token)); w.Code != http.StatusOK {
		t.Fatalf("me: status = %d, want 200", w.Code)
	}
	// A wrong password must not produce a session.
	if w := do(t, h, http.MethodPost, "/api/auth/login", `{"email":"owner@example.com","password":"wrong password"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", w.Code)
	}
}

// TestControlPlaneRequiresSession is the regression guard for the removed admin
// token: no cookie means no access, and the code on the wire is stable.
func TestControlPlaneRequiresSession(t *testing.T) {
	d, _ := authTestDeps(t, "", &registry.Entry{Kind: registry.KindModel, Name: "m1"})
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/resources", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", w.Code)
	}
	var err struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &err)
	if err.Code != "unauthenticated" {
		t.Fatalf("code = %q, want unauthenticated", err.Code)
	}

	// Discovery stays public: an agent that has never logged in must still be
	// able to find the hub.
	if w := do(t, h, http.MethodGet, "/.well-known/agent.json", "", nil); w.Code != http.StatusOK {
		t.Fatalf("agent card: status = %d, want 200", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/healthz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("healthz: status = %d, want 200", w.Code)
	}

	// Registering the owner is enough to reach the control plane: the owner
	// holds every permission implicitly.
	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	token := sessionOf(t, h, "owner@example.com", "correct horse")
	if w := do(t, h, http.MethodGet, "/api/resources", "", authHeader(token)); w.Code != http.StatusOK {
		t.Fatalf("owner: status = %d, want 200", w.Code)
	}
}

// TestPermissionGateIsNotRoleAware pins the vocabulary: a route asks for one
// permission, never for a role, so a new role can be created without touching
// the handlers.
func TestPermissionGateIsNotRoleAware(t *testing.T) {
	d, az := authTestDeps(t, "", &registry.Entry{Kind: registry.KindModel, Name: "m1"})
	h := Register(d)

	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	owner := sessionOf(t, h, "owner@example.com", "correct horse")

	// A second account has no roles at all, so it has no permissions.
	w := do(t, h, http.MethodPost, "/api/users", `{"email":"viewer@example.com","password":"correct horse","name":"V"}`, authHeader(owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	viewer := sessionOf(t, h, "viewer@example.com", "correct horse")

	if w := do(t, h, http.MethodGet, "/api/resources", "", authHeader(viewer)); w.Code != http.StatusForbidden {
		t.Fatalf("no permissions: status = %d, want 403", w.Code)
	}
	if w := do(t, h, http.MethodPost, "/api/resources", `{"kind":"model","name":"x"}`, authHeader(viewer)); w.Code != http.StatusForbidden {
		t.Fatalf("write without permission: status = %d, want 403", w.Code)
	}

	// Give the viewer role its permissions and the same request passes.
	az.mu.Lock()
	az.roles[created.User.ID] = []string{"role-viewer"}
	az.rolePerm["role-viewer"] = []string{authz.PermResourcesRead}
	az.mu.Unlock()
	if w := do(t, h, http.MethodGet, "/api/resources", "", authHeader(viewer)); w.Code != http.StatusOK {
		t.Fatalf("with resources:read: status = %d, want 200", w.Code)
	}
	if w := do(t, h, http.MethodPost, "/api/resources", `{"kind":"model","name":"x"}`, authHeader(viewer)); w.Code != http.StatusForbidden {
		t.Fatalf("still no resources:write: status = %d, want 403", w.Code)
	}
}

// TestLogoutRevokesTheSession is the reason sessions live in a table: after a
// logout the very next request must fail, not when the cookie expires.
func TestLogoutRevokesTheSession(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)
	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	token := sessionOf(t, h, "owner@example.com", "correct horse")

	if w := do(t, h, http.MethodPost, "/api/auth/logout", "", authHeader(token)); w.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(token)); w.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: status = %d, want 401", w.Code)
	}
}

// TestResourceGrantHidesAndProtects covers the resource level authorization:
// a granted resource disappears from listings, exports and the Agent Card, and
// a direct write to it is refused even though the caller has resources:write.
func TestResourceGrantHidesAndProtects(t *testing.T) {
	open := &registry.Entry{Kind: registry.KindModel, Name: "open-model"}
	secret := &registry.Entry{Kind: registry.KindMCP, Name: "team-mcp"}
	d, az := authTestDeps(t, "", open, secret)
	h := Register(d)

	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	owner := sessionOf(t, h, "owner@example.com", "correct horse")
	w := do(t, h, http.MethodPost, "/api/users", `{"email":"member@example.com","password":"correct horse","name":"M"}`, authHeader(owner))
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	member := sessionOf(t, h, "member@example.com", "correct horse")
	az.mu.Lock()
	az.roles[created.User.ID] = []string{"role-viewer"}
	az.rolePerm["role-viewer"] = []string{authz.PermResourcesRead, authz.PermResourcesWrite}
	az.mu.Unlock()

	// Before any grant exists the resource is unrestricted: everybody with
	// resources:read sees it.
	if w := do(t, h, http.MethodGet, "/api/resources", "", authHeader(member)); w.Code != http.StatusOK {
		t.Fatalf("list: status = %d", w.Code)
	} else if !strings.Contains(w.Body.String(), secret.ID) {
		t.Fatal("unrestricted resource must be listed")
	}

	// One grant turns it into a restricted resource.
	az.grant("somebody-else", secret.ID, authz.LevelRead)

	w = do(t, h, http.MethodGet, "/api/resources", "", authHeader(member))
	var listed struct {
		Resources []struct {
			ID string `json:"id"`
		} `json:"resources"`
		Counts map[string]int `json:"counts"`
	}
	decodeJSON(t, w, &listed)
	for _, r := range listed.Resources {
		if r.ID == secret.ID {
			t.Fatal("resource granted to somebody else must not be listed")
		}
	}
	if listed.Counts["mcp"] != 0 {
		t.Fatalf("counts leak the hidden resource: %v", listed.Counts)
	}
	// The counts of what the caller cannot see must not add up to more than
	// what it can.
	if len(listed.Resources) != 1 {
		t.Fatalf("visible resources = %d, want 1", len(listed.Resources))
	}

	// A direct read is refused with 404 so the existence of the id stays a
	// secret, and a write is refused even with resources:write.
	if w := do(t, h, http.MethodGet, "/api/resources/"+secret.ID, "", authHeader(member)); w.Code != http.StatusNotFound {
		t.Fatalf("direct read: status = %d, want 404", w.Code)
	}
	if w := do(t, h, http.MethodDelete, "/api/resources/"+secret.ID, "", authHeader(member)); w.Code != http.StatusNotFound {
		t.Fatalf("direct delete: status = %d, want 404", w.Code)
	}
	// The Agent Card is discovery, so it must filter too.
	card := do(t, h, http.MethodGet, "/.well-known/agent.json", "", authHeader(member))
	if strings.Contains(card.Body.String(), secret.Name) {
		t.Fatal("agent card must not advertise a resource the caller cannot read")
	}

	// Granting it to this user makes it appear again.
	az.grant(created.User.ID, secret.ID, authz.LevelRead)
	w = do(t, h, http.MethodGet, "/api/resources", "", authHeader(member))
	decodeJSON(t, w, &listed)
	if len(listed.Resources) != 2 {
		t.Fatalf("after grant: visible = %d, want 2", len(listed.Resources))
	}
	// read does not imply write: the delete stays refused.
	if w := do(t, h, http.MethodDelete, "/api/resources/"+secret.ID, "", authHeader(member)); w.Code != http.StatusNotFound {
		t.Fatalf("read grant must not allow delete: status = %d", w.Code)
	}
}

// TestAdminEndpointsNeedUsersManage guards the admin screen itself.
func TestAdminEndpointsNeedUsersManage(t *testing.T) {
	d, az := authTestDeps(t, "")
	h := Register(d)
	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	owner := sessionOf(t, h, "owner@example.com", "correct horse")

	w := do(t, h, http.MethodPost, "/api/users", `{"email":"op@example.com","password":"correct horse","name":"Op"}`, authHeader(owner))
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	op := sessionOf(t, h, "op@example.com", "correct horse")

	if w := do(t, h, http.MethodGet, "/api/users", "", authHeader(op)); w.Code != http.StatusForbidden {
		t.Fatalf("users:manage missing: status = %d, want 403", w.Code)
	}
	az.mu.Lock()
	az.roles[created.User.ID] = []string{"role-operator"}
	az.rolePerm["role-operator"] = []string{authz.PermUsersManage}
	az.mu.Unlock()
	if w := do(t, h, http.MethodGet, "/api/users", "", authHeader(op)); w.Code != http.StatusOK {
		t.Fatalf("with users:manage: status = %d, want 200", w.Code)
	}
	// One permission is not the whole set: the matrix has to be granted.
	if w := do(t, h, http.MethodGet, "/api/roles", "", authHeader(op)); w.Code != http.StatusForbidden {
		t.Fatalf("roles:manage must stay separate: status = %d, want 403", w.Code)
	}
}

// TestUserLifecycle walks the admin CRUD: create, update, disable and the
// refusal to disable the owner.
func TestUserLifecycle(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)
	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	owner := sessionOf(t, h, "owner@example.com", "correct horse")

	w := do(t, h, http.MethodPost, "/api/users", `{"email":"u@example.com","password":"correct horse","name":"U"}`, authHeader(owner))
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	id := created.User.ID

	if w := do(t, h, http.MethodGet, "/api/users?q=example", "", authHeader(owner)); w.Code != http.StatusOK {
		t.Fatalf("search: status = %d", w.Code)
	} else if !strings.Contains(w.Body.String(), id) {
		t.Fatal("search must find the user by address")
	}

	w = do(t, h, http.MethodPut, "/api/users/"+id, `{"name":"Renamed","status":"disabled"}`, authHeader(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", w.Code, w.Body.String())
	}
	var updated struct {
		User struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"user"`
	}
	decodeJSON(t, w, &updated)
	if updated.User.Name != "Renamed" || updated.User.Status != auth.StatusDisabled {
		t.Fatalf("updated = %+v", updated.User)
	}
	// A disabled user cannot log in any more, and its sessions are gone.
	if w := do(t, h, http.MethodPost, "/api/auth/login", `{"email":"u@example.com","password":"correct horse"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("disabled login: status = %d, want 403", w.Code)
	}

	if w := do(t, h, http.MethodDelete, "/api/users/"+id, "", authHeader(owner)); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", w.Code)
	}
	// The owner is the safety net: it cannot be disabled through the API.
	me := do(t, h, http.MethodGet, "/api/auth/me", "", authHeader(owner))
	var who struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, me, &who)
	if w := do(t, h, http.MethodDelete, "/api/users/"+who.User.ID, "", authHeader(owner)); w.Code != http.StatusBadRequest {
		t.Fatalf("delete owner: status = %d, want 400", w.Code)
	}
}

// TestHealthzReportsAuthMode keeps the /healthz contract stable for the console
// and for operators debugging a login loop.
func TestHealthzReportsAuthMode(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)

	var body struct {
		Auth string `json:"auth"`
	}
	decodeJSON(t, do(t, h, http.MethodGet, "/healthz", "", nil), &body)
	if body.Auth != "anonymous" {
		t.Fatalf("auth = %q, want anonymous", body.Auth)
	}

	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	token := sessionOf(t, h, "owner@example.com", "correct horse")
	decodeJSON(t, do(t, h, http.MethodGet, "/healthz", "", authHeader(token)), &body)
	if body.Auth != "session" {
		t.Fatalf("auth = %q, want session", body.Auth)
	}

	// Without an identity layer at all (unit test / in-memory mode) the old
	// open behaviour is reported honestly instead of pretending a session.
	open := Register(testDeps(t, openConfig(t.TempDir())))
	decodeJSON(t, do(t, open, http.MethodGet, "/healthz", "", nil), &body)
	if body.Auth != "disabled" {
		t.Fatalf("auth = %q, want disabled", body.Auth)
	}
}

// TestOAuthProviderNotConfigured pins the "no credentials, no 500" rule: an
// instance without OAuth simply has no such login button.
func TestOAuthProviderNotConfigured(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/auth/config", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("config: status = %d", w.Code)
	}
	var cfg struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	decodeJSON(t, w, &cfg)
	if len(cfg.Providers) != 0 {
		t.Fatalf("providers = %v, want none when nothing is configured", cfg.Providers)
	}

	w = do(t, h, http.MethodGet, "/api/auth/oauth/google", "", nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("unconfigured provider: status = %d, want 502", w.Code)
	}
	var got struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	if got.Code != "oauth_not_configured" {
		t.Fatalf("code = %q, want oauth_not_configured", got.Code)
	}
}
