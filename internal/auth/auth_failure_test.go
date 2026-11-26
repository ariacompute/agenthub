package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/authz"
)

// The happy paths live in auth_test.go; this file is the failure half. Every case
// arms one store hook and asserts the identity of the error the service returns —
// never a log line — because that error is what a handler turns into a status code
// and what the console translates.
//
// The fakes embed the in-memory stores rather than reimplementing them: one method
// fails, and everything else keeps the semantics the rest of the suite relies on.
// No production code was reshaped to make these reachable.

// errStoreDown is the injected failure. It is a plain sentinel so a case can
// assert the service passed *this* error through instead of inventing its own.
var errStoreDown = errors.New("store down")

type errUserStore struct {
	*memUserStore
	getErr        error
	getByEmailErr error
	countErr      error
	createErr     error
	updateErr     error
	recordErr     error
	resetErr      error
	lockedUntil   *time.Time
	lockedErr     error
}

func (s *errUserStore) Get(ctx context.Context, id string) (*User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memUserStore.Get(ctx, id)
}

func (s *errUserStore) GetByEmail(ctx context.Context, email string) (*User, error) {
	if s.getByEmailErr != nil {
		return nil, s.getByEmailErr
	}
	return s.memUserStore.GetByEmail(ctx, email)
}

func (s *errUserStore) Count(ctx context.Context) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.memUserStore.Count(ctx)
}

func (s *errUserStore) Create(ctx context.Context, u *User) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.memUserStore.Create(ctx, u)
}

func (s *errUserStore) Update(ctx context.Context, u *User) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.memUserStore.Update(ctx, u)
}

func (s *errUserStore) RecordLoginFailure(ctx context.Context, email string) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	return s.memUserStore.RecordLoginFailure(ctx, email)
}

func (s *errUserStore) ResetLoginAttempts(ctx context.Context, email string) error {
	if s.resetErr != nil {
		return s.resetErr
	}
	return s.memUserStore.ResetLoginAttempts(ctx, email)
}

func (s *errUserStore) LockedUntil(ctx context.Context, email string) (*time.Time, error) {
	if s.lockedErr != nil {
		return nil, s.lockedErr
	}
	if s.lockedUntil != nil {
		at := *s.lockedUntil
		return &at, nil
	}
	return s.memUserStore.LockedUntil(ctx, email)
}

type errSessionStore struct {
	*memSessionStore
	createErr        error
	byHashErr        error
	revokeErr        error
	touchErr         error
	listErr          error
	revokeForUserErr error
}

func (s *errSessionStore) Create(ctx context.Context, sess *Session, tokenHash string) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.memSessionStore.Create(ctx, sess, tokenHash)
}

func (s *errSessionStore) ByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	if s.byHashErr != nil {
		return nil, s.byHashErr
	}
	return s.memSessionStore.ByTokenHash(ctx, tokenHash)
}

func (s *errSessionStore) Revoke(ctx context.Context, id string) error {
	if s.revokeErr != nil {
		return s.revokeErr
	}
	return s.memSessionStore.Revoke(ctx, id)
}

func (s *errSessionStore) Touch(ctx context.Context, id string, at time.Time) error {
	if s.touchErr != nil {
		return s.touchErr
	}
	return s.memSessionStore.Touch(ctx, id, at)
}

func (s *errSessionStore) ListForUser(ctx context.Context, userID string) ([]Session, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memSessionStore.ListForUser(ctx, userID)
}

func (s *errSessionStore) RevokeForUser(ctx context.Context, userID string) error {
	if s.revokeForUserErr != nil {
		return s.revokeForUserErr
	}
	return s.memSessionStore.RevokeForUser(ctx, userID)
}

type errOAuthStore struct {
	*memOAuthStore
	findErr error
	linkErr error
}

func (s *errOAuthStore) Find(ctx context.Context, provider, subject string) (*OAuthAccount, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	return s.memOAuthStore.Find(ctx, provider, subject)
}

func (s *errOAuthStore) Link(ctx context.Context, a *OAuthAccount) error {
	if s.linkErr != nil {
		return s.linkErr
	}
	return s.memOAuthStore.Link(ctx, a)
}

// errAPIKeys fails only the last-used touch, which is the one API-key call the
// request path is allowed to swallow.
type errAPIKeys struct {
	*memAPIKeys
	touchErr error
}

func (s *errAPIKeys) Touch(ctx context.Context, id string, at time.Time) error {
	if s.touchErr != nil {
		return s.touchErr
	}
	return s.memAPIKeys.Touch(ctx, id, at)
}

