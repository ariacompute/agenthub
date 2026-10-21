package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// password hashing
// ---------------------------------------------------------------------------

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// The encoding carries everything needed to verify later, including the
	// iteration count, so a future upgrade can verify old hashes.
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatalf("hash = %q, want the pbkdf2-sha256 encoding", hash)
	}
	if hash == "correct horse battery" {
		t.Fatal("the password must not be stored verbatim")
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("the correct password must verify")
	}
	if VerifyPassword(hash, "correct horse batterie") {
		t.Fatal("a wrong password must not verify")
	}
	// Salting: the same password twice produces different rows.
	other, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if other == hash {
		t.Fatal("two hashes of one password must differ: the salt is random")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "not-a-hash", "pbkdf2-sha256$abc", "pbkdf2-sha256$x$y$z"} {
		if VerifyPassword(raw, "whatever") {
			t.Fatalf("hash %q must not verify", raw)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	if err := CheckPassword("1234567"); err == nil {
		t.Fatal("a password shorter than the minimum must be rejected")
	}
	if err := CheckPassword("correct horse"); err != nil {
		t.Fatalf("a long enough password must pass: %v", err)
	}
	if err := CheckPassword(""); err == nil {
		t.Fatal("an empty password must be rejected")
	}
}

func TestValidEmail(t *testing.T) {
	for _, tc := range []struct {
		email string
		want  bool
	}{
		{"a@example.com", true},
		{"A@Example.COM ", true}, // trimmed and lowercased by the caller
		{"", false},
		{"nope", false},
		{"@example.com", false},
		{"a@", false},
		{"a@example", false},
		{"a b@example.com", false},
		{"a@.example.com", false},
	} {
		if got := ValidEmail(NormalizeEmail(tc.email)); got != tc.want {
			t.Fatalf("ValidEmail(%q) = %v, want %v", tc.email, got, tc.want)
		}
	}
	if NormalizeEmail("  Owner@Example.COM ") != "owner@example.com" {
		t.Fatal("NormalizeEmail must trim and lowercase")
	}
}

// ---------------------------------------------------------------------------
// in-memory stores
// ---------------------------------------------------------------------------

type memUserStore struct {
	byID    map[string]*User
	byEmail map[string]string
}

func newMemUserStore() *memUserStore {
	return &memUserStore{byID: map[string]*User{}, byEmail: map[string]string{}}
}

func (s *memUserStore) Create(_ context.Context, u *User) error {
	if _, ok := s.byEmail[u.Email]; ok {
		return ErrEmailTaken
	}
	cp := *u
	s.byID[u.ID] = &cp
	s.byEmail[u.Email] = u.ID
	return nil
}
func (s *memUserStore) Get(_ context.Context, id string) (*User, error) {
	if u, ok := s.byID[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, nil
}
func (s *memUserStore) GetByEmail(_ context.Context, email string) (*User, error) {
	id, ok := s.byEmail[NormalizeEmail(email)]
	if !ok {
		return nil, nil
	}
	cp := *s.byID[id]
	return &cp, nil
}
func (s *memUserStore) Update(_ context.Context, u *User) error {
	cp := *u
	s.byID[u.ID] = &cp
	s.byEmail[u.Email] = u.ID
	return nil
}
func (s *memUserStore) List(_ context.Context, _ string) ([]User, error) {
	out := []User{}
	for _, u := range s.byID {
		out = append(out, *u)
	}
	return out, nil
}
func (s *memUserStore) Count(_ context.Context) (int64, error) { return int64(len(s.byID)), nil }
func (s *memUserStore) Delete(_ context.Context, id string) error {
	if u, ok := s.byID[id]; ok {
		u.Status = StatusDisabled
	}
	return nil
}
func (s *memUserStore) RecordLoginFailure(_ context.Context, _ string) error { return nil }
func (s *memUserStore) ResetLoginAttempts(_ context.Context, _ string) error { return nil }
func (s *memUserStore) LockedUntil(_ context.Context, _ string) (*time.Time, error) {
	return nil, nil
}

type memSessionStore struct {
	byHash map[string]*Session
	byID   map[string]*Session
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{byHash: map[string]*Session{}, byID: map[string]*Session{}}
}

func (s *memSessionStore) Create(_ context.Context, sess *Session, tokenHash string) error {
	cp := *sess
	s.byHash[tokenHash] = &cp
	s.byID[sess.ID] = &cp
	return nil
}
func (s *memSessionStore) ByTokenHash(_ context.Context, tokenHash string) (*Session, error) {
	if sess, ok := s.byHash[tokenHash]; ok {
		cp := *sess
		return &cp, nil
	}
	return nil, nil
}
func (s *memSessionStore) Touch(_ context.Context, id string, at time.Time) error {
	if sess, ok := s.byID[id]; ok {
		sess.LastSeenAt = at
	}
	return nil
}
func (s *memSessionStore) Revoke(_ context.Context, id string) error {
	if sess, ok := s.byID[id]; ok {
		now := time.Now().UTC()
		sess.RevokedAt = &now
	}
	return nil
}
func (s *memSessionStore) RevokeForUser(_ context.Context, userID string) error {
	now := time.Now().UTC()
	for _, sess := range s.byID {
		if sess.UserID == userID && sess.RevokedAt == nil {
			sess.RevokedAt = &now
		}
	}
	return nil
}
func (s *memSessionStore) ListForUser(_ context.Context, userID string) ([]Session, error) {
	out := []Session{}
	for _, sess := range s.byID {
		if sess.UserID == userID {
			out = append(out, *sess)
		}
	}
	return out, nil
}
func (s *memSessionStore) DeleteExpired(_ context.Context, now time.Time) error {
	for id, sess := range s.byID {
		if sess.ExpiresAt.Before(now) {
			delete(s.byID, id)
		}
	}
	return nil
}

type memOAuthStore struct{ rows []OAuthAccount }

func (s *memOAuthStore) Find(_ context.Context, provider, subject string) (*OAuthAccount, error) {
	for i := range s.rows {
		if s.rows[i].Provider == provider && s.rows[i].Subject == subject {
			cp := s.rows[i]
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *memOAuthStore) Link(_ context.Context, a *OAuthAccount) error {
	s.rows = append(s.rows, *a)
	return nil
}
func (s *memOAuthStore) ListForUser(_ context.Context, userID string) ([]OAuthAccount, error) {
	out := []OAuthAccount{}
	for _, a := range s.rows {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func newTestService() (*Service, *memUserStore, *memSessionStore) {
	users, sessions := newMemUserStore(), newMemSessionStore()
	return New(users, sessions, &memOAuthStore{}, nil, nil, Options{TTL: time.Hour}), users, sessions
}

// ---------------------------------------------------------------------------
// registration, login, sessions
// ---------------------------------------------------------------------------

// TestFirstUserBecomesOwner is what keeps a fresh instance usable: whoever
// registers first can grant permissions to everybody after them.
func TestFirstUserBecomesOwner(t *testing.T) {
	svc, _, _ := newTestService()
	first, err := svc.Register(context.Background(), "Owner@Example.com", "correct horse", "Owner")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !first.IsOwner {
		t.Fatal("the first account must be the owner")
	}
	if first.Email != "owner@example.com" {
		t.Fatalf("email = %q, want normalised", first.Email)
	}
	if first.PasswordHash == "" || first.PasswordHash == "correct horse" {
		t.Fatal("the password must be stored as a hash")
	}
	second, err := svc.Register(context.Background(), "second@example.com", "correct horse", "")
	if err != nil {
		t.Fatalf("register second: %v", err)
	}
	if second.IsOwner {
		t.Fatal("only the first account is the owner")
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	svc, _, _ := newTestService()
	if _, err := svc.Register(context.Background(), "nope", "correct horse", ""); err != ErrInvalidEmail {
		t.Fatalf("malformed email: %v", err)
	}
	if _, err := svc.Register(context.Background(), "a@example.com", "short", ""); err != ErrWeakPassword {
		t.Fatalf("weak password: %v", err)
	}
	if _, err := svc.Register(context.Background(), "a@example.com", "correct horse", ""); err != nil {
		t.Fatalf("register: %v", err)
	}
	// The same address is a conflict, whatever its case or padding.
	if _, err := svc.Register(context.Background(), " A@Example.com ", "correct horse", ""); err != ErrEmailTaken {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestLoginAndSessionLifecycle(t *testing.T) {
	svc, _, sessions := newTestService()
	if _, err := svc.Register(context.Background(), "u@example.com", "correct horse", "U"); err != nil {
		t.Fatalf("register: %v", err)
	}
	u, token, sess, err := svc.Login(context.Background(), "u@example.com", "correct horse", "curl", "10.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if u.ID == "" || token == "" || sess == nil {
		t.Fatalf("login returned %v / %q / %v", u, token, sess)
	}
	// The raw token is never what lands in the database.
	stored, err := sessions.ByTokenHash(context.Background(), HashToken(token))
	if err != nil || stored == nil {
		t.Fatalf("session not stored under its hash: %v", err)
	}
	if stored.ID != sess.ID {
		t.Fatal("the stored session must be the opened one")
	}
	if _, err := sessions.ByTokenHash(context.Background(), token); err == nil {
		if sessions.byHash[token] != nil {
			t.Fatal("the raw token must not be a lookup key")
		}
	}

	// Authenticating with the token resolves the identity again.
	p, got, err := svc.Authenticate(context.Background(), token)
	if err != nil || p == nil || got == nil {
		t.Fatalf("authenticate: %v (p=%v)", err, p)
	}
	if p.UserID != u.ID || p.Email != u.Email {
		t.Fatalf("principal = %+v", p)
	}
	if _, _, err := svc.Authenticate(context.Background(), "not-a-token"); err != ErrSessionExpired {
		t.Fatalf("unknown token: %v, want session_expired", err)
	}

	// Logout kills it immediately: the next request is anonymous.
	if err := svc.Logout(context.Background(), token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := svc.Authenticate(context.Background(), token); err != ErrSessionExpired {
		t.Fatalf("after logout: %v, want session_expired", err)
	}
}

func TestLoginRejectsWrongAndDisabled(t *testing.T) {
	svc, users, _ := newTestService()
	if _, err := svc.Register(context.Background(), "u@example.com", "correct horse", "U"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "wrong password", "", ""); err != ErrInvalidCredentials {
		t.Fatalf("wrong password: %v, want invalid_credentials", err)
	}
	if _, _, _, err := svc.Login(context.Background(), "nobody@example.com", "correct horse", "", ""); err != ErrInvalidCredentials {
		t.Fatalf("unknown user: %v, want invalid_credentials", err)
	}

	u, _ := users.GetByEmail(context.Background(), "u@example.com")
	u.Status = StatusDisabled
	if err := users.Update(context.Background(), u); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "correct horse", "", ""); err != ErrUserDisabled {
		t.Fatalf("disabled: %v, want user_disabled", err)
	}
	// A disabled user's session stops working on the next request, not at the
	// end of its lifetime.
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "correct horse", "", ""); err != ErrUserDisabled {
		t.Fatalf("disabled again: %v", err)
	}
}

// TestRevokeSessionIsOwnershipChecked stops one user from revoking another's
// session by guessing ids.
func TestRevokeSessionIsOwnershipChecked(t *testing.T) {
	svc, _, _ := newTestService()
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if _, err := svc.Register(context.Background(), email, "correct horse", ""); err != nil {
			t.Fatalf("register %s: %v", email, err)
		}
	}
	ua, ta, sa, err := svc.Login(context.Background(), "a@example.com", "correct horse", "", "")
	if err != nil {
		t.Fatalf("login a: %v", err)
	}
	ub, _, _, err := svc.Login(context.Background(), "b@example.com", "correct horse", "", "")
	if err != nil {
		t.Fatalf("login b: %v", err)
	}
	if err := svc.RevokeSession(context.Background(), ub.ID, sa.ID); err != ErrNotFound {
		t.Fatalf("revoking somebody else's session: %v, want not_found", err)
	}
	if _, _, err := svc.Authenticate(context.Background(), ta); err != nil {
		t.Fatalf("a's session must survive: %v", err)
	}
	// Empty id means "every session of this user".
	if err := svc.RevokeSession(context.Background(), ua.ID, ""); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if _, _, err := svc.Authenticate(context.Background(), ta); err != ErrSessionExpired {
		t.Fatalf("after revoke all: %v, want session_expired", err)
	}
}

// TestBootstrapAdminIsIdempotent is what makes restarting with the same .env
// safe: the account is promoted, never reset.
func TestBootstrapAdminIsIdempotent(t *testing.T) {
	svc, users, _ := newTestService()
	if err := svc.BootstrapAdmin(context.Background(), "admin@example.com", "correct horse", "Admin"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	u, _ := users.GetByEmail(context.Background(), "admin@example.com")
	if u == nil || !u.IsOwner {
		t.Fatal("the bootstrap administrator must be the owner")
	}
	firstHash := u.PasswordHash

	// A second boot with a different password must not overwrite it: an
	// operator may have changed it in the console.
	if err := svc.BootstrapAdmin(context.Background(), "admin@example.com", "another password", "Admin"); err != nil {
		t.Fatalf("bootstrap again: %v", err)
	}
	u, _ = users.GetByEmail(context.Background(), "admin@example.com")
	if u.PasswordHash != firstHash {
		t.Fatal("an existing password must never be overwritten by the bootstrap")
	}
	if !VerifyPassword(u.PasswordHash, "correct horse") {
		t.Fatal("the original password must still work")
	}

	// Without an address there is nothing to do, and without a password there
	// is no account to create.
	if err := svc.BootstrapAdmin(context.Background(), "", "", ""); err != nil {
		t.Fatalf("empty bootstrap: %v", err)
	}
	if err := svc.BootstrapAdmin(context.Background(), "second@example.com", "", ""); err == nil {
		t.Fatal("a first boot without a password must fail loudly")
	}
	if err := svc.BootstrapAdmin(context.Background(), "nope", "correct horse", ""); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("malformed bootstrap address: %v, want invalid_email", err)
	}
}

// TestSessionTokenFromRequest accepts the cookie and the header form, which is
// what curl and scripts/test-api.sh use.
func TestSessionTokenFromRequest(t *testing.T) {
	svc, _, _ := newTestService()
	svc.opts.CookieName = "agenthub_session"

	withCookie := httptest.NewRequest(http.MethodGet, "/", nil)
	withCookie.AddCookie(&http.Cookie{Name: "agenthub_session", Value: "abc"})
	if got := svc.SessionTokenFromRequest(withCookie); got != "abc" {
		t.Fatalf("cookie token = %q", got)
	}

	withHeader := httptest.NewRequest(http.MethodGet, "/", nil)
	withHeader.Header.Set("X-AgentHub-Session", "def")
	if got := svc.SessionTokenFromRequest(withHeader); got != "def" {
		t.Fatalf("header token = %q", got)
	}

	if got := svc.SessionTokenFromRequest(httptest.NewRequest(http.MethodGet, "/", nil)); got != "" {
		t.Fatalf("no credentials: %q", got)
	}
}

func TestNewTokenIsOpaqueAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if len(tok) != 64 {
			t.Fatalf("token length = %d, want 64 hex chars", len(tok))
		}
		if seen[tok] {
			t.Fatal("tokens must not repeat")
		}
		seen[tok] = true
	}
}

// lockoutUsers is an in-memory UserStore that also enforces the brute-force
// lockout so the Login control flow can be exercised without PostgreSQL.
type lockoutUsers struct {
	*memUserStore
	mu       sync.Mutex
	attempts map[string]int
	locked   map[string]time.Time
}

func newLockoutUsers() *lockoutUsers {
	return &lockoutUsers{
		memUserStore: newMemUserStore(),
		attempts:     map[string]int{},
		locked:       map[string]time.Time{},
	}
}

func (s *lockoutUsers) RecordLoginFailure(_ context.Context, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[email]++
	if s.attempts[email] >= MaxLoginAttempts {
		s.locked[email] = time.Now().UTC().Add(AccountLockMinutes * time.Minute)
	}
	return nil
}

func (s *lockoutUsers) ResetLoginAttempts(_ context.Context, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, email)
	delete(s.locked, email)
	return nil
}

func (s *lockoutUsers) LockedUntil(_ context.Context, email string) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.locked[email]; ok {
		c := until
		return &c, nil
	}
	return nil, nil
}

func TestLoginLockout(t *testing.T) {
	users := newLockoutUsers()
	sessions := newMemSessionStore()
	svc := New(users, sessions, &memOAuthStore{}, nil, nil, Options{TTL: time.Hour})
	if _, err := svc.Register(context.Background(), "u@example.com", "correct horse", "U"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Five wrong passwords are still ordinary invalid_credentials.
	for i := 0; i < MaxLoginAttempts; i++ {
		if _, _, _, err := svc.Login(context.Background(), "u@example.com", "wrong", "", ""); err != ErrInvalidCredentials {
			t.Fatalf("failure %d: %v, want invalid_credentials", i, err)
		}
	}
	// The next attempt — wrong or right — is blocked by the lockout.
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "wrong", "", ""); err != ErrAccountLocked {
		t.Fatalf("6th attempt: %v, want account_locked", err)
	}
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "correct horse", "", ""); err != ErrAccountLocked {
		t.Fatalf("correct while locked: %v, want account_locked", err)
	}

	// Clearing the lock (an operator unlocking, or the window passing) lets a
	// correct password in and resets the counter.
	users.locked = map[string]time.Time{}
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "correct horse", "curl", "10.0.0.1"); err != nil {
		t.Fatalf("after unlock: %v, want success", err)
	}
	// One more failure after the reset must not immediately re-lock.
	if _, _, _, err := svc.Login(context.Background(), "u@example.com", "wrong", "", ""); err != ErrInvalidCredentials {
		t.Fatalf("post-reset failure: %v, want invalid_credentials", err)
	}
}
