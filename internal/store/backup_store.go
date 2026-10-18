package store

import (
	"context"
	"errors"
	"time"

	"agenthub/internal/backup"
	"agenthub/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// backupStore keeps the checkpoint catalogue and the single schedule row. It is
// the GORM side of backup.BackupStore; the port itself is declared in
// internal/backup, which never imports this package.
type backupStore struct {
	db *gorm.DB
}

// NewBackupStore returns the catalogue store for backups.
func NewBackupStore(db *gorm.DB) backup.BackupStore {
	return &backupStore{db: db}
}

func (s *backupStore) Create(ctx context.Context, c *backup.Checkpoint) error {
	if c == nil || c.ID == "" {
		return errors.New("store: create checkpoint: id is empty")
	}
	return s.db.WithContext(ctx).Create(checkpointToModel(c)).Error
}

// Finish updates the mutable tail of a row. It updates rather than saves so a
// checkpoint that was pruned while it was running cannot come back: the row is
// gone, and a delete must not be undone by a late finish.
func (s *backupStore) Finish(ctx context.Context, c *backup.Checkpoint) error {
	if c == nil || c.ID == "" {
		return errors.New("store: finish checkpoint: id is empty")
	}
	return s.db.WithContext(ctx).Model(&model.BackupCheckpoint{}).Where("id = ?", c.ID).Updates(map[string]any{
		"status":         c.Status,
		"path":           c.Path,
		"checksum":       c.Checksum,
		"size_bytes":     c.SizeBytes,
		"row_count":      c.RowCount,
		"table_count":    c.TableCount,
		"schema_version": c.Schema,
		"duration_ms":    c.DurationMS,
		"error_code":     c.ErrorCode,
		"finished_at":    c.FinishedAt,
		"restored_at":    c.RestoredAt,
		"restored_by":    c.RestoredBy,
	}).Error
}

func (s *backupStore) Get(ctx context.Context, id string) (*backup.Checkpoint, error) {
	var row model.BackupCheckpoint
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, backup.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return checkpointFromModel(&row), nil
}

func (s *backupStore) List(ctx context.Context, limit int) ([]backup.Checkpoint, error) {
	q := s.db.WithContext(ctx).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []model.BackupCheckpoint
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]backup.Checkpoint, 0, len(rows))
	for i := range rows {
		out = append(out, *checkpointFromModel(&rows[i]))
	}
	return out, nil
}

func (s *backupStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.BackupCheckpoint{}).Error
}

func (s *backupStore) LastSuccess(ctx context.Context) (*backup.Checkpoint, error) {
	var row model.BackupCheckpoint
	err := s.db.WithContext(ctx).
		Where("status = ?", backup.StatusOK).
		Order("created_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, nil
	}
	return checkpointFromModel(&row), nil
}

// MarkInterrupted closes rows a previous process left running. A backup cannot
// outlive its process, so an old "running" row is a crash rather than work in
// progress, and the console should say so.
func (s *backupStore) MarkInterrupted(ctx context.Context, olderThan time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.BackupCheckpoint{}).
		Where("status = ? AND created_at < ?", backup.StatusRunning, olderThan).
		Updates(map[string]any{
			"status":      backup.StatusInterrupted,
			"finished_at": time.Now().UTC(),
			"error_code":  "backup_interrupted",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (s *backupStore) LoadSchedule(ctx context.Context) (*backup.Schedule, error) {
	var row model.BackupSchedule
	err := s.db.WithContext(ctx).Where("id = ?", "default").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// The migration seeds the row; a database that predates it gets the
		// defaults rather than an error, and SaveSchedule will create it.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sched := scheduleFromModel(&row)
	return &sched, nil
}

func (s *backupStore) SaveSchedule(ctx context.Context, sched backup.Schedule, actor string) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"mode", "enabled", "interval_sec", "daily_time", "timezone", "cron_expr", "keep_last", "max_age_days", "updated_at", "updated_by"}),
	}).Create(scheduleToModel(sched, actor)).Error
}

func checkpointToModel(c *backup.Checkpoint) *model.BackupCheckpoint {
	return &model.BackupCheckpoint{
		ID:         c.ID,
		Kind:       c.Kind,
		Engine:     c.Engine,
		Status:     c.Status,
		Label:      c.Label,
		Path:       c.Path,
		SizeBytes:  c.SizeBytes,
		Checksum:   c.Checksum,
		RowCount:   c.RowCount,
		TableCount: c.TableCount,
		Schema:     c.Schema,
		DurationMS: c.DurationMS,
		ErrorCode:  c.ErrorCode,
		CreatedAt:  c.CreatedAt,
		FinishedAt: c.FinishedAt,
		RestoredAt: c.RestoredAt,
		RestoredBy: c.RestoredBy,
	}
}

func checkpointFromModel(row *model.BackupCheckpoint) *backup.Checkpoint {
	return &backup.Checkpoint{
		ID:         row.ID,
		Kind:       row.Kind,
		Engine:     row.Engine,
		Status:     row.Status,
		Label:      row.Label,
		Path:       row.Path,
		SizeBytes:  row.SizeBytes,
		Checksum:   row.Checksum,
		RowCount:   row.RowCount,
		TableCount: row.TableCount,
		Schema:     row.Schema,
		DurationMS: row.DurationMS,
		ErrorCode:  row.ErrorCode,
		CreatedAt:  row.CreatedAt,
		FinishedAt: row.FinishedAt,
		RestoredAt: row.RestoredAt,
		RestoredBy: row.RestoredBy,
	}
}

func scheduleFromModel(row *model.BackupSchedule) backup.Schedule {
	return backup.Schedule{
		Mode:       row.Mode,
		Enabled:    row.Enabled,
		Interval:   time.Duration(row.IntervalSec) * time.Second,
		DailyTime:  row.DailyTime,
		Timezone:   row.Timezone,
		CronExpr:   row.CronExpr,
		KeepLast:   row.KeepLast,
		MaxAgeDays: row.MaxAgeDays,
	}
}

func scheduleToModel(s backup.Schedule, actor string) *model.BackupSchedule {
	return &model.BackupSchedule{
		ID:          "default",
		Mode:        s.Mode,
		Enabled:     s.Enabled,
		IntervalSec: int64(s.Interval / time.Second),
		DailyTime:   s.DailyTime,
		Timezone:    s.Timezone,
		CronExpr:    s.CronExpr,
		KeepLast:    s.KeepLast,
		MaxAgeDays:  s.MaxAgeDays,
		UpdatedAt:   time.Now().UTC(),
		UpdatedBy:   actor,
	}
}