// failingAuthz satisfies the port by embedding it: Load stops at the first call,
// so the methods below it are never reached.
type failingAuthz struct {
	authz.AuthzStore
	err error
}

func (f failingAuthz) GroupsOfUser(context.Context, string) ([]string, error) {
	return nil, f.err
}

// newFailingService builds the facade over hookable stores with one registered
// account, so a case can arm a failure after registration and still have a
// password hash that verifies.
func newFailingService(t *testing.T) (*Service, *errUserStore, *errSessionStore) {
	t.Helper()
	users := &errUserStore{memUserStore: newMemUserStore()}
	sessions := &errSessionStore{memSessionStore: newMemSessionStore()}
	svc := New(users, sessions, &memOAuthStore{}, nil, nil, Options{TTL: time.Hour})
	if _, err := svc.Register(context.Background(), "u@example.com", "correct horse", "U"); err != nil {
		t.Fatalf("register: %v", err)
	}
	return svc, users, sessions
}

// newAuthedService is newFailingService plus one live session and its raw token.
func newAuthedService(t *testing.T) (*Service, *errUserStore, *errSessionStore, *User, string) {
	t.Helper()
	ctx := context.Background()
	svc, users, sessions := newFailingService(t)
	u, token, _, err := svc.Login(ctx, "u@example.com", "correct horse", "", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return svc, users, sessions, u, token
}

func TestRegisterStoreFailures(t *testing.T) {
	ctx := context.Background()

	// Without a store the package stays inert, which is what the pure-rule tests
	// rely on; Register has to say so instead of panicking on a nil interface.
	if _, err := New(nil, nil, nil, nil, nil, Options{}).
		Register(ctx, "u@example.com", "correct horse", ""); err == nil {
		t.Fatal("Register without a user store must fail")
	}

	users := &errUserStore{memUserStore: newMemUserStore()}
	svc := New(users, newMemSessionStore(), &memOAuthStore{}, nil, nil, Options{TTL: time.Hour})

	// The owner flag is decided by Count: a count that fails must abort the
	// registration, otherwise a transient read error could create a second owner.
	users.countErr = errStoreDown
	if _, err := svc.Register(ctx, "a@example.com", "correct horse", ""); !errors.Is(err, errStoreDown) {
		t.Fatalf("Count failure: %v, want %v", err, errStoreDown)
	}
	users.countErr = nil

	// A failing insert is returned as is: the handler maps it to a 500 and the
	// password hash never reaches a row.
	users.createErr = errStoreDown
	if _, err := svc.Register(ctx, "b@example.com", "correct horse", ""); !errors.Is(err, errStoreDown) {
		t.Fatalf("Create failure: %v, want %v", err, errStoreDown)
	}
	users.createErr = nil

	// Clearing the hook proves the failure above came from the hook and not from
	// the input.
	if _, err := svc.Register(ctx, "c@example.com", "correct horse", ""); err != nil {
		t.Fatalf("register after clearing the hook: %v", err)
	}
}

func TestLoginFailurePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("stores not configured", func(t *testing.T) {
		if _, _, _, err := New(nil, nil, nil, nil, nil, Options{}).
			Login(ctx, "u@example.com", "correct horse", "", ""); err == nil {
			t.Fatal("Login without stores must fail")
		}
	})

	t.Run("oauth only account has no password", func(t *testing.T) {
		users := newMemUserStore()
		if err := users.Create(ctx, &User{ID: "user-oauth", Email: "oauth@example.com", Status: StatusActive}); err != nil {
			t.Fatalf("seed oauth account: %v", err)
		}
		svc := New(users, newMemSessionStore(), &memOAuthStore{}, nil, nil, Options{TTL: time.Hour})
		// The address exists, so this is not the enumeration-safe "invalid
		// credentials" answer: the caller is told to use the provider instead.
		if _, _, _, err := svc.Login(ctx, "oauth@example.com", "anything", "", ""); err != ErrNoPassword {
			t.Fatalf("err = %v, want no_password_set", err)
		}
	})

	t.Run("lockout lookup failure does not block a good password", func(t *testing.T) {
		svc, users, _ := newFailingService(t)
		users.lockedErr = errStoreDown
		if _, _, _, err := svc.Login(ctx, "u@example.com", "correct horse", "", ""); err != nil {
			t.Fatalf("a failing lockout check must not deny a verified password: %v", err)
		}
	})

	t.Run("lockout window in the future denies before hashing", func(t *testing.T) {
		svc, users, _ := newFailingService(t)
		until := time.Now().UTC().Add(AccountLockMinutes * time.Minute)
		users.lockedUntil = &until
		// Even the correct password is refused while the window is open.
		if _, _, _, err := svc.Login(ctx, "u@example.com", "correct horse", "", ""); err != ErrAccountLocked {
			t.Fatalf("err = %v, want account_locked", err)
		}
	})

	t.Run("failure counter write fails", func(t *testing.T) {
		svc, users, _ := newFailingService(t)
		users.recordErr = errStoreDown
		// The counter is a defence in depth: losing it must not change the answer
		// to the caller, which is the enumeration-safe invalid_credentials.
		if _, _, _, err := svc.Login(ctx, "u@example.com", "wrong password", "", ""); err != ErrInvalidCredentials {
			t.Fatalf("err = %v, want invalid_credentials", err)
		}
	})

	t.Run("counter reset after success fails", func(t *testing.T) {
		svc, users, _ := newFailingService(t)
		users.resetErr = errStoreDown
		// A stale counter leaves a later failure closer to the lockout, but the
		// successful login itself has already been decided.
		if _, token, sess, err := svc.Login(ctx, "u@example.com", "correct horse", "", ""); err != nil || token == "" || sess == nil {
			t.Fatalf("login must survive a failing reset: token=%q sess=%v err=%v", token, sess, err)
		}
	})

	t.Run("session write fails", func(t *testing.T) {
		svc, _, sessions := newFailingService(t)
		sessions.createErr = errStoreDown
		if _, _, _, err := svc.Login(ctx, "u@example.com", "correct horse", "", ""); !errors.Is(err, errStoreDown) {
			t.Fatalf("err = %v, want %v", err, errStoreDown)
		}
	})

	t.Run("user agent is truncated on the session", func(t *testing.T) {
		svc, _, _ := newFailingService(t)
		_, _, sess, err := svc.Login(ctx, "u@example.com", "correct horse", strings.Repeat("u", 300), "10.0.0.1")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if len(sess.UserAgent) != 256 {
			t.Fatalf("user agent length = %d, want the column bound of 256", len(sess.UserAgent))
		}
	})
}

