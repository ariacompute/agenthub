package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"agenthub/internal/auth"
	"agenthub/internal/model"
)

// apiKeyStore is the PostgreSQL backed auth.APIKeyStore.
type apiKeyStore struct{ db *gorm.DB }

// NewAPIKeyStore returns an API key store backed by db.
func NewAPIKeyStore(db *gorm.DB) auth.APIKeyStore { return &apiKeyStore{db: db} }

func (s *apiKeyStore) Create(ctx context.Context, k *auth.APIKey, tokenHash string) error {
	if k == nil || k.ID == "" || tokenHash == "" {
		return errors.New("store: create api key: key or token hash is empty")
	}
	if k.UserID == "" {
		return errors.New("store: create api key: user id is empty")
	}
	now := time.Now().UTC()
	row := &model.APIKey{
		ID:        k.ID,
		UserID:    k.UserID,
		Name:      k.Name,
		TokenHash: tokenHash,
		Prefix:    k.Prefix,
		CreatedAt: now,
		CreatedBy: k.CreatedBy,
		ExpiresAt: k.ExpiresAt,
	}
	scopes := make([]model.APIKeyScope, 0, len(k.Scopes))
	for _, p := range k.Scopes {
		scopes = append(scopes, model.APIKeyScope{KeyID: k.ID, Permission: p})
	}
	// One transaction so a key always has its scopes; a partial write would leave
	// a credential that can do nothing.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		if len(scopes) > 0 {
			if err := tx.Create(&scopes).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *apiKeyStore) Get(ctx context.Context, id string) (*auth.APIKey, error) {
	var row model.APIKey
	err := s.db.WithContext(ctx).Where("id = ? AND revoked_at IS NULL", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return s.decorate(ctx, apiKeyFromModel(&row))
}

func (s *apiKeyStore) ByTokenHash(ctx context.Context, tokenHash string) (*auth.APIKey, error) {
	if tokenHash == "" {
		return nil, nil
	}
	var row model.APIKey
	err := s.db.WithContext(ctx).Where("token_hash = ?", tokenHash).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return s.decorate(ctx, apiKeyFromModel(&row))
}

func (s *apiKeyStore) ListForUser(ctx context.Context, userID string) ([]auth.APIKey, error) {
	var rows []model.APIKey
	if err := s.db.WithContext(ctx).Where("user_id = ? AND revoked_at IS NULL", userID).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.decorateAll(ctx, rows)
}

func (s *apiKeyStore) ListAll(ctx context.Context) ([]auth.APIKey, error) {
	var rows []model.APIKey
	if err := s.db.WithContext(ctx).Where("revoked_at IS NULL").
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.decorateAll(ctx, rows)
}

// decorate attaches the permission subset of a key.
func (s *apiKeyStore) decorate(ctx context.Context, k *auth.APIKey) (*auth.APIKey, error) {
	if k == nil {
		return nil, nil
	}
	var scopes []model.APIKeyScope
	if err := s.db.WithContext(ctx).Where("key_id = ?", k.ID).Find(&scopes).Error; err != nil {
		return nil, err
	}
	for _, sc := range scopes {
		k.Scopes = append(k.Scopes, sc.Permission)
	}
	return k, nil
}

func (s *apiKeyStore) decorateAll(ctx context.Context, rows []model.APIKey) ([]auth.APIKey, error) {
	out := make([]auth.APIKey, 0, len(rows))
	for i := range rows {
		k, err := s.decorate(ctx, apiKeyFromModel(&rows[i]))
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, nil
}

// SetScopes replaces the key's permission subset in a single transaction.
func (s *apiKeyStore) SetScopes(ctx context.Context, id string, scopes []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("key_id = ?", id).Delete(&model.APIKeyScope{}).Error; err != nil {
			return err
		}
		rows := make([]model.APIKeyScope, 0, len(scopes))
		for _, p := range scopes {
			rows = append(rows, model.APIKeyScope{KeyID: id, Permission: p})
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Update writes the mutable fields of a key. Scopes are changed through
// SetScopes, so only the label and expiry are touched here.
func (s *apiKeyStore) Update(ctx context.Context, k *auth.APIKey) error {
	if k == nil || k.ID == "" {
		return errors.New("store: update api key: key or id is empty")
	}
	fields := map[string]any{
		"name":       k.Name,
		"expires_at": k.ExpiresAt,
		"updated_at": time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Model(&model.APIKey{}).Where("id = ?", k.ID).Updates(fields).Error
}

// Revoke soft-deletes a key: it keeps the row for the audit trail but makes it
// unusable on the next request.
func (s *apiKeyStore) Revoke(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&model.APIKey{}).Where("id = ?", id).
		Update("revoked_at", time.Now().UTC()).Error
}

func (s *apiKeyStore) Touch(ctx context.Context, id string, at time.Time) error {
	return s.db.WithContext(ctx).Model(&model.APIKey{}).Where("id = ?", id).
		Update("last_used_at", at).Error
}

func apiKeyFromModel(row *model.APIKey) *auth.APIKey {
	k := &auth.APIKey{
		ID:         row.ID,
		UserID:     row.UserID,
		Name:       row.Name,
		Prefix:     row.Prefix,
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
		ExpiresAt:  row.ExpiresAt,
		RevokedAt:  row.RevokedAt,
		CreatedBy:  row.CreatedBy,
	}
	// The unique token_hash is never surfaced; callers get only the prefix.
	return k
}
