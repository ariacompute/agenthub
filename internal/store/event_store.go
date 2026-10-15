package store

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/hub"
	"agenthub/internal/model"
)

// eventRepository is the PostgreSQL backed hub.EventRepository.
type eventRepository struct{ db *gorm.DB }

// NewEventRepository returns an event repository backed by db.
func NewEventRepository(db *gorm.DB) hub.EventRepository {
	return &eventRepository{db: db}
}

func (s *eventRepository) Append(ctx context.Context, e hub.Event) error {
	payload, err := model.NewJSONField(e)
	if err != nil {
		return err
	}
	row := &model.Event{
		Seq:       e.Seq,
		TaskID:    e.TaskID,
		Kind:      e.Kind,
		At:        e.At,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	// A replayed or retried frame carries the same seq: ignore it instead of
	// failing the writer.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_id"}, {Name: "seq"}},
		DoNothing: true,
	}).Create(row).Error
}

func (s *eventRepository) Since(ctx context.Context, taskID string, after uint64, limit int) ([]hub.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []model.Event
	err := s.db.WithContext(ctx).
		Where("task_id = ? AND seq > ?", taskID, after).
		Order("seq ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]hub.Event, 0, len(rows))
	for i := range rows {
		ev, err := eventFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s *eventRepository) MaxSeq(ctx context.Context) (uint64, error) {
	var max sql.NullInt64
	if err := s.db.WithContext(ctx).Model(&model.Event{}).
		Select("COALESCE(MAX(seq), 0)").Scan(&max).Error; err != nil {
		return 0, err
	}
	if !max.Valid || max.Int64 < 0 {
		return 0, nil
	}
	return uint64(max.Int64), nil
}

func eventFromRow(row *model.Event) (hub.Event, error) {
	var ev hub.Event
	if err := row.Payload.Decode(&ev); err != nil {
		return ev, err
	}
	// A payload that predates a field addition must still replay with a
	// coherent identity, so the columns win over the decoded body.
	ev.Seq = row.Seq
	ev.TaskID = row.TaskID
	ev.Kind = row.Kind
	ev.At = row.At
	return ev, nil
}