func TestBootstrapAdminFailurePaths(t *testing.T) {
	ctx := context.Background()
	users := &errUserStore{memUserStore: newMemUserStore()}
	svc := New(users, newMemSessionStore(), &memOAuthStore{}, nil, nil, Options{TTL: time.Hour})

	// A boot that cannot read the table must not fall through to "create": that
	// would fork a second owner behind the operator's back.
	users.getByEmailErr = errStoreDown
	if err := svc.BootstrapAdmin(ctx, "admin@example.com", "correct horse", "Admin"); !errors.Is(err, errStoreDown) {
		t.Fatalf("GetByEmail failure: %v, want %v", err, errStoreDown)
	}
	users.getByEmailErr = nil

	// Promotion of an existing account writes the owner flag, and that write is
	// the one thing that can fail. The account has to be registered second:
	// whoever registers first is already the owner, and an owner is a no-op.
	if _, err := svc.Register(ctx, "first@example.com", "correct horse", "First"); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if _, err := svc.Register(ctx, "person@example.com", "correct horse", "Person"); err != nil {
		t.Fatalf("register person: %v", err)
	}
	users.updateErr = errStoreDown
	if err := svc.BootstrapAdmin(ctx, "person@example.com", "", ""); !errors.Is(err, errStoreDown) {
		t.Fatalf("Update failure: %v, want %v", err, errStoreDown)
	}
	users.updateErr = nil

	if err := svc.BootstrapAdmin(ctx, "person@example.com", "", ""); err != nil {
		t.Fatalf("promote: %v", err)
	}
	promoted, err := users.GetByEmail(ctx, "person@example.com")
	if err != nil || promoted == nil || !promoted.IsOwner {
		t.Fatalf("promoted account = %+v, err=%v", promoted, err)
	}
	// The password is never rewritten by a promotion: an operator may have
	// changed it in the console after the first boot.
	if !VerifyPassword(promoted.PasswordHash, "correct horse") {
		t.Fatal("promotion must not touch the password hash")
	}

	// A first boot with a too-short password is refused at the same boundary as
	// registration.
	if err := svc.BootstrapAdmin(ctx, "root@example.com", "short", ""); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak bootstrap password: %v, want weak_password", err)
	}

	// An empty name falls back to the administrator label.
	if err := svc.BootstrapAdmin(ctx, "root@example.com", "correct horse", ""); err != nil {
		t.Fatalf("bootstrap root: %v", err)
	}
	root, err := users.GetByEmail(ctx, "root@example.com")
	if err != nil || root == nil {
		t.Fatalf("root account missing: %v", err)
	}
	if root.Name != "Administrator" {
		t.Fatalf("name = %q, want the default label", root.Name)
	}
}

