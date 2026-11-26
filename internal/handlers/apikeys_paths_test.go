package handlers

// API keys are the one credential a plain user may mint for themselves, so both
// halves matter: a key must never carry more than its issuer, and a store that
// cannot answer must not look like "you have no keys".

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/httputil"
)

type failingAPIKeys struct {
	*memAPIKeys
	listErr   error
	getErr    error
	createErr error
	scopesErr error
	updateErr error
	revokeErr error
}

func (f *failingAPIKeys) ListForUser(ctx context.Context, userID string) ([]auth.APIKey, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.memAPIKeys.ListForUser(ctx, userID)
}

func (f *failingAPIKeys) ListAll(ctx context.Context) ([]auth.APIKey, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.memAPIKeys.ListAll(ctx)
}

func (f *failingAPIKeys) Get(ctx context.Context, id string) (*auth.APIKey, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.memAPIKeys.Get(ctx, id)
}

func (f *failingAPIKeys) Create(ctx context.Context, k *auth.APIKey, tokenHash string) error {
	if f.createErr != nil {
		return f.createErr
	}
	return f.memAPIKeys.Create(ctx, k, tokenHash)
}

func (f *failingAPIKeys) SetScopes(ctx context.Context, id string, scopes []string) error {
	if f.scopesErr != nil {
		return f.scopesErr
	}
	return f.memAPIKeys.SetScopes(ctx, id, scopes)
}

func (f *failingAPIKeys) Update(ctx context.Context, k *auth.APIKey) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	return f.memAPIKeys.Update(ctx, k)
}

func (f *failingAPIKeys) Revoke(ctx context.Context, id string) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	return f.memAPIKeys.Revoke(ctx, id)
}

type apiKeyPaths struct {
	t     *testing.T
	h     http.Handler
	deps  Deps
	keys  *failingAPIKeys
	owner string // session cookie
	user  string // session cookie
	// ownerID is the author of every key this harness mints.
	ownerID string
}

func newAPIKeyPaths(t *testing.T) *apiKeyPaths {
	t.Helper()
	keys := &failingAPIKeys{memAPIKeys: newMemAPIKeys()}
	users := newMemUsers()
	sessions := newMemSessions()
	svc := auth.New(users, sessions, nil, keys, nil, auth.Options{
		CookieName: "agenthub_session",
		TTL:        time.Hour,
	})
	d := Deps{Cfg: openConfig(t.TempDir()), Auth: svc, Log: testLogger()}
	h := Register(d)
	ctx := context.Background()
	owner, err := svc.Register(ctx, "owner@agenthub.test", "correct horse", "Owner")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	if _, err := svc.Register(ctx, "user@agenthub.test", "correct horse", "User"); err != nil {
		t.Fatalf("register user: %v", err)
	}
	return &apiKeyPaths{
		t:       t,
		h:       h,
		deps:    d,
		keys:    keys,
		owner:   sessionOf(t, h, "owner@agenthub.test", "correct horse"),
		user:    sessionOf(t, h, "user@agenthub.test", "correct horse"),
		ownerID: owner.ID,
	}
}

func (k *apiKeyPaths) mint(name string) apiKeyResponse {
	k.t.Helper()
	w := do(k.t, k.h, http.MethodPost, "/api/api-keys",
		`{"name":"`+name+`","scopes":["resources:read"]}`, authHeader(k.owner))
	if w.Code != http.StatusCreated {
		k.t.Fatalf("mint %s: status = %d, body = %s", name, w.Code, w.Body.String())
	}
	return decodeKey(k.t, w.Body.String())
}

// TestAPIKeyListReportsAStoreFailure: an empty list would tell the console this
// user holds no credentials, and the next "mint" would look like a duplicate.
func TestAPIKeyListReportsAStoreFailure(t *testing.T) {
	k := newAPIKeyPaths(t)
	k.keys.listErr = errStoreDown

	for _, c := range []struct {
		name  string
		token string
	}{
		{"own keys", k.user},
		{"every key", k.owner},
	} {
		w := do(t, k.h, http.MethodGet, "/api/api-keys", "", authHeader(c.token))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, body = %s", c.name, w.Code, w.Body.String())
		}
	}
}

