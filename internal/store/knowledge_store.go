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

func (s *knowledgeStore) List(ctx context.Context, agentID string) ([]*knowledge.Knowledge, error) {
	var rows []model.Knowledge
	q := s.db.WithContext(ctx).Order("created_at DESC")
	if agentID != "" {
		q = q.Where("agent_id = ?", agentID)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*knowledge.Knowledge, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		out = append(out, &knowledge.Knowledge{
			ID:        r.ID,
			AgentID:   r.AgentID,
			ChunkText: r.ChunkText,
			SourceRef: r.SourceRef,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func (s *knowledgeStore) Add(ctx context.Context, k *knowledge.Knowledge) (*knowledge.Knowledge, error) {
	row := model.Knowledge{
		ID:        registry.NewID("knw"),
		AgentID:   k.AgentID,
		ChunkText: k.ChunkText,
		SourceRef: k.SourceRef,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return &knowledge.Knowledge{
		ID:        row.ID,
		AgentID:   row.AgentID,
		ChunkText: row.ChunkText,
		SourceRef: row.SourceRef,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}, nil
}