func TestPrincipalAndAuthenticateFailurePaths(t *testing.T) {
	ctx := context.Background()

	// No session store answers "anonymous, no error": the public Agent Card and
	// MCP export stay reachable on an instance without auth tables.
	bare := New(nil, nil, nil, nil, nil, Options{TTL: time.Hour})
	anonymous := httptest.NewRequest(http.MethodGet, "/", nil)
	anonymous.AddCookie(&http.Cookie{Name: bare.opts.CookieName, Value: "abc"})
	if p, sess, err := bare.Principal(ctx, anonymous); p != nil || sess != nil || err != nil {
		t.Fatalf("Principal without sessions = %v / %v / %v", p, sess, err)
	}

	// The cookie path resolves into a principal, which is the branch an
	// API-key-only test never reaches.
	svc, _, _, user, token := newAuthedService(t)
	withCookie := httptest.NewRequest(http.MethodGet, "/", nil)
	withCookie.AddCookie(&http.Cookie{Name: svc.opts.CookieName, Value: token})
	p, sess, err := svc.Principal(ctx, withCookie)
	if err != nil || p == nil || sess == nil {
		t.Fatalf("cookie Principal = %v / %v / %v", p, sess, err)
	}
	if p.UserID != user.ID {
		t.Fatalf("principal user = %q, want %q", p.UserID, user.ID)
	}

	t.Run("a failing key touch does not fail the call", func(t *testing.T) {
		keys := &errAPIKeys{memAPIKeys: newMemAPIKeys(), touchErr: errStoreDown}
		kusers := newMemUserStore()
		keySvc := New(kusers, newMemSessionStore(), &memOAuthStore{}, keys, authz.New(nil), Options{TTL: time.Hour})
		owner, err := keySvc.Register(ctx, "owner@example.com", "correct horse", "Owner")
		if err != nil {
			t.Fatalf("register owner: %v", err)
		}
		raw := seedKey(t, keys.memAPIKeys, owner, nil)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-AgentHub-Key", raw)
		p, sess, err := keySvc.Principal(ctx, req)
		if err != nil || p == nil || sess != nil {
			t.Fatalf("key Principal = %v / %v / %v", p, sess, err)
		}
		if p.UserID != owner.ID {
			t.Fatalf("principal user = %q, want %q", p.UserID, owner.ID)
		}
	})

	t.Run("authenticate without a session store or token", func(t *testing.T) {
		if _, _, err := bare.Authenticate(ctx, "token"); err != ErrSessionExpired {
			t.Fatalf("no store: %v, want session_expired", err)
		}
		if _, _, err := svc.Authenticate(ctx, ""); err != ErrSessionExpired {
			t.Fatalf("empty token: %v, want session_expired", err)
		}
	})

	t.Run("session lookup failure is indistinguishable from a dead session", func(t *testing.T) {
		svc, _, sessions, _, token := newAuthedService(t)
		sessions.byHashErr = errStoreDown
		if _, _, err := svc.Authenticate(ctx, token); err != ErrSessionExpired {
			t.Fatalf("err = %v, want session_expired", err)
		}
	})

	t.Run("user lookup failure is indistinguishable from a dead session", func(t *testing.T) {
		svc, users, _, _, token := newAuthedService(t)
		users.getErr = errStoreDown
		if _, _, err := svc.Authenticate(ctx, token); err != ErrSessionExpired {
			t.Fatalf("err = %v, want session_expired", err)
		}
	})

	t.Run("a disabled account kills its live session", func(t *testing.T) {
		svc, users, _, user, token := newAuthedService(t)
		live, err := users.Get(ctx, user.ID)
		if err != nil || live == nil {
			t.Fatalf("read user: %v", err)
		}
		live.Status = StatusDisabled
		if err := users.Update(ctx, live); err != nil {
			t.Fatalf("disable: %v", err)
		}
		if _, _, err := svc.Authenticate(ctx, token); err != ErrUserDisabled {
			t.Fatalf("err = %v, want user_disabled", err)
		}
	})

	t.Run("authorization load failure is reported, not swallowed", func(t *testing.T) {
		users := &errUserStore{memUserStore: newMemUserStore()}
		sessions := &errSessionStore{memSessionStore: newMemSessionStore()}
		// A nil store inside authz.New builds an owner-only principal; a store that
		// fails to answer has to surface instead of degrading to "no permissions".
		svc := New(users, sessions, &memOAuthStore{}, nil, authz.New(failingAuthz{err: errStoreDown}), Options{TTL: time.Hour})
		if _, err := svc.Register(ctx, "u@example.com", "correct horse", "U"); err != nil {
			t.Fatalf("register: %v", err)
		}
		_, token, _, err := svc.Login(ctx, "u@example.com", "correct horse", "", "")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if _, _, err := svc.Authenticate(ctx, token); !errors.Is(err, errStoreDown) {
			t.Fatalf("err = %v, want %v", err, errStoreDown)
		}
	})
}

