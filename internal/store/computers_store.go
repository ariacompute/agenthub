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
type computersStore struct{ db *gorm.DB }

// NewComputersStore returns a computers.Store backed by the shared GORM handle.
func NewComputersStore(db *gorm.DB) computers.Store {
	return &computersStore{db: db}
}

func toDomainComputer(r *model.Computer) *computers.Computer {
	return &computers.Computer{
		ID:         r.ID,
		OwnerID:    r.OwnerID,
		AgentID:    r.AgentID,
		Kind:       computers.Kind(r.Kind),
		SessionRef: r.SessionRef,
		Status:     r.Status,
		LastAction: r.LastAction,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *computersStore) List(ctx context.Context, ownerID string) ([]*computers.Computer, error) {
	var rows []model.Computer
	q := s.db.WithContext(ctx).Order("created_at DESC")
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
}

func (s *computersStore) Get(ctx context.Context, id string) (*computers.Computer, error) {
	var r model.Computer
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomainComputer(&r), nil
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
		Status:     c.Status,
		LastAction: c.LastAction,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return toDomainComputer(&row), nil
}
