package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"agenthub/internal/model"
	"agenthub/internal/registry"
	"agenthub/internal/threads"

	"gorm.io/gorm"
)

type threadRow struct {
	ID           string          `gorm:"primaryKey;size:64"`
	Title        string          `gorm:"size:256;not null"`
	OwnerID      string          `gorm:"size:64;index;not null"`
	Participants model.JSONField `gorm:"type:jsonb"`
	CreatedAt    time.Time       `gorm:"index"`
	UpdatedAt    time.Time
}

func (threadRow) TableName() string { return "threads" }

type threadMessageRow struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	ThreadID   string    `gorm:"size:64;index;not null"`
	AuthorType string    `gorm:"size:16;not null"`
	AuthorID   string    `gorm:"size:64;not null"`
	AuthorName string    `gorm:"size:256"`
	Body       string    `gorm:"type:text;not null"`
	TaskID     string    `gorm:"size:64;index"`
	ResourceID string    `gorm:"size:64;index"`
	CreatedAt  time.Time `gorm:"index"`
}

func (threadMessageRow) TableName() string { return "thread_messages" }

type threadsStore struct{ db *gorm.DB }

// NewThreadsStore returns a threads store backed by db.
func NewThreadsStore(db *gorm.DB) threads.Store {
	return &threadsStore{db: db}
}

func (s *threadsStore) CreateThread(ctx context.Context, t *threads.Thread) (*threads.Thread, error) {
	if t.OwnerID == "" {
		return nil, errors.New("store: create thread: owner is required")
	}
	parts, err := model.NewJSONField(t.Participants)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := threadRow{
		ID:           registry.NewID("thread"),
		Title:        t.Title,
		OwnerID:      t.OwnerID,
		Participants: parts,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if row.Title == "" {
		row.Title = "New thread"
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return s.toThread(&row), nil
}

func (s *threadsStore) GetThread(ctx context.Context, id, ownerID string) (*threads.Thread, error) {
	var row threadRow
	err := s.db.WithContext(ctx).Where("id = ? AND owner_id = ?", id, ownerID).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, threads.ErrNotFound
		}
		return nil, err
	}
	return s.toThread(&row), nil
}

func (s *threadsStore) ListThreads(ctx context.Context, ownerID string) ([]*threads.Thread, error) {
	var rows []threadRow
	if err := s.db.WithContext(ctx).Where("owner_id = ?", ownerID).Order("updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*threads.Thread, 0, len(rows))
	for i := range rows {
		out = append(out, s.toThread(&rows[i]))
	}
	return out, nil
}

func (s *threadsStore) SetParticipants(ctx context.Context, threadID string, resourceIDs []string) error {
	parts, err := model.NewJSONField(resourceIDs)
	if err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&threadRow{}).
		Where("id = ?", threadID).
		Updates(map[string]any{"participants": parts, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return threads.ErrNotFound
	}
	return nil
}

func (s *threadsStore) AddMessage(ctx context.Context, m *threads.Message) (*threads.Message, error) {
	row := threadMessageRow{
		ThreadID:   m.ThreadID,
		AuthorType: m.AuthorType,
		AuthorID:   m.AuthorID,
		AuthorName: m.AuthorName,
		Body:       m.Body,
		TaskID:     m.TaskID,
		ResourceID: m.ResourceID,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return s.toMessage(&row), nil
}

func (s *threadsStore) ListMessages(ctx context.Context, threadID string, sinceSeq int64) ([]*threads.Message, error) {
	q := s.db.WithContext(ctx).Model(&threadMessageRow{}).Where("thread_id = ?", threadID)
	if sinceSeq > 0 {
		q = q.Where("id > ?", sinceSeq)
	}
	var rows []threadMessageRow
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*threads.Message, 0, len(rows))
	for i := range rows {
		out = append(out, s.toMessage(&rows[i]))
	}
	return out, nil
}

func (s *threadsStore) UpsertAgentMessage(ctx context.Context, threadID, taskID, resourceID, body string) (*threads.Message, error) {
	if taskID == "" {
		return nil, errors.New("store: upsert agent message: task id is required")
	}
	var row threadMessageRow
	err := s.db.WithContext(ctx).Where("task_id = ?", taskID).Take(&row).Error
	if err == nil {
		if body != "" {
			if uerr := s.db.WithContext(ctx).Model(&row).Update("body", body).Error; uerr != nil {
				return nil, uerr
			}
			row.Body = body
		}
		return s.toMessage(&row), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	row = threadMessageRow{
		ThreadID:   threadID,
		AuthorType: "agent",
		AuthorID:   resourceID,
		AuthorName: resourceID,
		Body:       body,
		TaskID:     taskID,
		ResourceID: resourceID,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return s.toMessage(&row), nil
}

func (s *threadsStore) toThread(r *threadRow) *threads.Thread {
	var parts []string
	_ = r.Participants.Decode(&parts)
	return &threads.Thread{
		ID:           r.ID,
		Title:        r.Title,
		OwnerID:      r.OwnerID,
		Participants: parts,
		CreatedAt:    r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *threadsStore) toMessage(r *threadMessageRow) *threads.Message {
	return &threads.Message{
		ID:         strconv.FormatUint(uint64(r.ID), 10),
		ThreadID:   r.ThreadID,
		AuthorType: r.AuthorType,
		AuthorID:   r.AuthorID,
		AuthorName: r.AuthorName,
		Body:       r.Body,
		TaskID:     r.TaskID,
		ResourceID: r.ResourceID,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
	}
}