func TestLogoutSessionsAndRevokeFailurePaths(t *testing.T) {
	ctx := context.Background()

	// Logging out is best effort: an instance without stores, an empty token and
	// an unknown token all answer "done", because the cookie is cleared anyway.
	bare := New(nil, nil, nil, nil, nil, Options{})
	if err := bare.Logout(ctx, "token"); err != nil {
		t.Fatalf("logout without a store: %v", err)
	}
	svc, _, sessions, user, token := newAuthedService(t)
	if err := svc.Logout(ctx, ""); err != nil {
		t.Fatalf("logout without a token: %v", err)
	}
	if err := svc.Logout(ctx, "unknown-token"); err != nil {
		t.Fatalf("logout of an unknown token: %v", err)
	}

	// A lookup that fails looks like an already dead session, so the cookie is
	// cleared rather than the request failing.
	sessions.byHashErr = errStoreDown
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("logout with a failing lookup: %v", err)
	}
	sessions.byHashErr = nil

	// Revoking a single owned session is the "kick this device" path.
	live, err := sessions.ListForUser(ctx, user.ID)
	if err != nil || len(live) != 1 {
		t.Fatalf("live sessions = %+v, err=%v", live, err)
	}
	if err := svc.RevokeSession(ctx, user.ID, live[0].ID); err != nil {
		t.Fatalf("revoke own session: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, token); err != ErrSessionExpired {
		t.Fatalf("after revoke: %v, want session_expired", err)
	}
	// A second revoke finds nothing live under that id.
	if err := svc.RevokeSession(ctx, user.ID, live[0].ID); err != ErrNotFound {
		t.Fatalf("revoking an already revoked session: %v, want not_found", err)
	}

	if got, err := bare.Sessions(ctx, "user-1"); err != nil || got != nil {
		t.Fatalf("list without a store = %+v, err=%v, want an empty answer", got, err)
	}
	if err := bare.RevokeSession(ctx, "user-1", "session-1"); err != ErrNotFound {
		t.Fatalf("revoke without a store: %v, want not_found", err)
	}

	t.Run("list failure is reported", func(t *testing.T) {
		svc, _, sessions, user, _ := newAuthedService(t)
		sessions.listErr = errStoreDown
		if _, err := svc.Sessions(ctx, user.ID); !errors.Is(err, errStoreDown) {
			t.Fatalf("sessions: %v, want %v", err, errStoreDown)
		}
		// RevokeSession lists first to prove ownership, so the same failure stops
		// it before any write.
		if err := svc.RevokeSession(ctx, user.ID, "session-x"); !errors.Is(err, errStoreDown) {
			t.Fatalf("revoke: %v, want %v", err, errStoreDown)
		}
	})

	t.Run("revoke-all failure is reported", func(t *testing.T) {
		svc, _, sessions, user, _ := newAuthedService(t)
		sessions.revokeForUserErr = errStoreDown
		if err := svc.RevokeSession(ctx, user.ID, ""); !errors.Is(err, errStoreDown) {
			t.Fatalf("revoke all: %v, want %v", err, errStoreDown)
		}
	})

	t.Run("single revoke failure is reported", func(t *testing.T) {
		svc, _, sessions, user, _ := newAuthedService(t)
		live, err := sessions.ListForUser(ctx, user.ID)
		if err != nil || len(live) != 1 {
			t.Fatalf("live sessions = %+v, err=%v", live, err)
		}
		sessions.revokeErr = errStoreDown
		if err := svc.RevokeSession(ctx, user.ID, live[0].ID); !errors.Is(err, errStoreDown) {
			t.Fatalf("revoke: %v, want %v", err, errStoreDown)
		}
	})
}

