package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/auth"
	"agenthub/internal/model"
)

// userStore is the PostgreSQL backed auth.UserStore.
type userStore struct{ db *gorm.DB }

// NewUserStore returns a user store backed by db.
func NewUserStore(db *gorm.DB) auth.UserStore { return &userStore{db: db} }

func (s *userStore) Create(ctx context.Context, u *auth.User) error {
	if u == nil || u.ID == "" {
		return fmt.Errorf("store: create user: user or id is empty")
	}
	now := time.Now().UTC()
	row := &model.User{
		ID:           u.ID,
		Email:        u.Email,
		Name:         u.Name,
		PasswordHash: u.PasswordHash,
		Status:       orDefault(u.Status, auth.StatusActive),
		IsOwner:      u.IsOwner,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return auth.ErrEmailTaken
		}
		return err
	}
	u.Status, u.CreatedAt, u.UpdatedAt = row.Status, now, now
	return nil
}

func (s *userStore) Get(ctx context.Context, id string) (*auth.User, error) {
	var row model.User
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return s.decorate(ctx, userFromModel(&row))
}

func (s *userStore) GetByEmail(ctx context.Context, email string) (*auth.User, error) {
	if strings.TrimSpace(email) == "" {
		return nil, nil
	}
	var row model.User
	err := s.db.WithContext(ctx).Where("email = ?", strings.ToLower(strings.TrimSpace(email))).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return s.decorate(ctx, userFromModel(&row))
}

// decorate attaches the roles and groups of a user, which the admin API renders
// and the authorization layer resolves.
func (s *userStore) decorate(ctx context.Context, u *auth.User) (*auth.User, error) {
	var roles []model.UserRole
	if err := s.db.WithContext(ctx).Where("user_id = ?", u.ID).Find(&roles).Error; err != nil {
		return nil, err
	}
	for _, r := range roles {
		u.Roles = append(u.Roles, r.RoleID)
	}
	var groups []model.UserGroup
	if err := s.db.WithContext(ctx).Where("user_id = ?", u.ID).Find(&groups).Error; err != nil {
		return nil, err
	}
	for _, g := range groups {
		u.Groups = append(u.Groups, g.GroupID)
	}
	return u, nil
}

func (s *userStore) Update(ctx context.Context, u *auth.User) error {
	if u == nil || u.ID == "" {
		return fmt.Errorf("store: update user: user or id is empty")
	}
	fields := map[string]any{
		"email":      u.Email,
		"name":       u.Name,
		"status":     orDefault(u.Status, auth.StatusActive),
		"is_owner":   u.IsOwner,
		"updated_at": time.Now().UTC(),
	}
	// An empty hash means "keep the current one", so clearing a password stays
	// an explicit decision made elsewhere.
	if u.PasswordHash != "" {
		fields["password_hash"] = u.PasswordHash
	}
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", u.ID).Updates(fields).Error; err != nil {
		if isUniqueViolation(err) {
			return auth.ErrEmailTaken
		}
		return err
	}
	return nil
}

func (s *userStore) List(ctx context.Context, query string) ([]auth.User, error) {
	q := s.db.WithContext(ctx).Model(&model.User{})
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		q = q.Where("lower(email) LIKE ? OR lower(name) LIKE ?", like, like)
	}
	var rows []model.User
	if err := q.Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]auth.User, 0, len(rows))
	for i := range rows {
		u, err := s.decorate(ctx, userFromModel(&rows[i]))
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, nil
}

func (s *userStore) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.User{}).Count(&n).Error
	return n, err
}

// Delete disables instead of removing: tasks, grants and sessions keep their
// owner so the audit trail stays readable.
func (s *userStore) Delete(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).
		Updates(map[string]any{"status": auth.StatusDisabled, "disabled_at": now, "updated_at": now}).Error
}

// RecordLoginFailure increments the consecutive-failure counter for the account
// and locks it for auth.AccountLockMinutes once the threshold is reached. The
// counter and the lock are keyed by the (case-insensitive) email so a login
// attempt with a differently cased address still counts against the same account.
func (s *userStore) RecordLoginFailure(ctx context.Context, email string) error {
	return s.db.WithContext(ctx).Exec(`
UPDATE users
SET failed_login_attempts = failed_login_attempts + 1,
    locked_until = CASE
        WHEN failed_login_attempts + 1 >= $1 THEN now() + ($2 * interval '1 minute')
        ELSE locked_until
    END,
    updated_at = now()
WHERE lower(email) = lower($3)`,
		auth.MaxLoginAttempts, auth.AccountLockMinutes, email).Error
}

// ResetLoginAttempts clears the failure counter and any active lock on success.
func (s *userStore) ResetLoginAttempts(ctx context.Context, email string) error {
	return s.db.WithContext(ctx).Exec(`
UPDATE users SET failed_login_attempts = 0, locked_until = NULL, updated_at = now()
WHERE lower(email) = lower($1)`, email).Error
}

// LockedUntil returns the account's lock expiry, or nil when it is not locked.
func (s *userStore) LockedUntil(ctx context.Context, email string) (*time.Time, error) {
	var until sql.NullTime
	err := s.db.WithContext(ctx).Model(&model.User{}).
		Where("lower(email) = lower(?)", email).
		Select("locked_until").Scan(&until).Error
	if err != nil {
		return nil, err
	}
	if !until.Valid {
		return nil, nil
	}
	return &until.Time, nil
}

