package store

import (
	"context"
	"errors"
	"time"

	"agenthub/internal/files"

	"gorm.io/gorm"
)

// sharedFileRow is one row of shared_files. The row carries the object key
// rather than the bytes; the bytes live behind files.Backend.
type sharedFileRow struct {
	ID      string `gorm:"primaryKey;size:64"`
	OwnerID string `gorm:"size:64;index;not null;default:''"`
	Name    string `gorm:"size:256;not null"`
	MIME    string `gorm:"size:128;not null;default:''"`
	// Size is the byte length. BIGINT because a document can be large and an int
	// column would cap it silently.
	Size   int64  `gorm:"not null;default:0"`
	SHA256 string `gorm:"size:64;not null;default:''"`
	// Backend records which store held the bytes when the row was written, so a
	// later switch of AGENTHUB_FILES_STORE is visible instead of mysterious.
	Backend   string    `gorm:"size:16;not null;default:''"`
	ObjectKey string    `gorm:"size:128;not null;default:''"`
	CreatedAt time.Time `gorm:"index"`
}

func (sharedFileRow) TableName() string { return "shared_files" }

// filesStore implements files.Store over GORM.
//
// shared_files is under forced row level security (migrations/031), so every
// method runs through scoped/withIdentity. The explicit owner predicate is kept
// as well, for the same reason channels and schedules keep theirs: two
// enforcement points, and the second one still works if a policy is dropped by
// accident.
type filesStore struct{ db *gorm.DB }

// NewFilesStore returns a files.Store backed by the shared GORM handle.
func NewFilesStore(db *gorm.DB) files.Store { return &filesStore{db: db} }

func (s *filesStore) Create(ctx context.Context, f *files.File) (*files.File, error) {
	if f == nil || f.ID == "" || f.OwnerID == "" {
		return nil, files.ErrForbidden
	}
	created := f.CreatedAt
	at := time.Now().UTC()
	if created != "" {
		if parsed, err := time.Parse(time.RFC3339, created); err == nil {
			at = parsed
		}
	}
	row := sharedFileRow{
		ID:        f.ID,
		OwnerID:   f.OwnerID,
		Name:      f.Name,
		MIME:      f.MIME,
		Size:      f.Size,
		SHA256:    f.SHA256,
		Backend:   f.Backend,
		ObjectKey: f.ObjectKey,
		CreatedAt: at,
	}
	if err := withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return db.Create(&row).Error
	}); err != nil {
		return nil, err
	}
	return fileFromRow(&row), nil
}

func (s *filesStore) Get(ctx context.Context, id, ownerID string) (*files.File, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*files.File, error) {
		var row sharedFileRow
		if err := ownedWhere(db, id, ownerID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return fileFromRow(&row), nil
	})
}

func (s *filesStore) List(ctx context.Context, ownerID string) ([]*files.File, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]*files.File, error) {
		var rows []sharedFileRow
		if err := db.Where("owner_id = ?", ownerID).Order("created_at DESC").Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make([]*files.File, 0, len(rows))
		for i := range rows {
			out = append(out, fileFromRow(&rows[i]))
		}
		return out, nil
	})
}

func (s *filesStore) Delete(ctx context.Context, id, ownerID string) (bool, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (bool, error) {
		res := ownedWhere(db, id, ownerID).Delete(&sharedFileRow{})
		if res.Error != nil {
			return false, res.Error
		}
		return res.RowsAffected > 0, nil
	})
}

func fileFromRow(row *sharedFileRow) *files.File {
	return &files.File{
		ID:        row.ID,
		OwnerID:   row.OwnerID,
		Name:      row.Name,
		MIME:      row.MIME,
		Size:      row.Size,
		SHA256:    row.SHA256,
		Backend:   row.Backend,
		ObjectKey: row.ObjectKey,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}
}
