package auth

// API keys are the credential a script carries, which makes them the one place
// where "the code was never exercised" is a security finding rather than a gap in
// a report: the scopes are the only thing keeping a key from acting as its owner.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenthub/internal/authz"
)

// memAPIKeys is the key store as the service sees it: a hash lookup plus the
// admin views. It mirrors the GORM semantics that matter here (a revoked key is
// still returned by hash, so the caller can tell revoked from unknown).
type memAPIKeys struct {
	byID    map[string]*APIKey
	byToken map[string]*APIKey
	touched []string
}

func newMemAPIKeys() *memAPIKeys {
	return &memAPIKeys{byID: map[string]*APIKey{}, byToken: map[string]*APIKey{}}
}

func (m *memAPIKeys) Create(_ context.Context, k *APIKey, tokenHash string) error {
	cp := *k
	m.byID[k.ID] = &cp
	m.byToken[tokenHash] = &cp
	return nil
}

func (m *memAPIKeys) Get(_ context.Context, id string) (*APIKey, error) {
	if k, ok := m.byID[id]; ok {
		cp := *k
		return &cp, nil
	}
	return nil, nil
}

func (m *memAPIKeys) ByTokenHash(_ context.Context, tokenHash string) (*APIKey, error) {
	if k, ok := m.byToken[tokenHash]; ok {
		cp := *k
		return &cp, nil
	}
	return nil, nil
}

func (m *memAPIKeys) ListForUser(_ context.Context, userID string) ([]APIKey, error) {
	out := []APIKey{}
	for _, k := range m.byID {
		if k.UserID == userID {
			out = append(out, *k)
		}
	}
	return out, nil
}

func (m *memAPIKeys) ListAll(context.Context) ([]APIKey, error) {
	out := []APIKey{}
	for _, k := range m.byID {
		out = append(out, *k)
	}
	return out, nil
}

func (m *memAPIKeys) SetScopes(_ context.Context, id string, scopes []string) error {
	if k, ok := m.byID[id]; ok {
		k.Scopes = scopes
	}
	return nil
}

func (m *memAPIKeys) Update(_ context.Context, k *APIKey) error {
	cp := *k
	m.byID[k.ID] = &cp
	return nil
}

func (m *memAPIKeys) Revoke(_ context.Context, id string) error {
	if k, ok := m.byID[id]; ok {
		now := time.Now().UTC()
		k.RevokedAt = &now
	}
	return nil
}

func (m *memAPIKeys) Touch(_ context.Context, id string, at time.Time) error {
	m.touched = append(m.touched, id)
	if k, ok := m.byID[id]; ok {
		k.LastUsedAt = &at
	}
	return nil
}

// apiKeyService builds the identity facade over the memory stores with the owner
// account already created, so a key always has someone to belong to.
func apiKeyService(t *testing.T) (*Service, *memAPIKeys, *User) {
	t.Helper()
	users, sessions := newMemUserStore(), newMemSessionStore()
	keys := newMemAPIKeys()
	svc := New(users, sessions, &memOAuthStore{}, keys, authz.New(nil), Options{TTL: time.Hour})
	owner, err := svc.Register(context.Background(), "owner@example.com", "correct horse", "Owner")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	return svc, keys, owner
}

// seedKey mints a key for the owner and returns the raw secret.
func seedKey(t *testing.T, keys *memAPIKeys, owner *User, mutate func(*APIKey)) string {
	t.Helper()
	raw, prefix, hash, err := NewAPIKeyToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key := &APIKey{
		ID: "key-1", UserID: owner.ID, Name: "CI", Prefix: prefix,
		Scopes:    []string{authz.PermTasksRead},
		CreatedAt: time.Now().UTC(),
	}
	if mutate != nil {
		mutate(key)
	}
	if err := keys.Create(context.Background(), key, hash); err != nil {
		t.Fatalf("store key: %v", err)
	}
	return raw
}

func TestNewAPIKeyTokenIsPrefixedHashedAndUnique(t *testing.T) {
	raw, prefix, hash, err := NewAPIKeyToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(raw) != len(APIKeyPrefix)+64 {
		t.Fatalf("raw key length = %d, want %d", len(raw), len(APIKeyPrefix)+64)
	}
	if raw[:len(APIKeyPrefix)] != APIKeyPrefix {
		t.Fatalf("raw key = %q, want the %q prefix", raw, APIKeyPrefix)
	}
	if prefix != raw[:len(APIKeyPrefix)+8] {
		t.Fatalf("prefix = %q, want the display half of the raw key", prefix)
	}
	// The stored hash must be the hash of the raw value, or nothing will resolve.
	if hash != HashToken(raw) {
		t.Fatal("the stored hash must be HashToken(raw)")
	}
	if hash == raw {
		t.Fatal("the raw secret must never be stored")
	}

	secondRaw, _, secondHash, err := NewAPIKeyToken()
	if err != nil {
		t.Fatalf("mint again: %v", err)
	}
	if secondRaw == raw || secondHash == hash {
		t.Fatal("two keys must not collide")
	}
}

func TestAPIKeyActiveWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	cases := []struct {
		name string
		key  APIKey
		want bool
	}{
		{"no expiry", APIKey{}, true},
		{"expiry in the future", APIKey{ExpiresAt: &future}, true},
		{"expired", APIKey{ExpiresAt: &past}, false},
		{"revoked", APIKey{RevokedAt: &past}, false},
		{"revoked and expired", APIKey{RevokedAt: &past, ExpiresAt: &past}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.Active(now); got != tc.want {
				t.Fatalf("Active = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateAPIKeyScopes(t *testing.T) {
	owner := func(string) bool { return true }
	limited := func(perm string) bool { return perm == authz.PermTasksRead }

	cases := []struct {
		name   string
		scopes []string
		can    func(string) bool
		want   error
	}{
		{"a known scope the caller holds", []string{authz.PermTasksRead}, owner, nil},
		{"duplicates collapse", []string{authz.PermTasksRead, authz.PermTasksRead}, owner, nil},
		{"empty is not a key", nil, owner, ErrAPIKeyInvalidScope},
		{"an unknown permission", []string{"launch:missiles"}, owner, ErrAPIKeyInvalidScope},
		{"a known scope the caller lacks", []string{authz.PermBackupsManage}, limited, ErrAPIKeyInvalidScope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Errors are compared by identity: the console localises the code.
			if err := ValidateAPIKeyScopes(tc.scopes, tc.can); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAPIKeyRawComesFromTheKeyHeaderOrABearer(t *testing.T) {
	build := func(headers map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}

	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"the dedicated header", map[string]string{"X-AgentHub-Key": "ahk_abc"}, "ahk_abc"},
		{"the header wins over a bearer", map[string]string{"X-AgentHub-Key": "ahk_abc", "Authorization": "Bearer ahk_def"}, "ahk_abc"},
		{"a bearer token", map[string]string{"Authorization": "Bearer ahk_def"}, "ahk_def"},
		{"a bearer with padding", map[string]string{"Authorization": "Bearer   ahk_def  "}, "ahk_def"},
		{"another scheme is not a key", map[string]string{"Authorization": "Basic abc"}, ""},
		{"nothing at all", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiKeyRaw(build(tc.headers)); got != tc.want {
				t.Fatalf("apiKeyRaw = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthenticateAPIKeyCarriesOnlyItsScopes(t *testing.T) {
	svc, keys, owner := apiKeyService(t)
	raw := seedKey(t, keys, owner, nil)

	p, keyID, err := svc.AuthenticateAPIKey(context.Background(), raw)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if keyID != "key-1" {
		t.Fatalf("key id = %q", keyID)
	}
	if p.UserID != owner.ID || p.IsOwner {
		t.Fatalf("a key acts as its owner but never with the owner bypass: %+v", p)
	}
	if !p.Can(authz.PermTasksRead) {
		t.Fatal("the key must hold the scope it was issued with")
	}
	// The owner holds everything; the key must still be limited to its own scopes.
	if p.Can(authz.PermBackupsManage) {
		t.Fatalf("the key must not inherit the owner's other permissions: %v", p.Permissions)
	}
}

func TestAuthenticateAPIKeyRefusesUnusableKeys(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)

	t.Run("an unknown secret", func(t *testing.T) {
		svc, _, _ := apiKeyService(t)
		if _, _, err := svc.AuthenticateAPIKey(context.Background(), "ahk_nope"); !errors.Is(err, ErrAPIKeyNotFound) {
			t.Fatalf("err = %v, want ErrAPIKeyNotFound", err)
		}
	})

	t.Run("no key store at all", func(t *testing.T) {
		svc := New(newMemUserStore(), newMemSessionStore(), &memOAuthStore{}, nil, nil, Options{})
		if _, _, err := svc.AuthenticateAPIKey(context.Background(), "ahk_nope"); !errors.Is(err, ErrAPIKeyNotFound) {
			t.Fatalf("err = %v, want ErrAPIKeyNotFound", err)
		}
	})

	t.Run("a revoked key", func(t *testing.T) {
		svc, keys, owner := apiKeyService(t)
		raw := seedKey(t, keys, owner, func(k *APIKey) { k.RevokedAt = &past })
		if _, _, err := svc.AuthenticateAPIKey(context.Background(), raw); !errors.Is(err, ErrAPIKeyRevoked) {
			t.Fatalf("err = %v, want ErrAPIKeyRevoked", err)
		}
	})

	t.Run("an expired key", func(t *testing.T) {
		svc, keys, owner := apiKeyService(t)
		raw := seedKey(t, keys, owner, func(k *APIKey) { k.ExpiresAt = &past })
		if _, _, err := svc.AuthenticateAPIKey(context.Background(), raw); !errors.Is(err, ErrAPIKeyExpired) {
			t.Fatalf("err = %v, want ErrAPIKeyExpired", err)
		}
	})

	t.Run("a disabled owner", func(t *testing.T) {
		svc, keys, owner := apiKeyService(t)
		ctx := context.Background()
		disabled := *owner
		disabled.Status = StatusDisabled
		if err := svc.users.Update(ctx, &disabled); err != nil {
			t.Fatalf("disable owner: %v", err)
		}
		raw := seedKey(t, keys, owner, nil)
		if _, _, err := svc.AuthenticateAPIKey(ctx, raw); !errors.Is(err, ErrUserDisabled) {
			t.Fatalf("err = %v, want ErrUserDisabled", err)
		}
	})
}

func TestAPIKeyCannotExceedItsOwner(t *testing.T) {
	svc, keys, owner := apiKeyService(t)
	ctx := context.Background()

	// A second account with no roles at all: everything it holds comes from the
	// key's scopes, minus the permissions it does not have — which is nothing.
	member, err := svc.Register(ctx, "member@example.com", "correct horse", "Member")
	if err != nil {
		t.Fatalf("register member: %v", err)
	}
	raw := seedKey(t, keys, member, nil)

	p, _, err := svc.AuthenticateAPIKey(ctx, raw)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.Can(authz.PermTasksRead) {
		t.Fatal("a key must not grant a permission its owner lacks")
	}
	_ = owner
}

func TestPrincipalPrefersTheKeyAndFallsBackToTheSession(t *testing.T) {
	svc, keys, owner := apiKeyService(t)
	ctx := context.Background()
	raw := seedKey(t, keys, owner, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("X-AgentHub-Key", raw)
	p, sess, err := svc.Principal(ctx, req)
	if err != nil || p == nil || sess != nil {
		t.Fatalf("a key must resolve to a principal without a session: p=%+v sess=%v err=%v", p, sess, err)
	}
	if len(keys.touched) != 1 || keys.touched[0] != "key-1" {
		t.Fatalf("using a key must record last-used, got %v", keys.touched)
	}

	// An anonymous request resolves to nobody rather than to an error.
	empty := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	if p, sess, err := svc.Principal(ctx, empty); p != nil || sess != nil || err != nil {
		t.Fatalf("anonymous: p=%+v sess=%v err=%v", p, sess, err)
	}

	// A stale key is not a failure either: the call becomes anonymous.
	stale := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	stale.Header.Set("Authorization", "Bearer ahk_gone")
	if p, _, err := svc.Principal(ctx, stale); p != nil || err != nil {
		t.Fatalf("a dead key must degrade to anonymous, got p=%+v err=%v", p, err)
	}
}

func TestServiceAccessorsAndCookieJar(t *testing.T) {
	svc, keys, owner := apiKeyService(t)
	if svc.APIKeys() != keys {
		t.Fatal("APIKeys must expose the store the service was built with")
	}
	if svc.Options().CookieName != "agenthub_session" {
		t.Fatalf("cookie name = %q", svc.Options().CookieName)
	}
	if svc.Users() == nil || svc.Authz() == nil {
		t.Fatal("the accessors must return the wired stores")
	}
	// A user with no sessions yet answers with an empty list, not an error.
	sessions, err := svc.Sessions(context.Background(), owner.ID)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %v (err=%v)", sessions, err)
	}

	w := httptest.NewRecorder()
	svc.SetCookie(w, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil), "tok", time.Now().Add(time.Hour))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want one cookie, got %d", len(cookies))
	}
	if !cookies[0].HttpOnly || cookies[0].Path != "/" || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags = %+v", cookies[0])
	}
	if cookies[0].Secure {
		t.Fatal("a plain-http deployment must not mark the cookie Secure by default")
	}

	w = httptest.NewRecorder()
	svc.ClearCookie(w)
	cleared := w.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Value != "" || cleared[0].MaxAge >= 0 {
		t.Fatalf("logout must expire the cookie, got %+v", cleared)
	}
}

func TestSecureCookieWarnsOnlyWhenItCannotWork(t *testing.T) {
	users, sessions := newMemUserStore(), newMemSessionStore()
	svc := New(users, sessions, &memOAuthStore{}, nil, nil, Options{Secure: true, TTL: time.Hour})

	// Secure over plain HTTP is dropped by the browser; the cookie is still set
	// (the caller may be behind a proxy) but the operator is warned.
	plain := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	w := httptest.NewRecorder()
	svc.SetCookie(w, plain, "tok", time.Now().Add(time.Hour))
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("the configured Secure flag must be honoured")
	}

	// Behind a TLS-terminating proxy, X-Forwarded-Proto is what says the request
	// arrived over https.
	if !isRequestTLS(func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		r.Header.Set("X-Forwarded-Proto", "HTTPS")
		return r
	}()) {
		t.Fatal("X-Forwarded-Proto: https counts as TLS")
	}
	if isRequestTLS(plain) {
		t.Fatal("a plain request is not TLS")
	}
}