// SetStatus is the enable/disable switch of the admin API.
func (s *userStore) SetStatus(ctx context.Context, id, status string) error {
	fields := map[string]any{"status": status, "updated_at": time.Now().UTC()}
	if status == auth.StatusActive {
		fields["disabled_at"] = nil
	} else {
		fields["disabled_at"] = time.Now().UTC()
	}
	return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(fields).Error
}

func userFromModel(row *model.User) *auth.User {
	return &auth.User{
		ID:           row.ID,
		Email:        row.Email,
		Name:         row.Name,
		PasswordHash: row.PasswordHash,
		Status:       row.Status,
		IsOwner:      row.IsOwner,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		DisabledAt:   row.DisabledAt,
	}
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

type sessionStore struct{ db *gorm.DB }

// NewSessionStore returns a session store backed by db.
func NewSessionStore(db *gorm.DB) auth.SessionStore { return &sessionStore{db: db} }

func (s *sessionStore) Create(ctx context.Context, sess *auth.Session, tokenHash string) error {
	if sess == nil || sess.ID == "" || tokenHash == "" {
		return fmt.Errorf("store: create session: session or token hash is empty")
	}
	row := &model.Session{
		ID:         sess.ID,
		UserID:     sess.UserID,
		TokenHash:  tokenHash,
		CreatedAt:  sess.CreatedAt,
		ExpiresAt:  sess.ExpiresAt,
		LastSeenAt: sess.LastSeenAt,
		UserAgent:  sess.UserAgent,
		IP:         sess.IP,
	}
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *sessionStore) ByTokenHash(ctx context.Context, tokenHash string) (*auth.Session, error) {
	if tokenHash == "" {
		return nil, nil
	}
	var row model.Session
	err := s.db.WithContext(ctx).Where("token_hash = ?", tokenHash).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return sessionFromModel(&row), nil
}

func (s *sessionStore) Touch(ctx context.Context, id string, at time.Time) error {
	return s.db.WithContext(ctx).Model(&model.Session{}).Where("id = ?", id).
		Update("last_seen_at", at).Error
}

func (s *sessionStore) Revoke(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&model.Session{}).Where("id = ?", id).
		Update("revoked_at", time.Now().UTC()).Error
}

func (s *sessionStore) RevokeForUser(ctx context.Context, userID string) error {
	return s.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now().UTC()).Error
}

func (s *sessionStore) ListForUser(ctx context.Context, userID string) ([]auth.Session, error) {
	var rows []model.Session
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]auth.Session, 0, len(rows))
	for i := range rows {
		out = append(out, *sessionFromModel(&rows[i]))
	}
	return out, nil
}

func (s *sessionStore) DeleteExpired(ctx context.Context, now time.Time) error {
	return s.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.Session{}).Error
}

func sessionFromModel(row *model.Session) *auth.Session {
	return &auth.Session{
		ID:         row.ID,
		UserID:     row.UserID,
		CreatedAt:  row.CreatedAt,
		ExpiresAt:  row.ExpiresAt,
		LastSeenAt: row.LastSeenAt,
		UserAgent:  row.UserAgent,
		IP:         row.IP,
		RevokedAt:  row.RevokedAt,
	}
}

// ---------------------------------------------------------------------------
// oauth bindings
// ---------------------------------------------------------------------------

type oauthStore struct{ db *gorm.DB }

// NewOAuthStore returns an OAuth binding store backed by db.
func NewOAuthStore(db *gorm.DB) auth.OAuthStore { return &oauthStore{db: db} }

func (s *oauthStore) Find(ctx context.Context, provider, subject string) (*auth.OAuthAccount, error) {
	if provider == "" || subject == "" {
		return nil, nil
	}
	var row model.OAuthAccount
	err := s.db.WithContext(ctx).Where("provider = ? AND subject = ?", provider, subject).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &auth.OAuthAccount{ID: row.ID, UserID: row.UserID, Provider: row.Provider, Subject: row.Subject, Email: row.Email, CreatedAt: row.CreatedAt}, nil
}

func (s *oauthStore) Link(ctx context.Context, a *auth.OAuthAccount) error {
	if a == nil || a.UserID == "" || a.Provider == "" || a.Subject == "" {
		return fmt.Errorf("store: link oauth: incomplete account")
	}
	if a.ID == "" {
		a.ID = auth.NewID("oauth")
	}
	row := &model.OAuthAccount{
		ID:        a.ID,
		UserID:    a.UserID,
		Provider:  a.Provider,
		Subject:   a.Subject,
		Email:     a.Email,
		CreatedAt: time.Now().UTC(),
	}
	// A provider identity belongs to exactly one user; re-linking an existing
	// (provider, subject) pair is a no-op instead of an error so a second login
	// through the same account never breaks.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider"}, {Name: "subject"}},
		DoNothing: true,
	}).Create(row).Error
}

func (s *oauthStore) ListForUser(ctx context.Context, userID string) ([]auth.OAuthAccount, error) {
	var rows []model.OAuthAccount
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]auth.OAuthAccount, 0, len(rows))
	for i := range rows {
		out = append(out, auth.OAuthAccount{ID: rows[i].ID, UserID: rows[i].UserID, Provider: rows[i].Provider, Subject: rows[i].Subject, Email: rows[i].Email, CreatedAt: rows[i].CreatedAt})
	}
	return out, nil
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func isUniqueViolation(err error) bool {
	// Postgres unique violation is SQLSTATE 23505; matching the code keeps this
	// independent of the driver's error text.
	return err != nil && strings.Contains(err.Error(), "23505")
}
