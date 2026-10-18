package tests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/store"

	"gorm.io/gorm"
)

// TestIntegrationAuthSchemaIsIdempotent runs the whole migration set twice: an
// existing installation must survive the upgrade to 005/006 without a manual
// step, and every table the identity layer needs must be there afterwards.
func TestIntegrationAuthSchemaIsIdempotent(t *testing.T) {
	db := openDBReal(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	for _, table := range []string{
		"users", "sessions", "oauth_accounts",
		"groups", "roles", "permissions", "role_permissions",
		"user_groups", "user_roles", "group_roles", "resource_grants",
	} {
		var name string
		if err := db.Raw("SELECT to_regclass($1)::text", table).Scan(&name).Error; err != nil {
			t.Fatalf("look up %s: %v", table, err)
		}
		if name == "" {
			t.Fatalf("table %s is missing after migration", table)
		}
	}
}

// TestIntegrationUserLifecycle walks the identity path against PostgreSQL:
// register → login → authenticate → logout, plus the uniqueness constraint
// that makes an address a real identity.
func TestIntegrationUserLifecycle(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	users := store.NewUserStore(db)
	sessions := store.NewSessionStore(db)
	az := authz.New(store.NewAuthzStore(db))
	svc := auth.New(users, sessions, store.NewOAuthStore(db), az, auth.Options{TTL: time.Hour})

	email := uniqueEmail("lifecycle")
	u, err := svc.Register(ctx, email, "correct horse", "Lifecycle")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.ID == "" {
		t.Fatal("register returned no id")
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM sessions WHERE user_id = ?", u.ID) })

	// The unique index is what stops one person from holding two accounts
	// under the same address in a different case.
	if _, err := svc.Register(ctx, strings.ToUpper(email), "correct horse", ""); err != auth.ErrEmailTaken {
		t.Fatalf("duplicate register: %v, want email_taken", err)
	}

	_, token, created, err := svc.Login(ctx, email, "correct horse", "curl", "127.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token == "" || created == nil {
		t.Fatalf("login returned %q / %v", token, created)
	}
	p, sess, err := svc.Authenticate(ctx, token)
	if err != nil || p == nil || sess == nil {
		t.Fatalf("authenticate: %v (p=%v sess=%v)", err, p, sess)
	}
	if p.UserID != u.ID {
		t.Fatalf("resolved user = %q, want %q", p.UserID, u.ID)
	}

	// Only the hash of the token is stored: the secret itself is not a key.
	byRaw, err := sessions.ByTokenHash(ctx, token)
	if err != nil {
		t.Fatalf("lookup by raw token: %v", err)
	}
	if byRaw != nil {
		t.Fatal("the raw token must not resolve to a session")
	}

	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, token); err != auth.ErrSessionExpired {
		t.Fatalf("after logout: %v, want session_expired", err)
	}

	// The bootstrap administrator is upserted, never duplicated.
	admin := uniqueEmail("bootstrap")
	if err := svc.BootstrapAdmin(ctx, admin, "correct horse", "Admin"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := svc.BootstrapAdmin(ctx, admin, "correct horse", "Admin"); err != nil {
		t.Fatalf("bootstrap twice: %v", err)
	}
	got, err := users.GetByEmail(ctx, admin)
	if err != nil || got == nil || !got.IsOwner {
		t.Fatalf("bootstrap admin = %v (err %v)", got, err)
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM sessions WHERE user_id = ?", got.ID) })
}

// TestIntegrationRBACResolution proves the chain user → group → role →
// permission resolves in SQL, and that the seeded roles carry what the
// migration claims.
func TestIntegrationRBACResolution(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	users := store.NewUserStore(db)
	azs := store.NewAuthzStore(db)
	az := authz.New(azs)
	svc := auth.New(users, store.NewSessionStore(db), store.NewOAuthStore(db), az, auth.Options{TTL: time.Hour})

	roles, err := azs.ListRoles(ctx)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) < 3 {
		t.Fatalf("seeded roles = %d, want at least the three built-ins", len(roles))
	}
	byName := map[string]authz.Role{}
	for _, r := range roles {
		byName[r.Name] = r
	}
	for _, want := range []string{"viewer", "operator", "admin"} {
		r, ok := byName[want]
		if !ok {
			t.Fatalf("seeded role %q is missing", want)
		}
		if !r.Builtin {
			t.Fatalf("role %q must be built-in so the console cannot delete it", want)
		}
		if len(r.Permissions) == 0 {
			t.Fatalf("role %q was seeded without permissions", want)
		}
	}

	role := &authz.Role{Name: "integration-" + time.Now().Format("150405.000000"), Permissions: []string{authz.PermTasksRead}}
	if err := azs.CreateRole(ctx, role); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() { _ = azs.DeleteRole(context.Background(), role.ID) })

	if err := azs.CreateRole(ctx, &authz.Role{Name: role.Name}); !errors.Is(err, authz.ErrRoleExists) {
		t.Fatalf("duplicate role: %v, want role exists", err)
	}
	// An unknown permission must be refused: the FK is the last line of
	// defence, but the store rejects it before it ever reaches SQL.
	if err := azs.CreateRole(ctx, &authz.Role{Name: role.Name + "-bad", Permissions: []string{"not:a:permission"}}); !errors.Is(err, authz.ErrUnknownPermission) {
		t.Fatalf("unknown permission: %v, want unknown permission", err)
	}

	group := &authz.Group{Name: "integration-" + time.Now().Format("150405.000000"), Roles: []string{role.ID}}
	if err := azs.CreateGroup(ctx, group); err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() { _ = azs.DeleteGroup(context.Background(), group.ID) })

	u, err := svc.Register(ctx, uniqueEmail("rbac"), "correct horse", "RBAC")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM user_groups WHERE user_id = ?", u.ID)
		_ = db.Exec("DELETE FROM user_roles WHERE user_id = ?", u.ID)
	})

	if err := azs.SetUserGroups(ctx, u.ID, []string{group.ID}); err != nil {
		t.Fatalf("set groups: %v", err)
	}
	// A missing group must not silently grant nothing: it is a bug in the
	// caller, and the admin screen needs to hear about it.
	if err := azs.SetUserGroups(ctx, u.ID, []string{"group-nope"}); !errors.Is(err, authz.ErrGroupNotFound) {
		t.Fatalf("unknown group: %v, want group not found", err)
	}
	if err := azs.SetUserRoles(ctx, u.ID, []string{role.ID}); err != nil {
		t.Fatalf("set roles: %v", err)
	}

	p, err := az.Load(ctx, u.ID, u.Email, u.Name, false)
	if err != nil {
		t.Fatalf("load principal: %v", err)
	}
	if !p.Can(authz.PermTasksRead) {
		t.Fatalf("the role granted through the group did not resolve: %v", p.Permissions)
	}
	if p.Can(authz.PermUsersManage) {
		t.Fatal("a role must not grant permissions it does not carry")
	}
	groups, err := azs.GroupsOfUser(ctx, u.ID)
	if err != nil || len(groups) != 1 || groups[0] != group.ID {
		t.Fatalf("groups of user = %v (err %v)", groups, err)
	}
}

