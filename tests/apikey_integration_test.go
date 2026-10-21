package tests

import (
	"context"
	"testing"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/store"
)

// TestIntegrationAPIKeysSchema ensures the api_keys and api_key_scopes tables
// exist after a migration run, so the feature survives a real upgrade.
func TestIntegrationAPIKeysSchema(t *testing.T) {
	db := openDBReal(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"api_keys", "api_key_scopes"} {
		var name string
		if err := db.Raw("SELECT to_regclass($1)::text", table).Scan(&name).Error; err != nil {
			t.Fatalf("look up %s: %v", table, err)
		}
		if name == "" {
			t.Fatalf("table %s missing after migration", table)
		}
	}
}

// TestIntegrationAPIKeyLifecycle exercises the store and the auth resolution
// against PostgreSQL: mint → authenticate → scope intersection → revoke → expire.
func TestIntegrationAPIKeyLifecycle(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	users := store.NewUserStore(db)
	az := authz.New(store.NewAuthzStore(db))
	keys := store.NewAPIKeyStore(db)
	svc := auth.New(users, store.NewSessionStore(db), store.NewOAuthStore(db), keys, az, auth.Options{TTL: time.Hour})

	owner := uniqueEmail("apikey")
	u, err := svc.Register(ctx, owner, "correct horse", "Owner")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	member := uniqueEmail("apikey-member")
	m, err := svc.Register(ctx, member, "correct horse", "Member")
	if err != nil {
		t.Fatalf("register member: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM api_key_scopes WHERE key_id IN (SELECT id FROM api_keys WHERE user_id IN (?, ?))", u.ID, m.ID)
		_ = db.Exec("DELETE FROM api_keys WHERE user_id IN (?, ?)", u.ID, m.ID)
		_ = db.Exec("DELETE FROM sessions WHERE user_id IN (?, ?)", u.ID, m.ID)
	})

	// Mint a key directly through the store, mirroring what the handler does.
	raw, prefix, hash, err := auth.NewAPIKeyToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	key := &auth.APIKey{
		ID:        auth.NewID("key"),
		UserID:    u.ID,
		Name:      "ci-key",
		Prefix:    prefix,
		Scopes:    []string{"resources:read"},
		CreatedBy: u.ID,
	}
	if err := keys.Create(ctx, key, hash); err != nil {
		t.Fatalf("create: %v", err)
	}

	// ByTokenHash finds the active key even though only the hash is stored.
	if got, err := keys.ByTokenHash(ctx, hash); err != nil || got == nil {
		t.Fatalf("by hash: %v %v", got, err)
	}

	// Authenticate resolves to a principal for the owner, scoped to the key.
	p, kid, err := svc.AuthenticateAPIKey(ctx, raw)
	if err != nil {
		t.Fatalf("auth key: %v", err)
	}
	if kid != key.ID {
		t.Fatalf("key id mismatch: got %s want %s", kid, key.ID)
	}
	if p.UserID != u.ID {
		t.Fatalf("principal user mismatch: %s", p.UserID)
	}
	if p.Can("resources:read") != true {
		t.Fatal("key should carry resources:read")
	}
	// A key never rides the owner bypass, even for the instance owner.
	if p.IsOwner {
		t.Fatal("key principal must not be owner")
	}

	// A key for a plain member keeps only what the member actually holds. The
	// default member permission set is users:read + groups:read, so backups:manage
	// must be dropped while the two it does hold survive.
	rawM, prefixM, hashM, _ := auth.NewAPIKeyToken()
	mKey := &auth.APIKey{
		ID:        auth.NewID("key"),
		UserID:    m.ID,
		Name:      "member-key",
		Prefix:    prefixM,
		Scopes:    []string{"users:read", "groups:read", "backups:manage"},
		CreatedBy: u.ID,
	}
	if err := keys.Create(ctx, mKey, hashM); err != nil {
		t.Fatalf("create member key: %v", err)
	}
	pm, _, err := svc.AuthenticateAPIKey(ctx, rawM)
	if err != nil {
		t.Fatalf("auth member key: %v", err)
	}
	if !pm.Can("users:read") || !pm.Can("groups:read") {
		t.Fatal("member key should keep the permissions the member holds")
	}
	if pm.Can("backups:manage") {
		t.Fatal("member key must drop backups:manage the member lacks")
	}

	// Revoked keys stop authenticating, and disappear from Get.
	if err := keys.Revoke(ctx, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := svc.AuthenticateAPIKey(ctx, raw); err != auth.ErrAPIKeyRevoked {
		t.Fatalf("revoked auth: %v, want api_key_revoked", err)
	}
	if got, _ := keys.Get(ctx, key.ID); got != nil {
		t.Fatal("revoked key should be invisible to Get")
	}

	// Expired keys stop authenticating too.
	rawE, prefixE, hashE, _ := auth.NewAPIKeyToken()
	expiry := time.Now().UTC().Add(-time.Hour)
	expKey := &auth.APIKey{
		ID:        auth.NewID("key"),
		UserID:    u.ID,
		Name:      "expired",
		Prefix:    prefixE,
		Scopes:    []string{"resources:read"},
		ExpiresAt: &expiry,
		CreatedBy: u.ID,
	}
	if err := keys.Create(ctx, expKey, hashE); err != nil {
		t.Fatalf("create expired key: %v", err)
	}
	if _, _, err := svc.AuthenticateAPIKey(ctx, rawE); err != auth.ErrAPIKeyExpired {
		t.Fatalf("expired auth: %v, want api_key_expired", err)
	}
}

// TestIntegrationAPIKeyScopeValidation checks the pure rule that scopes must be
// known permissions the caller holds, independent of the database.
func TestIntegrationAPIKeyScopeValidation(t *testing.T) {
	can := func(perm string) bool { return perm == "resources:read" }
	if err := auth.ValidateAPIKeyScopes(nil, can); err != auth.ErrAPIKeyInvalidScope {
		t.Fatalf("empty scopes: %v, want api_key_invalid_scope", err)
	}
	if err := auth.ValidateAPIKeyScopes([]string{"not_a_permission"}, can); err != auth.ErrAPIKeyInvalidScope {
		t.Fatalf("unknown scope: %v, want api_key_invalid_scope", err)
	}
	if err := auth.ValidateAPIKeyScopes([]string{"resources:read", "backups:manage"}, can); err != auth.ErrAPIKeyInvalidScope {
		t.Fatalf("unheld scope: %v, want api_key_invalid_scope", err)
	}
	if err := auth.ValidateAPIKeyScopes([]string{"resources:read"}, can); err != nil {
		t.Fatalf("valid scopes rejected: %v", err)
	}
}
