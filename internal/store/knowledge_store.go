package store

import (
	"context"
	"time"

	"agenthub/internal/knowledge"
	"agenthub/internal/model"
	"agenthub/internal/registry"

	"gorm.io/gorm"
)

// knowledgeStore implements knowledge.Store over GORM.
type knowledgeStore struct{ db *gorm.DB }

// NewKnowledgeStore returns a knowledge.Store backed by the shared GORM handle.
func NewKnowledgeStore(db *gorm.DB) knowledge.Store {
	return &knowledgeStore{db: db}
}

func (s *knowledgeStore) List(ctx context.Context, q knowledge.Query) ([]*knowledge.Knowledge, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*knowledge.Knowledge, error) {
		var rows []model.Knowledge
		query := db.Order("created_at DESC")
		if q.AgentID != "" {
			query = query.Where("agent_id = ?", q.AgentID)
		}
		if q.OwnerID != "" {
			query = query.Where("owner_id = ?", q.OwnerID)
		}
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make([]*knowledge.Knowledge, 0, len(rows))
		for i := range rows {
			out = append(out, knowledgeFromRow(&rows[i]))
		}
		return out, nil
	})
}

func (s *knowledgeStore) Add(ctx context.Context, k *knowledge.Knowledge) (*knowledge.Knowledge, error) {
	row := model.Knowledge{
		ID:        registry.NewID("knw"),
		AgentID:   k.AgentID,
		OwnerID:   k.OwnerID,
		ChunkText: k.ChunkText,
		SourceRef: k.SourceRef,
		CreatedAt: time.Now().UTC(),
	}
	if err := withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return db.Create(&row).Error
	}); err != nil {
		return nil, err
	}
	return knowledgeFromRow(&row), nil
}

func knowledgeFromRow(row *model.Knowledge) *knowledge.Knowledge {
	return &knowledge.Knowledge{
		ID:        row.ID,
		AgentID:   row.AgentID,
		OwnerID:   row.OwnerID,
		ChunkText: row.ChunkText,
		SourceRef: row.SourceRef,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}
}
