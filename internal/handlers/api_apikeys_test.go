package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"agenthub/internal/auth"
)

// memAPIKeys is an in-memory auth.APIKeyStore so the handler package can exercise
// every path without a database — the same shape the GORM store provides.
type memAPIKeys struct {
	mu     sync.Mutex
	byID   map[string]*auth.APIKey
	byHash map[string]*auth.APIKey
}

func newMemAPIKeys() *memAPIKeys {
	return &memAPIKeys{byID: map[string]*auth.APIKey{}, byHash: map[string]*auth.APIKey{}}
}

func cloneKey(k *auth.APIKey) *auth.APIKey {
	cp := *k
	cp.Scopes = append([]string(nil), k.Scopes...)
	return &cp
}

func (m *memAPIKeys) Create(ctx context.Context, k *auth.APIKey, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := cloneKey(k)
	m.byID[k.ID] = cp
	m.byHash[tokenHash] = cp
	return nil
}

func (m *memAPIKeys) Get(ctx context.Context, id string) (*auth.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.byID[id]
	if !ok || k.RevokedAt != nil {
		return nil, nil
	}
	return cloneKey(k), nil
}

func (m *memAPIKeys) ByTokenHash(ctx context.Context, tokenHash string) (*auth.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.byHash[tokenHash]
	if !ok {
		return nil, nil
	}
	return cloneKey(k), nil
}

func (m *memAPIKeys) ListForUser(ctx context.Context, userID string) ([]auth.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.APIKey
	for _, k := range m.byID {
		if k.UserID == userID && k.RevokedAt == nil {
			out = append(out, *cloneKey(k))
		}
	}
	return out, nil
}

func (m *memAPIKeys) ListAll(ctx context.Context) ([]auth.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.APIKey
	for _, k := range m.byID {
		if k.RevokedAt == nil {
			out = append(out, *cloneKey(k))
		}
	}
	return out, nil
}

func (m *memAPIKeys) SetScopes(ctx context.Context, id string, scopes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.byID[id]; ok {
		k.Scopes = append([]string(nil), scopes...)
	}
	return nil
}

func (m *memAPIKeys) Update(ctx context.Context, k *auth.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.byID[k.ID]; ok {
		cur.Name = k.Name
		cur.ExpiresAt = k.ExpiresAt
	}
	return nil
}

func (m *memAPIKeys) Revoke(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.byID[id]; ok {
		now := time.Now().UTC()
		k.RevokedAt = &now
	}
	return nil
}

func (m *memAPIKeys) Touch(ctx context.Context, id string, at time.Time) error { return nil }

// keyHarness wires an in-memory users/sessions pair plus the fake key store and
// returns a handler with an owner and a plain user already logged in.
type keyHarness struct {
	t      *testing.T
	h      http.Handler
	fake   *memAPIKeys
	owner  string
	user   string
	ownerC string
	userC  string
}

func newKeyHarness(t *testing.T) *keyHarness {
	t.Helper()
	fake := newMemAPIKeys()
	users := newMemUsers()
	sessions := newMemSessions()
	svc := auth.New(users, sessions, nil, fake, nil, auth.Options{
		CookieName: "agenthub_session",
		TTL:        time.Hour,
	})
	d := Deps{Cfg: openConfig(t.TempDir()), Auth: svc, Log: testLogger()}
	h := Register(d)
	ctx := context.Background()
	// First registration becomes the owner, so it holds every permission.
	owner, err := svc.Register(ctx, "owner@agenthub.test", "correct horse", "Owner")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	user, err := svc.Register(ctx, "user@agenthub.test", "correct horse", "User")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	return &keyHarness{
		t:      t,
		h:      h,
		fake:   fake,
		owner:  owner.ID,
		user:   user.ID,
		ownerC: sessionOf(t, h, "owner@agenthub.test", "correct horse"),
		userC:  sessionOf(t, h, "user@agenthub.test", "correct horse"),
	}
}

// apiKeyResponse is the create payload: the key view plus the one-time token.
type apiKeyResponse struct {
	APIKey apiKeyView `json:"apiKey"`
	Token  string     `json:"token"`
}

func decodeKey(t *testing.T, body string) apiKeyResponse {
	t.Helper()
	var resp apiKeyResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	return resp
}

func TestAPIKeyCreateAndAuthenticate(t *testing.T) {
	k := newKeyHarness(t)

	// A plain user cannot grant a permission it does not hold: with no authz
	// service the non-owner has an empty permission set, so any scope is too much.
	body := `{"name":"mine","scopes":["tasks:read"]}`
	w := do(k.t, k.h, http.MethodPost, "/api/api-keys", body, authHeader(k.userC))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("user with no perms create: status = %d, body = %s", w.Code, w.Body.String())
	}

	// The owner can mint a key with an explicit scope subset.
	body = `{"name":"owner-key","scopes":["resources:read","tasks:write"]}`
	w = do(k.t, k.h, http.MethodPost, "/api/api-keys", body, authHeader(k.ownerC))
	if w.Code != http.StatusCreated {
		t.Fatalf("owner create: status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeKey(k.t, w.Body.String())
	if resp.Token == "" || resp.APIKey.Prefix != resp.Token[:12] {
		t.Fatalf("token/prefix mismatch: token=%q prefix=%q", resp.Token, resp.APIKey.Prefix)
	}
	if len(resp.APIKey.Scopes) != 2 {
		t.Fatalf("scopes not stored: %v", resp.APIKey.Scopes)
	}

	// The token authenticates a /api call as the owner but only with the key
	// scopes: it can list keys (the one just minted) without holding apikeys:manage.
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys", "", map[string]string{
		"Authorization": "Bearer " + resp.Token,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("bearer call: status = %d, body = %s", w.Code, w.Body.String())
	}
	var listed struct {
		APIKeys []apiKeyView `json:"apiKeys"`
	}
	if err := json.Unmarshal([]byte(w.Body.String()), &listed); err != nil {
		t.Fatalf("decode bearer list: %v", err)
	}
	if len(listed.APIKeys) != 1 {
		t.Fatalf("bearer list wrong: %+v", listed.APIKeys)
	}

	// An invalid token is anonymous and therefore rejected on a gated route.
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys", "", map[string]string{
		"Authorization": "Bearer ahk_bogus",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", w.Code)
	}
}

