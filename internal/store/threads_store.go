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
	ID         uint   `gorm:"primaryKey;autoIncrement"`
	ThreadID   string `gorm:"size:64;index;not null"`
	AuthorType string `gorm:"size:16;not null"`
	AuthorID   string `gorm:"size:64;not null"`
	AuthorName string `gorm:"size:256"`
	Body       string `gorm:"type:text;not null"`
	TaskID     string `gorm:"size:64;index"`
	ResourceID string `gorm:"size:64;index"`
	Origin     string `gorm:"size:16;not null;default:''"`
	// OwnerID mirrors the parent thread, so thread_messages can sit under the
	// policy in migrations/030_threads_rls.sql.
	OwnerID   string    `gorm:"size:64;index;not null;default:''"`
	CreatedAt time.Time `gorm:"index"`
}

func (threadMessageRow) TableName() string { return "thread_messages" }

// threadsStore implements threads.Store over GORM.
//
// Both tables are under forced row level security since
// migrations/030_threads_rls.sql, so every method runs through
// scoped/withIdentity: a statement on a pool connection with no identity
// compares the owner against NULL and touches nothing. That is also why the
// message writes read their owner from the parent thread inside the same
// transaction — the row has to carry an owner the policy will accept.
type threadsStore struct{ db *gorm.DB }

// NewThreadsStore returns a threads store backed by db.
func NewThreadsStore(db *gorm.DB) threads.Store {
	return &threadsStore{db: db}
}

// threadOwner reads the owner a message on threadID has to carry. It runs inside
// the caller's transaction, so the row level security policy decides whether the
// thread is visible at all: an owner mismatch comes back as ErrNotFound, never as
// an empty owner.
func threadOwner(db *gorm.DB, threadID string) (string, error) {
	var row struct{ OwnerID string }
	res := db.Model(&threadRow{}).Select("owner_id").Where("id = ?", threadID).Limit(1).Scan(&row)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", threads.ErrNotFound
	}
	return row.OwnerID, nil
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
	if err := withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return db.Create(&row).Error
	}); err != nil {
		return nil, err
	}
	return s.toThread(&row), nil
}

func (s *threadsStore) GetThread(ctx context.Context, id, ownerID string) (*threads.Thread, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*threads.Thread, error) {
		var row threadRow
		if err := ownedWhere(db, id, ownerID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, threads.ErrNotFound
			}
			return nil, err
		}
		return s.toThread(&row), nil
	})
}

// GetThreadUnscoped is the webhook's read: no ownership predicate, always under
// the process identity. It exists because a channel is bound to a thread and the
// inbound path has to reach that thread's participants before any session
// exists.
func (s *threadsStore) GetThreadUnscoped(ctx context.Context, id string) (*threads.Thread, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*threads.Thread, error) {
		var row threadRow
		if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, threads.ErrNotFound
			}
			return nil, err
		}
		return s.toThread(&row), nil
	})
}

func (s *threadsStore) ListThreads(ctx context.Context, ownerID string) ([]*threads.Thread, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*threads.Thread, error) {
		var rows []threadRow
		if err := db.Where("owner_id = ?", ownerID).Order("updated_at DESC").Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make([]*threads.Thread, 0, len(rows))
		for i := range rows {
			out = append(out, s.toThread(&rows[i]))
		}
		return out, nil
	})
}

func (s *threadsStore) SetParticipants(ctx context.Context, threadID string, resourceIDs []string) error {
	parts, err := model.NewJSONField(resourceIDs)
	if err != nil {
		return err
	}
	return withIdentity(ctx, s.db, func(db *gorm.DB) error {
		res := db.Model(&threadRow{}).
			Where("id = ?", threadID).
			Updates(map[string]any{"participants": parts, "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return threads.ErrNotFound
		}
		return nil
	})
}

func (s *threadsStore) AddMessage(ctx context.Context, m *threads.Message) (*threads.Message, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*threads.Message, error) {
		owner, err := threadOwner(db, m.ThreadID)
		if err != nil {
			return nil, err
		}
		row := threadMessageRow{
			ThreadID:   m.ThreadID,
			AuthorType: m.AuthorType,
			AuthorID:   m.AuthorID,
			AuthorName: m.AuthorName,
			Body:       m.Body,
			TaskID:     m.TaskID,
			ResourceID: m.ResourceID,
			Origin:     m.Origin,
			OwnerID:    owner,
			CreatedAt:  time.Now().UTC(),
		}
		if err := db.Create(&row).Error; err != nil {
			return nil, err
		}
		return s.toMessage(&row), nil
	})
}

func (s *threadsStore) ListMessages(ctx context.Context, threadID string, sinceSeq int64) ([]*threads.Message, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*threads.Message, error) {
		q := db.Model(&threadMessageRow{}).Where("thread_id = ?", threadID)
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
	})
}

func (s *threadsStore) UpsertAgentMessage(ctx context.Context, threadID, taskID, resourceID, body string) (*threads.Message, error) {
	if taskID == "" {
		return nil, errors.New("store: upsert agent message: task id is required")
	}
	return scoped(ctx, s.db, func(db *gorm.DB) (*threads.Message, error) {
		var row threadMessageRow
		err := db.Where("task_id = ?", taskID).Take(&row).Error
		if err == nil {
			if body != "" {
				if uerr := db.Model(&row).Update("body", body).Error; uerr != nil {
					return nil, uerr
				}
				row.Body = body
			}
			return s.toMessage(&row), nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		owner, oerr := threadOwner(db, threadID)
		if oerr != nil {
			return nil, oerr
		}
		row = threadMessageRow{
			ThreadID:   threadID,
			AuthorType: "agent",
			AuthorID:   resourceID,
			AuthorName: resourceID,
			Body:       body,
			TaskID:     taskID,
			ResourceID: resourceID,
			Origin:     "agent",
			OwnerID:    owner,
			CreatedAt:  time.Now().UTC(),
		}
		if err := db.Create(&row).Error; err != nil {
			return nil, err
		}
		return s.toMessage(&row), nil
	})
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
		Origin:     r.Origin,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
	}
}