// TestIntegrationResourceGrantRestricts covers the resource level rule in SQL:
// a granted resource is hidden from listings and a read grant never allows a
// write, while the owner stays above all of it.
func TestIntegrationResourceGrantRestricts(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	azs := store.NewAuthzStore(db)
	az := authz.New(azs)

	public := integrationResource(t, db, "model", "integration-public")
	secret := integrationResource(t, db, "model", "integration-secret")
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM resource_grants WHERE resource_id IN (?,?)", public, secret)
		_ = db.Exec("DELETE FROM resources WHERE id IN (?,?)", public, secret)
	})

	// The grant holder has to be a real row: a grant to a user that does not
	// exist would restrict a resource nobody could ever release.
	holder, err := svcHolder(t, db).Register(ctx, uniqueEmail("grant-holder"), "correct horse", "Holder")
	if err != nil {
		t.Fatalf("register grant holder: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM resource_grants WHERE principal_id = ?", holder.ID) })

	grant := &authz.Grant{
		ResourceID:    secret,
		PrincipalKind: authz.PrincipalUser,
		PrincipalID:   holder.ID,
		Level:         authz.LevelRead,
	}
	if err := azs.CreateGrant(ctx, grant); err != nil {
		t.Fatalf("create grant: %v", err)
	}

	p := &authz.Principal{UserID: "somebody-else", Permissions: map[string]bool{authz.PermResourcesRead: true}}
	visible, err := az.VisibleResourceIDs(ctx, p, []string{public, secret})
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if _, ok := visible[public]; !ok {
		t.Fatal("an unrestricted resource must stay visible")
	}
	if _, ok := visible[secret]; ok {
		t.Fatal("a resource granted to somebody else must not be visible")
	}
	if ok, err := az.CanAccessResource(ctx, p, secret, true); err != nil || ok {
		t.Fatalf("a read grant must not allow writes (ok=%v err=%v)", ok, err)
	}

	// The grant holder itself may read, and the owner bypasses everything.
	holderPrincipal := &authz.Principal{UserID: grant.PrincipalID, Permissions: map[string]bool{authz.PermResourcesRead: true}}
	if ok, err := az.CanAccessResource(ctx, holderPrincipal, secret, false); err != nil || !ok {
		t.Fatalf("the grant holder must be able to read (ok=%v err=%v)", ok, err)
	}
	owner := &authz.Principal{UserID: "owner", IsOwner: true}
	if ok, err := az.CanAccessResource(ctx, owner, secret, true); err != nil || !ok {
		t.Fatalf("the owner must bypass grants (ok=%v err=%v)", ok, err)
	}

	// A grant pointing at a principal that does not exist would restrict a
	// resource that nobody could ever make public again.
	if err := azs.CreateGrant(ctx, &authz.Grant{
		ResourceID:    public,
		PrincipalKind: authz.PrincipalGroup,
		PrincipalID:   "group-nope",
		Level:         authz.LevelRead,
	}); err != authz.ErrGroupNotFound {
		t.Fatalf("grant to a missing principal: %v, want group not found", err)
	}

	// Revoking the grant releases the resource again.
	if err := azs.DeleteGrant(ctx, grant.ID); err != nil {
		t.Fatalf("delete grant: %v", err)
	}
	if ok, err := az.CanAccessResource(ctx, p, secret, false); err != nil || !ok {
		t.Fatalf("after revoke the resource must be public again (ok=%v err=%v)", ok, err)
	}
}

// svcHolder is a minimal identity service for tests that only need to create a
// user row on the real database.
func svcHolder(t *testing.T, db *gorm.DB) *auth.Service {
	t.Helper()
	return auth.New(store.NewUserStore(db), store.NewSessionStore(db), store.NewOAuthStore(db), nil,
		auth.Options{TTL: time.Hour})
}

// uniqueEmail keeps parallel runs from colliding on the unique index.
func uniqueEmail(prefix string) string {
	return prefix + "-" + time.Now().Format("150405.000000") + "@example.com"
}

// integrationResource inserts a registry row directly, so the grant tests have
// a real resource id to point at.
func integrationResource(t *testing.T, db *gorm.DB, kind, name string) string {
	t.Helper()
	id := name + "-" + time.Now().Format("150405.000000")
	now := time.Now().UTC()
	if err := db.Exec(
		`INSERT INTO resources (id, kind, name, description, health, tags, capabilities, auth_schemes, env, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'unknown', '[]', '[]', '[]', '{}', '{}', ?, ?) ON CONFLICT (id) DO NOTHING`,
		id, kind, name, now, now,
	).Error; err != nil {
		t.Fatalf("insert resource %s: %v", id, err)
	}
	return id
}
