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
//
// channels is one of the tables under forced row level security
// (migrations/028_rls_policies_owned_surfaces.sql), so every method runs through
// scoped/withIdentity: without the identity in the session, a policy compares the
// owner against NULL and the statement touches nothing.
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
		OwnerID:     r.OwnerID,
		CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
	}
	_ = r.Payload.Decode(&m.Payload)
	if m.Payload == nil {
		m.Payload = map[string]any{}
	}
	return m
}

func (s *channelsStore) List(ctx context.Context, ownerID string) ([]*channels.Channel, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*channels.Channel, error) {
		var rows []model.Channel
		q := db.Order("created_at DESC")
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
	})
}

func (s *channelsStore) Get(ctx context.Context, id, ownerID string) (*channels.Channel, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*channels.Channel, error) {
		var r model.Channel
		if err := ownedWhere(db, id, ownerID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return toDomainChannel(&r), nil
	})
}

// GetUnscoped is the webhook's read: no ownership predicate, because the row
// itself carries the signing secret reference the request is verified against.
// It is called with a system identity so row level security admits it, and it
// must never be reached from a path that answers a signed in user.
func (s *channelsStore) GetUnscoped(ctx context.Context, id string) (*channels.Channel, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*channels.Channel, error) {
		var r model.Channel
		if err := db.Where("id = ?", id).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return toDomainChannel(&r), nil
	})
}

// channelConfig encodes the per-channel settings. A nil map becomes an empty
// object rather than SQL NULL: the column is `JSONB NOT NULL DEFAULT '{}'`, and
// an explicit NULL overrides that default, so a caller that creates a channel
// without settings would be refused by the database instead of stored.
func channelConfig(cfg map[string]any) (model.JSONField, error) {
	if cfg == nil {
		return model.NewJSONField(map[string]any{})
	}
	return model.NewJSONField(cfg)
}

// deliveryPayload is channelConfig for the receipt table, which carries the same
// NOT NULL DEFAULT '{}' contract: a webhook body that was never parsed is an
// empty object, not a missing one.
func deliveryPayload(payload map[string]any) (model.JSONField, error) {
	if payload == nil {
		return model.NewJSONField(map[string]any{})
	}
	return model.NewJSONField(payload)
}

func (s *channelsStore) Create(ctx context.Context, c *channels.Channel) (*channels.Channel, error) {
	cfg, err := channelConfig(c.Config)
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
	if err := withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return db.Create(&r).Error
	}); err != nil {
		return nil, err
	}
	return toDomainChannel(&r), nil
}

func (s *channelsStore) Update(ctx context.Context, c *channels.Channel, ownerID string) (*channels.Channel, error) {
	cfg, err := channelConfig(c.Config)
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
	// Ownership is never transferred by an update: the row is matched with the
	// caller's owner id, and the write puts the same owner back.
	if err := withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return ownedWhere(db.Model(&model.Channel{}), c.ID, ownerID).Updates(&r).Error
	}); err != nil {
		return nil, err
	}
	return toDomainChannel(&r), nil
}

func (s *channelsStore) Delete(ctx context.Context, id, ownerID string) (bool, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (bool, error) {
		res := ownedWhere(db, id, ownerID).Delete(&model.Channel{})
		if res.Error != nil {
			return false, res.Error
		}
		// RowsAffected is the only trustworthy answer to "did this row exist and
		// belong to the caller": a prior SELECT would answer a question that can
		// change before the DELETE runs.
		return res.RowsAffected > 0, nil
	})
}

// channelOwner is the owner a receipt has to carry: the one on the channel it
// was routed through. Copying it here is what makes channel_messages safe to put
// under a policy — the row can no longer claim an owner its parent does not have.
func channelOwner(db *gorm.DB, channelID string) (string, error) {
	var row struct{ OwnerID string }
	res := db.Model(&model.Channel{}).Select("owner_id").Where("id = ?", channelID).Limit(1).Scan(&row)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", channels.ErrChannelNotFound
	}
	return row.OwnerID, nil
}

func (s *channelsStore) RecordDelivery(ctx context.Context, m *channels.ChannelMessage) (bool, error) {
	payload, err := deliveryPayload(m.Payload)
	if err != nil {
		return false, err
	}
	return scoped(ctx, s.db, func(db *gorm.DB) (bool, error) {
		owner, err := channelOwner(db, m.ChannelID)
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
			OwnerID:     owner,
			CreatedAt:   time.Now().UTC(),
		}
		// The unique index on (channel_id, external_ref) dedupes redeliveries: an
		// identical key is swallowed rather than erroring.
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; err != nil {
			return false, err
		}
		// If no row was written (conflict swallowed), treat as a duplicate.
		affected := db.
			Where("channel_id = ? AND external_ref = ?", m.ChannelID, m.ExternalRef).
			First(&model.ChannelMessage{}).Error
		if affected != nil {
			return false, nil
		}
		return true, nil
	})
}

func (s *channelsStore) GetDelivery(ctx context.Context, channelID, id, ownerID string) (*channels.ChannelMessage, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*channels.ChannelMessage, error) {
		var r model.ChannelMessage
		q := ownedChildWhere(db.Where("external_ref = ?", id), "channel_id", channelID, ownerID)
		if err := q.First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return toDomainMessage(&r), nil
	})
}

func (s *channelsStore) UpdateDelivery(ctx context.Context, m *channels.ChannelMessage, ownerID string) (bool, error) {
	payload, err := deliveryPayload(m.Payload)
	if err != nil {
		return false, err
	}
	return scoped(ctx, s.db, func(db *gorm.DB) (bool, error) {
		// Ownership is never rewritten by an update: the row is matched with the
		// caller's owner id and the payload written back leaves owner_id alone.
		res := ownedChildWhere(db.Model(&model.ChannelMessage{}).Where("external_ref = ?", m.ID), "channel_id", m.ChannelID, ownerID).
			Updates(map[string]any{"status": m.Status, "error": m.Error, "payload": payload})
		if res.Error != nil {
			return false, res.Error
		}
		return res.RowsAffected > 0, nil
	})
}

func (s *channelsStore) ListDeliveries(ctx context.Context, channelID, ownerID string) ([]*channels.ChannelMessage, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*channels.ChannelMessage, error) {
		var rows []model.ChannelMessage
		if err := ownedChildWhere(db, "channel_id", channelID, ownerID).
			Order("created_at DESC").
			Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make([]*channels.ChannelMessage, 0, len(rows))
		for i := range rows {
			out = append(out, toDomainMessage(&rows[i]))
		}
		return out, nil
	})
}