func TestAPIKeyListScoping(t *testing.T) {
	k := newKeyHarness(t)
	// Owner creates a key for the plain user.
	body := `{"name":"for-user","scopes":["resources:read"],"userId":"` + k.user + `"}`
	w := do(k.t, k.h, http.MethodPost, "/api/api-keys", body, authHeader(k.ownerC))
	if w.Code != http.StatusCreated {
		t.Fatalf("owner create for user: status = %d, body = %s", w.Code, w.Body.String())
	}

	// The plain user sees only their own key.
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys", "", authHeader(k.userC))
	if w.Code != http.StatusOK {
		t.Fatalf("user list: status = %d", w.Code)
	}
	var list struct {
		APIKeys []apiKeyView `json:"apiKeys"`
	}
	if err := json.Unmarshal([]byte(w.Body.String()), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.APIKeys) != 1 || list.APIKeys[0].UserID != k.user {
		t.Fatalf("user list wrong: %+v", list.APIKeys)
	}

	// The owner sees every key.
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys", "", authHeader(k.ownerC))
	if w.Code != http.StatusOK {
		t.Fatalf("owner list: status = %d", w.Code)
	}
	if err := json.Unmarshal([]byte(w.Body.String()), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.APIKeys) != 1 || list.APIKeys[0].UserID != k.user {
		t.Fatalf("owner list wrong: %+v", list.APIKeys)
	}
}

func TestAPIKeyManageGuards(t *testing.T) {
	k := newKeyHarness(t)
	body := `{"name":"owner-key","scopes":["resources:read"]}`
	w := do(k.t, k.h, http.MethodPost, "/api/api-keys", body, authHeader(k.ownerC))
	keyID := decodeKey(k.t, w.Body.String()).APIKey.ID

	// A plain user may not read the owner's key.
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys/"+keyID, "", authHeader(k.userC))
	if w.Code != http.StatusForbidden {
		t.Fatalf("user get owner key: status = %d, want 403", w.Code)
	}

	// A plain user may not create a key for someone else.
	body = `{"name":"hijack","scopes":["resources:read"],"userId":"` + k.owner + `"}`
	w = do(k.t, k.h, http.MethodPost, "/api/api-keys", body, authHeader(k.userC))
	if w.Code != http.StatusForbidden {
		t.Fatalf("user create for owner: status = %d, want 403", w.Code)
	}

	// Unknown id is 404.
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys/key-missing", "", authHeader(k.ownerC))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing key: status = %d, want 404", w.Code)
	}

	// Missing name is rejected.
	w = do(k.t, k.h, http.MethodPost, "/api/api-keys", `{"scopes":["resources:read"]}`, authHeader(k.ownerC))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty name: status = %d, want 400", w.Code)
	}

	// An unknown scope is rejected.
	w = do(k.t, k.h, http.MethodPost, "/api/api-keys", `{"name":"x","scopes":["not_a_permission"]}`, authHeader(k.ownerC))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: status = %d, want 400", w.Code)
	}
}

func TestAPIKeyUpdateAndRevoke(t *testing.T) {
	k := newKeyHarness(t)
	w := do(k.t, k.h, http.MethodPost, "/api/api-keys", `{"name":"k","scopes":["resources:read"]}`, authHeader(k.ownerC))
	keyID := decodeKey(k.t, w.Body.String()).APIKey.ID

	// Rename and replace scopes.
	w = do(k.t, k.h, http.MethodPut, "/api/api-keys/"+keyID, `{"name":"renamed","scopes":["tasks:write"]}`, authHeader(k.ownerC))
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", w.Code, w.Body.String())
	}
	var got apiKeyView
	got = decodeKey(k.t, w.Body.String()).APIKey
	if got.Name != "renamed" || len(got.Scopes) != 1 || got.Scopes[0] != "tasks:write" {
		t.Fatalf("update not applied: %+v", got)
	}

	// Revoke, then a get is 404 (the store filters revoked keys).
	w = do(k.t, k.h, http.MethodDelete, "/api/api-keys/"+keyID, "", authHeader(k.ownerC))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d", w.Code)
	}
	w = do(k.t, k.h, http.MethodGet, "/api/api-keys/"+keyID, "", authHeader(k.ownerC))
	if w.Code != http.StatusNotFound {
		t.Fatalf("get revoked: status = %d, want 404", w.Code)
	}
}

func TestAPIKeyUnauthorizedWithoutSession(t *testing.T) {
	k := newKeyHarness(t)
	w := do(k.t, k.h, http.MethodGet, "/api/api-keys", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon list: status = %d, want 401", w.Code)
	}
}
