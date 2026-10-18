// Package auth is the identity layer of agenthub: email + password accounts,
// OAuth bindings, and server side sessions carried by an HttpOnly cookie.
//
// Sessions live in PostgreSQL (never in a stateless token) so an administrator
// can revoke one and the very next request stops working. Only derived values
// are persisted: password_hash carries its own salt and iteration count, and a
// session row stores the hash of the opaque cookie value, so a database leak
// cannot be replayed as a login.
//
// Storage is declared here as ports (UserStore / SessionStore / OAuthStore) and
// implemented in internal/store, which keeps this package — and therefore its
// unit tests — free of GORM.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agenthub/internal/authz"
)

// Status values of a user.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

var (
	// ErrEmailTaken is returned when an address is already registered.
	ErrEmailTaken = errors.New("email_taken")
	// ErrInvalidEmail rejects malformed addresses.
	ErrInvalidEmail = errors.New("invalid_email")
	// ErrUserDisabled is returned when a disabled account tries to log in.
	ErrUserDisabled = errors.New("user_disabled")
	// ErrNotFound is returned when a user or session does not exist.
	ErrNotFound = errors.New("not_found")
	// ErrSessionExpired is returned when the cookie points at a dead session.
	ErrSessionExpired = errors.New("session_expired")
	// ErrPasswordRequired rejects making the last credential disappear.
	ErrPasswordRequired = errors.New("password_required")
)

// User is one identity. PasswordHash is never rendered by the API: handlers
// copy the fields they need into a response struct.
type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	Status       string
	IsOwner      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DisabledAt   *time.Time
	Roles        []string
	Groups       []string
}

// Session is one authenticated browser.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	IP         string
	RevokedAt  *time.Time
}

// OAuthAccount binds a provider identity to a local user.
type OAuthAccount struct {
	ID        string
	UserID    string
	Provider  string
	Subject   string
	Email     string
	CreatedAt time.Time
}

