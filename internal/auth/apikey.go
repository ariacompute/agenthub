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

// API key errors. They mirror the codes in internal/httputil: the console
// localises them, so nothing here carries operator-facing prose.
var (
	ErrAPIKeyNotFound     = errors.New("api_key_not_found")
	ErrAPIKeyInvalidScope = errors.New("api_key_invalid_scope")
	ErrAPIKeyExpired      = errors.New("api_key_expired")
	ErrAPIKeyRevoked      = errors.New("api_key_revoked")
	ErrAPIKeyNameRequired = errors.New("api_key_name_required")
)

// APIKeyPrefix is the literal a raw key begins with, so it is recognisable in a
// config file and on the wire.
const APIKeyPrefix = "ahk_"

// APIKey is a long-lived bearer credential. Scopes is the permission subset the
// key may use; it is always intersected with the owner's own permissions when
// the key is resolved, so a key can never exceed what the owner can do.
type APIKey struct {
	ID         string
	UserID     string
	Name       string
	Prefix     string // "ahk_" + 8 hex, for display only
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	CreatedBy  string
}

// Active reports whether the key is usable right now: not revoked and, if it has
// an expiry, not past it.
func (k *APIKey) Active(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && k.ExpiresAt.Before(now) {
		return false
	}
	return true
}

// APIKeyStore persists API keys. Declared here as a port so this package and its
// unit tests stay free of GORM; internal/store provides the implementation.
type APIKeyStore interface {
	Create(ctx context.Context, k *APIKey, tokenHash string) error
	Get(ctx context.Context, id string) (*APIKey, error)
	// ByTokenHash returns any key (revoked or expired included) so the caller can
	// tell "missing" apart from "revoked" or "expired".
	ByTokenHash(ctx context.Context, tokenHash string) (*APIKey, error)
	ListForUser(ctx context.Context, userID string) ([]APIKey, error)
	ListAll(ctx context.Context) ([]APIKey, error)
	SetScopes(ctx context.Context, id string, scopes []string) error
	Update(ctx context.Context, k *APIKey) error
	Revoke(ctx context.Context, id string) error
	Touch(ctx context.Context, id string, at time.Time) error
}

// NewAPIKeyToken mints a raw secret, its display prefix and the stored hash. The
// raw value is shown to the caller exactly once and never persisted.
func NewAPIKeyToken() (raw, prefix, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("auth: api key: %w", err)
	}
	raw = APIKeyPrefix + hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, raw[:len(APIKeyPrefix)+8], hex.EncodeToString(sum[:]), nil
}

// ValidateAPIKeyScopes checks that every scope is a known permission the caller
// holds. An empty set and any unknown or unheld permission are rejected, which
// is what keeps a key from being issued with powers its owner lacks.
func ValidateAPIKeyScopes(scopes []string, can func(perm string) bool) error {
	if len(scopes) == 0 {
		return ErrAPIKeyInvalidScope
	}
	seen := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		if !authz.ValidPermission(s) {
			return ErrAPIKeyInvalidScope
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		if !can(s) {
			return ErrAPIKeyInvalidScope
		}
	}
	return nil
}

// AuthenticateAPIKey resolves a raw key into a principal carrying only the key's
// scopes intersected with the owner's permissions. The caller's identity is set
// to the key owner but IsOwner is false, so a key can never ride the owner
// bypass. Returns the key id alongside the principal so the handler can record
// last-used.
func (s *Service) AuthenticateAPIKey(ctx context.Context, raw string) (*authz.Principal, string, error) {
	if s.apiKeys == nil {
		return nil, "", ErrAPIKeyNotFound
	}
	key, err := s.apiKeys.ByTokenHash(ctx, HashToken(raw))
	if err != nil || key == nil {
		return nil, "", ErrAPIKeyNotFound
	}
	if key.RevokedAt != nil {
		return nil, "", ErrAPIKeyRevoked
	}
	if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now().UTC()) {
		return nil, "", ErrAPIKeyExpired
	}
	u, err := s.users.Get(ctx, key.UserID)
	if err != nil || u == nil {
		return nil, "", ErrAPIKeyNotFound
	}
	if u.Status != StatusActive {
		return nil, "", ErrUserDisabled
	}
	// A key is the owner's power minus what was not granted: build the owner's
	// full permission set, then keep only the scopes the key actually holds.
	userP, err := s.authz.Load(ctx, u.ID, u.Email, u.Name, u.IsOwner)
	if err != nil {
		return nil, "", err
	}
	perms := make(map[string]bool, len(key.Scopes))
	for _, sc := range key.Scopes {
		if userP.Can(sc) {
			perms[sc] = true
		}
	}
	p := &authz.Principal{
		UserID:      u.ID,
		Email:       u.Email,
		Name:        u.Name,
		IsOwner:     false,
		Permissions: perms,
	}
	return p, key.ID, nil
}

// APIKeys exposes the key store for the admin handlers.
func (s *Service) APIKeys() APIKeyStore { return s.apiKeys }

// apiKeyRaw extracts a raw key from the request: either the dedicated
// X-AgentHub-Key header or a Bearer token in Authorization, mirroring how the
// session cookie is also accepted as X-AgentHub-Session.
func apiKeyRaw(r *http.Request) string {
	if v := r.Header.Get("X-AgentHub-Key"); v != "" {
		return strings.TrimSpace(v)
	}
	const scheme = "Bearer "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, scheme) {
		return strings.TrimSpace(strings.TrimPrefix(h, scheme))
	}
	return ""
}
