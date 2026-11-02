package store

import (
	"context"
	"errors"
	"time"

	"agenthub/internal/computers"
	"agenthub/internal/model"
	"agenthub/internal/registry"

	"gorm.io/gorm"
)

// computersStore implements computers.Store over GORM.
//
// computers is one of the tables under forced row level security
// (migrations/028_rls_policies_owned_surfaces.sql), so every method runs through
// scoped/withIdentity rather than a bare handle.
type computersStore struct{ db *gorm.DB }

// NewComputersStore returns a computers.Store backed by the shared GORM handle.
func NewComputersStore(db *gorm.DB) computers.Store {
	return &computersStore{db: db}
}

func toDomainComputer(r *model.Computer) *computers.Computer {
	out := &computers.Computer{
		ID:         r.ID,
		OwnerID:    r.OwnerID,
		AgentID:    r.AgentID,
		Kind:       computers.Kind(r.Kind),
		SessionRef: r.SessionRef,
		Provider:   r.Provider,
		LeaseID:    r.LeaseID,
		NodeID:     r.NodeID,
		Status:     r.Status,
		LastAction: r.LastAction,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.ExpiresAt != nil {
		out.ExpiresAt = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *computersStore) List(ctx context.Context, ownerID string) ([]*computers.Computer, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*computers.Computer, error) {
		var rows []model.Computer
		q := db.Order("created_at DESC")
		if ownerID != "" {
			q = q.Where("owner_id = ?", ownerID)
		}
		if err := q.Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make([]*computers.Computer, 0, len(rows))
		for i := range rows {
			out = append(out, toDomainComputer(&rows[i]))
		}
		return out, nil
	})
}

func (s *computersStore) Get(ctx context.Context, id, ownerID string) (*computers.Computer, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*computers.Computer, error) {
		var r model.Computer
		if err := ownedWhere(db, id, ownerID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return toDomainComputer(&r), nil
	})
}

func (s *computersStore) Create(ctx context.Context, c *computers.Computer) (*computers.Computer, error) {
	if c.Kind == "" {
		c.Kind = computers.KindBrowser
	}
	if c.Status == "" {
		c.Status = "idle"
	}
	row := model.Computer{
		ID:         registry.NewID("comp"),
		OwnerID:    c.OwnerID,
		AgentID:    c.AgentID,
		Kind:       string(c.Kind),
		SessionRef: c.SessionRef,
		Provider:   c.Provider,
		LeaseID:    c.LeaseID,
		NodeID:     c.NodeID,
		ExpiresAt:  parseTimePtr(c.ExpiresAt),
		Status:     c.Status,
		LastAction: c.LastAction,
		CreatedAt:  time.Now().UTC(),
	}
	if err := withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return db.Create(&row).Error
	}); err != nil {
		return nil, err
	}
	return toDomainComputer(&row), nil
}

func (s *computersStore) Update(ctx context.Context, c *computers.Computer, ownerID string) (*computers.Computer, error) {
	updates := map[string]any{
		"session_ref": c.SessionRef,
		"provider":    c.Provider,
		"lease_id":    c.LeaseID,
		"node_id":     c.NodeID,
		"expires_at":  parseTimePtr(c.ExpiresAt),
		"status":      c.Status,
		"last_action": c.LastAction,
	}
	return scoped(ctx, s.db, func(db *gorm.DB) (*computers.Computer, error) {
		if err := ownedWhere(db.Model(&model.Computer{}), c.ID, ownerID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
		var r model.Computer
		if err := ownedWhere(db, c.ID, ownerID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return toDomainComputer(&r), nil
	})
}

func (s *computersStore) Delete(ctx context.Context, id, ownerID string) error {
	return withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return ownedWhere(db, id, ownerID).Delete(&model.Computer{}).Error
	})
}

// parseTimePtr keeps an empty string meaning "no expiry" instead of the zero
// time, which would render as 0001-01-01 in the console.
func parseTimePtr(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
