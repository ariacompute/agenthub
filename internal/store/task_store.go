package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/a2a"
	"agenthub/internal/hub"
	"agenthub/internal/model"
)

// taskRepository is the PostgreSQL backed hub.TaskRepository.
type taskRepository struct{ db *gorm.DB }

// NewTaskRepository returns a task repository backed by db.
func NewTaskRepository(db *gorm.DB) hub.TaskRepository {
	return &taskRepository{db: db}
}

func (s *taskRepository) Create(ctx context.Context, t *a2a.Task) error {
	if t == nil || t.ID == "" {
		return fmt.Errorf("store: create task: task or id is empty")
	}
	row, err := taskFromTask(t)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *taskRepository) Get(ctx context.Context, id string) (*a2a.Task, error) {
	var row model.Task
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, hub.ErrTaskNotFound
		}
		return nil, err
	}
	arts, err := s.artifacts(ctx, id)
	if err != nil {
		return nil, err
	}
	return taskFromRow(&row, arts)
}

func (s *taskRepository) List(ctx context.Context) ([]*a2a.Task, error) {
	var rows []model.Task
	if err := s.db.WithContext(ctx).Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []*a2a.Task{}, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}

	// One query for every artifact of the page keeps listing O(1) round trips.
	var artRows []model.TaskArtifact
	if err := s.db.WithContext(ctx).Where("task_id IN ?", ids).
		Order("task_id ASC, id ASC").Find(&artRows).Error; err != nil {
		return nil, err
	}
	grouped := make(map[string][]a2a.Artifact, len(ids))
	for i := range artRows {
		art, err := artifactFromRow(&artRows[i])
		if err != nil {
			return nil, err
		}
		grouped[artRows[i].TaskID] = append(grouped[artRows[i].TaskID], art)
	}

	out := make([]*a2a.Task, 0, len(rows))
	for i := range rows {
		task, err := taskFromRow(&rows[i], grouped[rows[i].ID])
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *taskRepository) SetState(ctx context.Context, id string, from, to a2a.TaskState, msg *a2a.Message) (*a2a.Task, error) {
	statusMsg, err := model.NewJSONField(msg)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	// The state filter makes the update conditional: a concurrent writer that
	// already advanced the task matches zero rows instead of clobbering it.
	res := s.db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND state = ?", id, string(from)).
		Updates(map[string]any{
			"state":            string(to),
			"status_message":   statusMsg,
			"status_timestamp": a2a.Timestamp(now),
			"updated_at":       now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		exists, err := s.exists(ctx, id)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, hub.ErrTaskNotFound
		}
		return nil, hub.ErrStateConflict
	}
	return s.Get(ctx, id)
}

func (s *taskRepository) AddArtifact(ctx context.Context, id string, art a2a.Artifact) (*a2a.Task, error) {
	exists, err := s.exists(ctx, id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, hub.ErrTaskNotFound
	}
	parts, err := model.NewJSONField(art.Parts)
	if err != nil {
		return nil, err
	}
	meta, err := model.NewJSONField(art.Metadata)
	if err != nil {
		return nil, err
	}
	row := &model.TaskArtifact{
		TaskID:      id,
		ArtifactID:  art.ArtifactID,
		Name:        art.Name,
		Description: art.Description,
		Parts:       parts,
		Idx:         art.Index,
		Append:      art.Append,
		LastChunk:   art.LastChunk,
		Metadata:    meta,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *taskRepository) AppendHistory(ctx context.Context, id string, msg a2a.Message) error {
	// History is a single jsonb column, so the read-modify-write runs inside a
	// transaction that locks the row: parallel flow branches must not lose a
	// turn.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return hub.ErrTaskNotFound
			}
			return err
		}
		var history []a2a.Message
		if err := row.History.Decode(&history); err != nil {
			return err
		}
		history = append(history, msg)
		encoded, err := model.NewJSONField(history)
		if err != nil {
			return err
		}
		return tx.Model(&model.Task{}).Where("id = ?", id).
			Updates(map[string]any{"history": encoded, "updated_at": time.Now().UTC()}).Error
	})
}

func (s *taskRepository) AddSpan(ctx context.Context, span hub.Span) error {
	if span.ID == "" {
		return fmt.Errorf("store: add span: span id is empty")
	}
	row := &model.TaskSpan{
		ID:         span.ID,
		TaskID:     span.TaskID,
		ResourceID: span.ResourceID,
		Kind:       span.Kind,
		Label:      span.Label,
		StartAt:    span.StartAt,
		EndAt:      span.EndAt,
		DurationMS: span.DurationMS,
		Status:     span.Status,
		Code:       span.Code,
		CreatedAt:  time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func (s *taskRepository) Spans(ctx context.Context, id string) ([]hub.Span, error) {
	var rows []model.TaskSpan
	if err := s.db.WithContext(ctx).Where("task_id = ?", id).
		Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]hub.Span, 0, len(rows))
	for _, r := range rows {
		out = append(out, hub.Span{
			ID:         r.ID,
			TaskID:     r.TaskID,
			ResourceID: r.ResourceID,
			Kind:       r.Kind,
			Label:      r.Label,
			StartAt:    r.StartAt,
			EndAt:      r.EndAt,
			DurationMS: r.DurationMS,
			Status:     r.Status,
			Code:       r.Code,
		})
	}
	return out, nil
}