// UserStore persists identities.
type UserStore interface {
	Create(ctx context.Context, u *User) error
	Get(ctx context.Context, id string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	Update(ctx context.Context, u *User) error
	List(ctx context.Context, query string) ([]User, error)
	Count(ctx context.Context) (int64, error)
	Delete(ctx context.Context, id string) error
}

// SessionStore persists sessions, keyed by the hash of the cookie value.
type SessionStore interface {
	Create(ctx context.Context, s *Session, tokenHash string) error
	ByTokenHash(ctx context.Context, tokenHash string) (*Session, error)
	Touch(ctx context.Context, id string, at time.Time) error
	Revoke(ctx context.Context, id string) error
	RevokeForUser(ctx context.Context, userID string) error
	ListForUser(ctx context.Context, userID string) ([]Session, error)
	DeleteExpired(ctx context.Context, now time.Time) error
}

// OAuthStore persists third party bindings.
type OAuthStore interface {
	Find(ctx context.Context, provider, subject string) (*OAuthAccount, error)
	Link(ctx context.Context, a *OAuthAccount) error
	ListForUser(ctx context.Context, userID string) ([]OAuthAccount, error)
}

// Options configures the cookie, the session lifetime and the OAuth providers.
type Options struct {
	CookieName string
	TTL        time.Duration
	Secure     bool
	OAuth      OAuthConfig
}

// Service is the identity facade used by the HTTP layer.
type Service struct {
	users    UserStore
	sessions SessionStore
	oauth    OAuthStore
	authz    *authz.Service
	opts     Options
	oauthCfg OAuthConfig
}

// New builds the service. Every store may be nil, which keeps the package inert
// for tests that only exercise the pure rules.
func New(users UserStore, sessions SessionStore, oauth OAuthStore, az *authz.Service, opts Options) *Service {
	if opts.CookieName == "" {
		opts.CookieName = "agenthub_session"
	}
	if opts.TTL <= 0 {
		opts.TTL = 12 * time.Hour
	}
	return &Service{users: users, sessions: sessions, oauth: oauth, authz: az, opts: opts, oauthCfg: opts.OAuth}
}

// Options exposes the effective cookie configuration.
func (s *Service) Options() Options { return s.opts }

// Register creates an email + password account. The first account of an instance
// becomes the owner so a fresh deployment always has someone with every
// permission (BootstrapAdmin covers the .env case).
func (s *Service) Register(ctx context.Context, email, password, name string) (*User, error) {
	email = NormalizeEmail(email)
	if !ValidEmail(email) {
		return nil, ErrInvalidEmail
	}
	if err := CheckPassword(password); err != nil {
		return nil, err
	}
	if s.users == nil {
		return nil, errors.New("auth: no user store configured")
	}
	if existing, err := s.users.GetByEmail(ctx, email); err == nil && existing != nil {
		return nil, ErrEmailTaken
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	count, err := s.users.Count(ctx)
	if err != nil {
		return nil, err
	}
	u := &User{
		ID:           NewID("user"),
		Email:        email,
		Name:         strings.TrimSpace(name),
		PasswordHash: hash,
		Status:       StatusActive,
		IsOwner:      count == 0,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Login verifies email + password and opens a session.
func (s *Service) Login(ctx context.Context, email, password, userAgent, ip string) (*User, string, *Session, error) {
	email = NormalizeEmail(email)
	if s.users == nil || s.sessions == nil {
		return nil, "", nil, errors.New("auth: stores not configured")
	}
	u, err := s.users.GetByEmail(ctx, email)
	if err != nil || u == nil {
		return nil, "", nil, ErrInvalidCredentials
	}
	if u.PasswordHash == "" {
		return nil, "", nil, ErrNoPassword
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return nil, "", nil, ErrInvalidCredentials
	}
	if u.Status != StatusActive {
		return nil, "", nil, ErrUserDisabled
	}
	sess, token, err := s.openSession(ctx, u.ID, userAgent, ip)
	if err != nil {
		return nil, "", nil, err
	}
	return u, token, sess, nil
}

// BootstrapAdmin upserts the administrator described in the environment. It is
// idempotent: the address is matched by email and the password is only ever
// overwritten when the environment still carries one, so restarting with the
// same .env never resets a password someone changed in the console.
func (s *Service) BootstrapAdmin(ctx context.Context, email, password, name string) error {
	if s.users == nil || email == "" {
		return nil
	}
	email = NormalizeEmail(email)
	if !ValidEmail(email) {
		return fmt.Errorf("%w: %s", ErrInvalidEmail, email)
	}
	existing, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		return err
	}
	if existing != nil {
		// Promote an existing account to owner but never touch its password or
		// status: an operator may have changed both.
		if !existing.IsOwner {
			existing.IsOwner = true
			return s.users.Update(ctx, existing)
		}
		return nil
	}
	if password == "" {
		return fmt.Errorf("%w: bootstrap admin needs a password on first boot", ErrPasswordRequired)
	}
	if err := CheckPassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if name == "" {
		name = "Administrator"
	}
	return s.users.Create(ctx, &User{
		ID:           NewID("user"),
		Email:        email,
		Name:         name,
		PasswordHash: hash,
		Status:       StatusActive,
		IsOwner:      true,
	})
}

// Principal resolves the identity of a request: reads the cookie, loads the
// session, and expands the user into permissions.
func (s *Service) Principal(ctx context.Context, r *http.Request) (*authz.Principal, *Session, error) {
	if s.sessions == nil {
		return nil, nil, nil
	}
	token := sessionTokenFromRequest(r, s.opts.CookieName)
	if token == "" {
		return nil, nil, nil
	}
	return s.Authenticate(ctx, token)
}

// Authenticate resolves an opaque session token.
func (s *Service) Authenticate(ctx context.Context, token string) (*authz.Principal, *Session, error) {
	if s.sessions == nil || token == "" {
		return nil, nil, ErrSessionExpired
	}
	sess, err := s.sessions.ByTokenHash(ctx, HashToken(token))
	if err != nil || sess == nil {
		return nil, nil, ErrSessionExpired
	}
	if sess.RevokedAt != nil || !sess.ExpiresAt.After(time.Now().UTC()) {
		return nil, nil, ErrSessionExpired
	}
	u, err := s.users.Get(ctx, sess.UserID)
	if err != nil || u == nil {
		return nil, nil, ErrSessionExpired
	}
	if u.Status != StatusActive {
		return nil, nil, ErrUserDisabled
	}
	p, err := s.authz.Load(ctx, u.ID, u.Email, u.Name, u.IsOwner)
	if err != nil {
		return nil, nil, err
	}
	return p, sess, nil
}

// SetCookie writes the session cookie. Secure is driven by configuration so a
// plain http deployment behind a reverse proxy still works.
func (s *Service) SetCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.opts.CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   s.opts.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie removes the session cookie on logout.
func (s *Service) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.opts.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.opts.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Logout revokes the session behind the cookie and clears it.
func (s *Service) Logout(ctx context.Context, token string) error {
	if s.sessions == nil || token == "" {
		return nil
	}
	sess, err := s.sessions.ByTokenHash(ctx, HashToken(token))
	if err != nil || sess == nil {
		return nil
	}
	return s.sessions.Revoke(ctx, sess.ID)
}

// Sessions lists the live sessions of a user (for "kick this device").
func (s *Service) Sessions(ctx context.Context, userID string) ([]Session, error) {
	if s.sessions == nil {
		return nil, nil
	}
	return s.sessions.ListForUser(ctx, userID)
}

// RevokeSession revokes one session; revokeAll drops every session of a user.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID string) error {
	if s.sessions == nil {
		return ErrNotFound
	}
	if sessionID == "" {
		return s.sessions.RevokeForUser(ctx, userID)
	}
	// Ownership first: only the sessions of this user can be revoked here.
	all, err := s.sessions.ListForUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, sess := range all {
		if sess.ID == sessionID && sess.RevokedAt == nil {
			return s.sessions.Revoke(ctx, sessionID)
		}
	}
	return ErrNotFound
}

// Users exposes the user port for the admin handlers.
func (s *Service) Users() UserStore { return s.users }

// Authz exposes the authorization service.
func (s *Service) Authz() *authz.Service { return s.authz }

func (s *Service) openSession(ctx context.Context, userID, userAgent, ip string) (*Session, string, error) {
	token, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:         NewID("ses"),
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.opts.TTL),
		LastSeenAt: now,
		UserAgent:  truncate(userAgent, 256),
		IP:         ip,
	}
	if err := s.sessions.Create(ctx, sess, HashToken(token)); err != nil {
		return nil, "", err
	}
	return sess, token, nil
}

func sessionTokenFromRequest(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil && c.Value != "" {
		return c.Value
	}
	// The console always sends cookies, but scripts and curl can pass the
	// header explicitly; both end up in the same session row.
	if v := r.Header.Get("X-AgentHub-Session"); v != "" {
		return v
	}
	return ""
}

// SessionTokenFromRequest mirrors the cookie/header lookup for handlers that
// need to clear or rotate a session.
func (s *Service) SessionTokenFromRequest(r *http.Request) string {
	return sessionTokenFromRequest(r, s.opts.CookieName)
}

// HashToken is the stored form of a session token: the raw value never lands in
// the database.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewToken returns an opaque, URL-safe session value.
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// NewID builds a prefixed identifier.
func NewID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is fatal anyway; a time based fallback keeps the
		// ID unique within the process.
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf)
}

// NormalizeEmail trims and lowercases an address.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidEmail is a deliberately small structural check: enough to catch typos,
// not to adjudicate every RFC edge case.
func ValidEmail(email string) bool {
	if email == "" || strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