func TestAPIKeyCreateBranches(t *testing.T) {
	k := newAPIKeyPaths(t)

	// A body that does not decode must not reach the store.
	w := do(t, k.h, http.MethodPost, "/api/api-keys", `{"name":`, authHeader(k.owner))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("decode failure: status = %d", w.Code)
	}

	// Expiry is optional; when asked for, the view must carry it.
	w = do(t, k.h, http.MethodPost, "/api/api-keys", `{"name":"expiring","scopes":["resources:read"],"expiresInDays":3}`, authHeader(k.owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("expiring key: status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := decodeKey(t, w.Body.String()).APIKey.ExpiresAt; got == nil {
		t.Fatal("expiresAt = null, want the requested expiry")
	}

	// A failed write must not hand out a token the store does not know.
	k.keys.createErr = errStoreDown
	w = do(t, k.h, http.MethodPost, "/api/api-keys", `{"name":"lost","scopes":["resources:read"]}`, authHeader(k.owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create failure: status = %d, body = %s", w.Code, w.Body.String())
	}
	if w.Body.Len() > 0 && decodeKey(t, w.Body.String()).Token != "" {
		t.Fatal("a failed create returned a token")
	}
	k.keys.createErr = nil
}

func TestAPIKeyReadAndUpdateBranches(t *testing.T) {
	k := newAPIKeyPaths(t)
	key := k.mint("readable")

	// The owning path is exercised through the detail route.
	w := do(t, k.h, http.MethodGet, "/api/api-keys/"+key.APIKey.ID, "", authHeader(k.owner))
	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d, body = %s", w.Code, w.Body.String())
	}

	k.keys.getErr = errStoreDown
	w = do(t, k.h, http.MethodPut, "/api/api-keys/"+key.APIKey.ID, `{"name":"x","scopes":["resources:read"]}`, authHeader(k.owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update with a failing read: status = %d, body = %s", w.Code, w.Body.String())
	}
	k.keys.getErr = nil

	// Unknown id: the api key domain has its own 404 code.
	w = do(t, k.h, http.MethodPut, "/api/api-keys/key-missing", `{"name":"x","scopes":["resources:read"]}`, authHeader(k.owner))
	if w.Code != http.StatusNotFound || decodeErrorCode(t, w) != string(httputil.CodeAPIKeyNotFound) {
		t.Fatalf("update unknown: status = %d, body = %s", w.Code, w.Body.String())
	}

	// Another user's key is forbidden even though the route only requires a
	// session: ownership is checked per object.
	w = do(t, k.h, http.MethodPut, "/api/api-keys/"+key.APIKey.ID, `{"name":"stolen"}`, authHeader(k.user))
	if w.Code != http.StatusForbidden {
		t.Fatalf("update another user's key: status = %d, body = %s", w.Code, w.Body.String())
	}

	w = do(t, k.h, http.MethodPut, "/api/api-keys/"+key.APIKey.ID, `{"name":`, authHeader(k.owner))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update decode failure: status = %d", w.Code)
	}

	// A scope the caller cannot grant is refused before any write.
	w = do(t, k.h, http.MethodPut, "/api/api-keys/"+key.APIKey.ID, `{"scopes":["not:a:scope"]}`, authHeader(k.owner))
	if w.Code != http.StatusBadRequest || decodeErrorCode(t, w) != string(httputil.CodeAPIKeyInvalidScope) {
		t.Fatalf("invalid scope: status = %d, body = %s", w.Code, w.Body.String())
	}

	k.keys.scopesErr = errStoreDown
	w = do(t, k.h, http.MethodPut, "/api/api-keys/"+key.APIKey.ID, `{"scopes":["resources:read"]}`, authHeader(k.owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("scope write failure: status = %d, body = %s", w.Code, w.Body.String())
	}
	k.keys.scopesErr = nil

	k.keys.updateErr = errStoreDown
	w = do(t, k.h, http.MethodPut, "/api/api-keys/"+key.APIKey.ID,
		`{"expiresAt":"2030-01-01T00:00:00Z"}`, authHeader(k.owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update failure: status = %d, body = %s", w.Code, w.Body.String())
	}
	k.keys.updateErr = nil
}

func TestAPIKeyRevokeBranches(t *testing.T) {
	k := newAPIKeyPaths(t)
	key := k.mint("revocable")

	k.keys.getErr = errStoreDown
	w := do(t, k.h, http.MethodDelete, "/api/api-keys/"+key.APIKey.ID, "", authHeader(k.owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("revoke with a failing read: status = %d, body = %s", w.Code, w.Body.String())
	}
	k.keys.getErr = nil

	w = do(t, k.h, http.MethodDelete, "/api/api-keys/key-missing", "", authHeader(k.owner))
	if w.Code != http.StatusNotFound || decodeErrorCode(t, w) != string(httputil.CodeAPIKeyNotFound) {
		t.Fatalf("revoke unknown: status = %d, body = %s", w.Code, w.Body.String())
	}

	w = do(t, k.h, http.MethodDelete, "/api/api-keys/"+key.APIKey.ID, "", authHeader(k.user))
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoke another user's key: status = %d, body = %s", w.Code, w.Body.String())
	}

	k.keys.revokeErr = errStoreDown
	w = do(t, k.h, http.MethodDelete, "/api/api-keys/"+key.APIKey.ID, "", authHeader(k.owner))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("revoke failure: status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestWriteAPIKeyErrMapsEverySentinel keeps the key vocabulary in one table: the
// console localises these codes when a request is refused.
func TestWriteAPIKeyErrMapsEverySentinel(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{auth.ErrAPIKeyNotFound, http.StatusNotFound, string(httputil.CodeAPIKeyNotFound)},
		{auth.ErrAPIKeyNameRequired, http.StatusBadRequest, string(httputil.CodeAPIKeyNameRequired)},
		{auth.ErrAPIKeyInvalidScope, http.StatusBadRequest, string(httputil.CodeAPIKeyInvalidScope)},
		{auth.ErrAPIKeyExpired, http.StatusUnauthorized, string(httputil.CodeAPIKeyExpired)},
		{auth.ErrAPIKeyRevoked, http.StatusUnauthorized, string(httputil.CodeAPIKeyRevoked)},
		{errors.New("plain failure"), http.StatusInternalServerError, string(httputil.CodeInternal)},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		writeAPIKeyErr(w, c.err)
		if w.Code != c.status || decodeErrorCode(t, w) != c.code {
			t.Errorf("%v: status = %d code = %q, want %d / %q", c.err, w.Code, decodeErrorCode(t, w), c.status, c.code)
		}
	}
}

// TestMayManageKeyWithoutAPrincipal is the defensive read: no identity means no
// ownership, even for a key whose UserID happens to be empty.
func TestMayManageKeyWithoutAPrincipal(t *testing.T) {
	k := newAPIKeyPaths(t)
	if k.deps.mayManageKey(nil, &auth.APIKey{ID: "key-1"}) {
		t.Fatal("a nil principal must not manage a key")
	}
}