// TestCompleteOAuthStoreFailures covers the store arms of a third-party login.
// WeChat is the provider used here because its API can be pointed at an httptest
// server, so the exchange itself needs no network and no credentials — the
// failures under test are all after the provider has answered.
func TestCompleteOAuthStoreFailures(t *testing.T) {
	ctx := context.Background()
	withWechatServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/sns/oauth2/access_token"):
			w.Write([]byte(`{"access_token":"AT","openid":"OPENID","unionid":"UNIONID","expires_in":7200}`))
		case strings.Contains(r.URL.Path, "/sns/userinfo"):
			w.Write([]byte(`{"openid":"OPENID","nickname":"微信用户","unionid":"UNIONID"}`))
		}
	})
	cfg := OAuthConfig{Enabled: true, WeChat: ProviderConfig{ClientID: "wx-app-id", ClientSecret: "wx-app-secret"}}
	complete := func(svc *Service) error {
		t.Helper()
		_, err := svc.CompleteOAuth(ctx, "wechat", "code", "verifier", "https://hub.example.com/callback", "curl", "10.0.0.1")
		return err
	}

	t.Run("no stores", func(t *testing.T) {
		if err := complete(New(nil, newMemSessionStore(), nil, nil, nil, Options{OAuth: cfg, TTL: time.Hour})); err == nil {
			t.Fatal("a service without user or oauth stores must refuse the login")
		}
	})

	t.Run("account creation fails", func(t *testing.T) {
		users := &errUserStore{memUserStore: newMemUserStore(), createErr: errStoreDown}
		svc := New(users, newMemSessionStore(), &errOAuthStore{memOAuthStore: &memOAuthStore{}}, nil, nil,
			Options{OAuth: cfg, TTL: time.Hour})
		if err := complete(svc); !errors.Is(err, errStoreDown) {
			t.Fatalf("err = %v, want %v", err, errStoreDown)
		}
	})

	t.Run("binding the provider identity fails", func(t *testing.T) {
		// The account can be created, so the failure lands on Link: a login that
		// cannot record the binding has to fail rather than open a session.
		oauth := &errOAuthStore{memOAuthStore: &memOAuthStore{}, linkErr: errStoreDown}
		svc := New(newMemUserStore(), newMemSessionStore(), oauth, nil, nil, Options{OAuth: cfg, TTL: time.Hour})
		if err := complete(svc); !errors.Is(err, errStoreDown) {
			t.Fatalf("err = %v, want %v", err, errStoreDown)
		}
	})

	t.Run("a binding whose account is unreadable fails", func(t *testing.T) {
		users := &errUserStore{memUserStore: newMemUserStore(), getErr: errStoreDown}
		oauth := &errOAuthStore{memOAuthStore: &memOAuthStore{rows: []OAuthAccount{{
			ID: "oauth-1", UserID: "user-gone", Provider: "wechat", Subject: "UNIONID",
		}}}}
		svc := New(users, newMemSessionStore(), oauth, nil, nil, Options{OAuth: cfg, TTL: time.Hour})
		if err := complete(svc); !errors.Is(err, errStoreDown) {
			t.Fatalf("err = %v, want %v", err, errStoreDown)
		}
	})
}

// TestVerifyPasswordRejectsMalformedEncodings pins the decode order: a hash
// crafted with a broken salt or key must answer false, never panic on a nil
// slice or allocate from a negative length.
func TestVerifyPasswordRejectsMalformedEncodings(t *testing.T) {
	for _, raw := range []string{
		"pbkdf2-sha256$210000$***$AAAA",   // salt is not base64url
		"pbkdf2-sha256$210000$c2FsdA$***", // key is not base64url
		"pbkdf2-sha256$0$c2FsdA$AAAA",     // zero iterations would be a free hash
		"pbkdf2-sha256$-1$c2FsdA$AAAA",    // negative iterations
		"pbkdf2-sha256$$c2FsdA$AAAA",      // missing iteration count
		"pbkdf2-sha1$210000$c2FsdA$AAAA",  // a different KDF is not ours
	} {
		if VerifyPassword(raw, "whatever") {
			t.Fatalf("hash %q must not verify", raw)
		}
	}
}
