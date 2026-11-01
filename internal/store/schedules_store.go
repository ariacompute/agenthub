package store

import (
	"context"
	"errors"
	"time"

	"agenthub/internal/model"
	"agenthub/internal/registry"
	"agenthub/internal/schedules"

	"gorm.io/gorm"
)

// schedulesStore implements schedules.Store over GORM.
type schedulesStore struct{ db *gorm.DB }

// NewSchedulesStore returns a schedules.Store backed by the shared GORM handle.
func NewSchedulesStore(db *gorm.DB) schedules.Store {
	return &schedulesStore{db: db}
}

func toDomainSchedule(r *model.Schedule) *schedules.Schedule {
	s := &schedules.Schedule{
		ID:         r.ID,
		Goal:       r.Goal,
		Trigger:    r.Trigger,
		CronExpr:   r.CronExpr,
		Enabled:    r.Enabled,
		OwnerID:    r.OwnerID,
		LastStatus: r.LastStatus,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.NextRun != nil {
		s.NextRun = r.NextRun.UTC().Format(time.RFC3339)
	}
	if r.LastRunAt != nil {
		s.LastRunAt = r.LastRunAt.UTC().Format(time.RFC3339)
	}
	return s
}

func (s *schedulesStore) List(ctx context.Context, ownerID string) ([]*schedules.Schedule, error) {
	var rows []model.Schedule
	q := s.db.WithContext(ctx).Order("created_at DESC")
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*schedules.Schedule, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainSchedule(&rows[i]))
	}
	return out, nil
}

func (s *schedulesStore) Get(ctx context.Context, id string) (*schedules.Schedule, error) {
	var r model.Schedule
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return toDomainSchedule(&r), nil
}

func (s *schedulesStore) Create(ctx context.Context, in *schedules.Schedule) (*schedules.Schedule, error) {
	if in.Trigger == "" {
		in.Trigger = "manual"
	}
	now := time.Now().UTC()
	row := model.Schedule{
		ID:        registry.NewID("sched"),
		Goal:      in.Goal,
		Trigger:   in.Trigger,
		CronExpr:  in.CronExpr,
		Enabled:   in.Enabled,
		OwnerID:   in.OwnerID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// The next run is derived from the expression at save time, so the field the
	// router scans is real rather than a placeholder.
	if next := nextRunAt(in, now); !next.IsZero() {
		row.NextRun = &next
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return toDomainSchedule(&row), nil
}

// nextRunAt is when a saved schedule fires next, or the zero time when it is not
// a cron schedule. An expression the parser rejects yields the zero time: the
// row is still saved, and the router marks it invalid on its next scan, which is
// more useful to an operator than a rejected save.
func nextRunAt(in *schedules.Schedule, from time.Time) time.Time {
	next, err := schedules.NextRun(in.CronExpr, from)
	if err != nil || next.IsZero() {
		return time.Time{}
	}
	return next
}

func (s *schedulesStore) Update(ctx context.Context, in *schedules.Schedule) (*schedules.Schedule, error) {
	updates := map[string]any{
		"goal":        in.Goal,
		"trigger":     in.Trigger,
		"cron_expr":   in.CronExpr,
		"enabled":     in.Enabled,
		"last_status": in.LastStatus,
		"updated_at":  time.Now().UTC(),
	}
	if in.LastRunAt != "" {
		if t, err := time.Parse(time.RFC3339, in.LastRunAt); err == nil {
			updates["last_run_at"] = t
		}
	}
	if in.NextRun != "" {
		if t, err := time.Parse(time.RFC3339, in.NextRun); err == nil {
			updates["next_run"] = t
		}
	} else if t := nextRunAt(in, time.Now().UTC()); t.IsZero() {
		// The expression changed or the schedule was disabled: clear the field so
		// the router never fires a slot that no longer exists.
		updates["next_run"] = nil
	} else {
		updates["next_run"] = t
	}
	if err := s.db.WithContext(ctx).Model(&model.Schedule{}).Where("id = ?", in.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, in.ID)
}

func (s *schedulesStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Schedule{}).Error
}
