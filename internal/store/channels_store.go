package store

import (
	"context"
	"errors"
	"time"

	"agenthub/internal/channels"
	"agenthub/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// channelsStore implements channels.Store over GORM, following the same shape as the
// other store implementations in this package.
type channelsStore struct {
	db *gorm.DB
}

// NewChannelsStore returns a channels.Store backed by the shared GORM handle.
func NewChannelsStore(db *gorm.DB) channels.Store {
	return &channelsStore{db: db}
}

func toDomainChannel(r *model.Channel) *channels.Channel {
	c := &channels.Channel{
		ID:            r.ID,
		Kind:          channels.Kind(r.Kind),
		Name:          r.Name,
		Enabled:       r.Enabled,
		SigningKeyEnv: r.SigningKeyEnv,
		ThreadID:      r.ThreadID,
		OwnerID:       r.OwnerID,
	}
	_ = r.Config.Decode(&c.Config)
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	return c
}

func toDomainMessage(r *model.ChannelMessage) *channels.ChannelMessage {
	m := &channels.ChannelMessage{
		ID:          r.ExternalRef,
		ChannelID:   r.ChannelID,
		ExternalRef: r.ExternalRef,
		ThreadID:    r.ThreadID,
		TaskID:      r.TaskID,
		Direction:   r.Direction,
		Status:      r.Status,
		Error:       r.Error,
		CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
	}
	_ = r.Payload.Decode(&m.Payload)
	if m.Payload == nil {
		m.Payload = map[string]any{}
	}
	return m
}

func (s *channelsStore) List(ctx context.Context, ownerID string) ([]*channels.Channel, error) {
	var rows []model.Channel
	q := s.db.WithContext(ctx).Order("created_at DESC")
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*channels.Channel, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainChannel(&rows[i]))
	}
	return out, nil
}

func (s *channelsStore) Get(ctx context.Context, id string) (*channels.Channel, error) {
	var r model.Channel
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomainChannel(&r), nil
}

func (s *channelsStore) Create(ctx context.Context, c *channels.Channel) (*channels.Channel, error) {
	cfg, err := model.NewJSONField(c.Config)
	if err != nil {
		return nil, err
	}
	r := model.Channel{
		ID:            c.ID,
		Kind:          string(c.Kind),
		Name:          c.Name,
		Enabled:       c.Enabled,
		Config:        cfg,
		SigningKeyEnv: c.SigningKeyEnv,
		ThreadID:      c.ThreadID,
		OwnerID:       c.OwnerID,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&r).Error; err != nil {
		return nil, err
	}
	return toDomainChannel(&r), nil
}

func (s *channelsStore) Update(ctx context.Context, c *channels.Channel) (*channels.Channel, error) {
	cfg, err := model.NewJSONField(c.Config)
	if err != nil {
		return nil, err
	}
	r := model.Channel{
		ID:            c.ID,
		Kind:          string(c.Kind),
		Name:          c.Name,
		Enabled:       c.Enabled,
		Config:        cfg,
		SigningKeyEnv: c.SigningKeyEnv,
		ThreadID:      c.ThreadID,
		OwnerID:       c.OwnerID,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).
		Where("id = ?", c.ID).
		Updates(&r).Error; err != nil {
		return nil, err
	}
	return toDomainChannel(&r), nil
}

func (s *channelsStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Channel{}).Error
}

func (s *channelsStore) RecordDelivery(ctx context.Context, m *channels.ChannelMessage) (bool, error) {
	payload, err := model.NewJSONField(m.Payload)
	if err != nil {
		return false, err
	}
	r := model.ChannelMessage{
		ChannelID:   m.ChannelID,
		ExternalRef: m.ExternalRef,
		ThreadID:    m.ThreadID,
		TaskID:      m.TaskID,
		Direction:   m.Direction,
		Payload:     payload,
		Status:      m.Status,
		Error:       m.Error,
		CreatedAt:   time.Now().UTC(),
	}
	// The unique index on (channel_id, external_ref) dedupes redeliveries: an
	// identical key is swallowed rather than erroring.
	err = s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&r).Error
	if err != nil {
		return false, err
	}
	// If no row was written (conflict swallowed), treat as a duplicate.
	affected := s.db.WithContext(ctx).
		Where("channel_id = ? AND external_ref = ?", m.ChannelID, m.ExternalRef).
		First(&model.ChannelMessage{}).Error
	if affected != nil {
		return false, nil
	}
	return true, nil
}

func (s *channelsStore) ListDeliveries(ctx context.Context, channelID string) ([]*channels.ChannelMessage, error) {
	var rows []model.ChannelMessage
	if err := s.db.WithContext(ctx).
		Where("channel_id = ?", channelID).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*channels.ChannelMessage, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainMessage(&rows[i]))
	}
	return out, nil
}