func (s *taskRepository) SetPending(ctx context.Context, id string, p *hub.PendingInput) error {
	if p == nil {
		return s.db.WithContext(ctx).Where("task_id = ?", id).Delete(&model.TaskPending{}).Error
	}
	row := &model.TaskPending{
		TaskID:    id,
		Prompt:    p.Prompt,
		AskedAt:   p.AskedAt,
		UpdatedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"prompt", "asked_at", "updated_at"}),
	}).Create(row).Error
}

func (s *taskRepository) Pending(ctx context.Context, id string) (*hub.PendingInput, error) {
	var row model.TaskPending
	err := s.db.WithContext(ctx).Where("task_id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &hub.PendingInput{Prompt: row.Prompt, AskedAt: row.AskedAt}, nil
}

func (s *taskRepository) SetPush(ctx context.Context, id string, cfg *a2a.PushNotificationConfig) error {
	if cfg == nil {
		return s.db.WithContext(ctx).Where("task_id = ?", id).Delete(&model.TaskPush{}).Error
	}
	encoded, err := model.NewJSONField(cfg)
	if err != nil {
		return err
	}
	row := &model.TaskPush{TaskID: id, Config: encoded, UpdatedAt: time.Now().UTC()}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"config", "updated_at"}),
	}).Create(row).Error
}

func (s *taskRepository) Push(ctx context.Context, id string) (*a2a.PushNotificationConfig, error) {
	var row model.TaskPush
	err := s.db.WithContext(ctx).Where("task_id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var cfg a2a.PushNotificationConfig
	if err := row.Config.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *taskRepository) exists(ctx context.Context, id string) (bool, error) {
	var row model.Task
	err := s.db.WithContext(ctx).Select("id").Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *taskRepository) artifacts(ctx context.Context, taskID string) ([]a2a.Artifact, error) {
	var rows []model.TaskArtifact
	if err := s.db.WithContext(ctx).Where("task_id = ?", taskID).
		Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]a2a.Artifact, 0, len(rows))
	for i := range rows {
		art, err := artifactFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, art)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func artifactFromRow(row *model.TaskArtifact) (a2a.Artifact, error) {
	art := a2a.Artifact{
		ArtifactID:  row.ArtifactID,
		Name:        row.Name,
		Description: row.Description,
		Index:       row.Idx,
		Append:      row.Append,
		LastChunk:   row.LastChunk,
	}
	if err := row.Parts.Decode(&art.Parts); err != nil {
		return art, err
	}
	if err := row.Metadata.Decode(&art.Metadata); err != nil {
		return art, err
	}
	return art, nil
}

func taskFromTask(t *a2a.Task) (*model.Task, error) {
	history, err := model.NewJSONField(t.History)
	if err != nil {
		return nil, err
	}
	meta, err := model.NewJSONField(t.Metadata)
	if err != nil {
		return nil, err
	}
	statusMsg, err := model.NewJSONField(t.Status.Message)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &model.Task{
		ID:              t.ID,
		Kind:            t.Kind,
		ContextID:       t.ContextID,
		SessionID:       t.SessionID,
		ResourceID:      t.ResourceID,
		State:           string(t.Status.State),
		StatusMessage:   statusMsg,
		StatusTimestamp: t.Status.Timestamp,
		History:         history,
		Metadata:        meta,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func taskFromRow(row *model.Task, artifacts []a2a.Artifact) (*a2a.Task, error) {
	task := &a2a.Task{
		Kind:       row.Kind,
		ID:         row.ID,
		ContextID:  row.ContextID,
		SessionID:  row.SessionID,
		Status:     a2a.TaskStatus{State: a2a.TaskState(row.State), Timestamp: row.StatusTimestamp},
		Artifacts:  artifacts,
		ResourceID: row.ResourceID,
	}
	if len(row.StatusMessage) > 0 {
		var msg a2a.Message
		if err := row.StatusMessage.Decode(&msg); err != nil {
			return nil, err
		}
		task.Status.Message = &msg
	}
	if err := row.History.Decode(&task.History); err != nil {
		return nil, err
	}
	if err := row.Metadata.Decode(&task.Metadata); err != nil {
		return nil, err
	}
	return task, nil
}
