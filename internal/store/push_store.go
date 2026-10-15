package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"agenthub/internal/a2a"
	"agenthub/internal/hub"
	"agenthub/internal/model"
)

// pushRepository is the PostgreSQL backed hub.PushRepository: the audit trail of
// every callback the notifier captured.
type pushRepository struct{ db *gorm.DB }

// NewPushRepository returns a push repository backed by db.
func NewPushRepository(db *gorm.DB) hub.PushRepository {
	return &pushRepository{db: db}
}

func (s *pushRepository) Append(ctx context.Context, r hub.PushRecord) error {
	cfg, err := model.NewJSONField(r.Config)
	if err != nil {
		return err
	}
	if r.TaskID == "" {
		return fmt.Errorf("store: append push record: task id is empty")
	}
	row := &model.PushRecord{
		TaskID:    r.TaskID,
		URL:       r.URL,
		State:     string(r.State),
		Config:    cfg,
		CreatedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Create(row).Error
}

// List returns records oldest first so it matches the order the in-memory
// notifier produced before persistence existed. An empty taskID means every
// task; limit <= 0 falls back to 200 rows.
func (s *pushRepository) List(ctx context.Context, taskID string, limit int) ([]hub.PushRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	q := s.db.WithContext(ctx).Model(&model.PushRecord{})
	if taskID != "" {
		q = q.Where("task_id = ?", taskID)
	}
	var rows []model.PushRecord
	if err := q.Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]hub.PushRecord, 0, len(rows))
	for i := range rows {
		rec, err := pushRecordFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Prune keeps the newest keepPerTask records per task so a long-lived instance
// does not grow the audit trail without bound.
func (s *pushRepository) Prune(ctx context.Context, keepPerTask int) error {
	if keepPerTask <= 0 {
		return fmt.Errorf("store: prune push records: keep must be positive, got %d", keepPerTask)
	}
	return s.db.WithContext(ctx).Exec(`
		DELETE FROM push_records p
		USING (
			SELECT id FROM (
				SELECT id, row_number() OVER (
					PARTITION BY task_id ORDER BY created_at DESC, id DESC
				) AS rn
				FROM push_records
			) ranked
			WHERE ranked.rn > ?
		) old
		WHERE p.id = old.id`, keepPerTask).Error
}

func pushRecordFromRow(row *model.PushRecord) (hub.PushRecord, error) {
	rec := hub.PushRecord{
		TaskID: row.TaskID,
		URL:    row.URL,
		State:  a2a.TaskState(row.State),
	}
	if err := row.Config.Decode(&rec.Config); err != nil {
		return rec, err
	}
	return rec, nil
}
