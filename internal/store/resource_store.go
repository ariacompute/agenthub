package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/model"
	"agenthub/internal/registry"
)

// resourceStore is the PostgreSQL backed registry.ResourceStore.
type resourceStore struct{ db *gorm.DB }

// NewResourceStore returns a resource store backed by db.
func NewResourceStore(db *gorm.DB) registry.ResourceStore {
	return &resourceStore{db: db}
}

func (s *resourceStore) Upsert(ctx context.Context, e *registry.Entry) (*registry.Entry, error) {
	if e == nil || e.ID == "" {
		return nil, fmt.Errorf("store: upsert resource: entry or id is empty")
	}
	row, err := resourceFromEntry(e)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns(resourceUpdateColumns),
	}).Create(row).Error
	if err != nil {
		return nil, err
	}
	return entryFromResource(row)
}

// resourceUpdateColumns are the columns refreshed by an upsert; created_at is
// deliberately absent so it keeps the value of the first insert.
var resourceUpdateColumns = []string{
	"kind", "name", "description", "endpoint", "version", "repository",
	"tags", "capabilities", "auth_schemes", "env", "metadata",
	"transport", "health", "updated_at",
}

func (s *resourceStore) Get(ctx context.Context, id string) (*registry.Entry, error) {
	var row model.Resource
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, registry.ErrNotFound
		}
		return nil, err
	}
	return entryFromResource(&row)
}

func (s *resourceStore) List(ctx context.Context, kind string) ([]*registry.Entry, error) {
	q := s.db.WithContext(ctx).Model(&model.Resource{})
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	var rows []model.Resource
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*registry.Entry, 0, len(rows))
	for i := range rows {
		e, err := entryFromResource(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *resourceStore) Delete(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Resource{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func (s *resourceStore) SetHealth(ctx context.Context, id string, h registry.Health) error {
	res := s.db.WithContext(ctx).Model(&model.Resource{}).
		Where("id = ?", id).
		Updates(map[string]any{"health": string(h), "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func (s *resourceStore) Counts(ctx context.Context) (map[string]int, map[string]int, error) {
	byKind := map[string]int{}
	for _, k := range registry.Kinds {
		byKind[string(k)] = 0
	}
	byHealth := map[string]int{}
	for _, h := range registry.Healths {
		byHealth[string(h)] = 0
	}

	var kindRows []struct {
		Kind string
		Cnt  int64
	}
	if err := s.db.WithContext(ctx).Model(&model.Resource{}).
		Select("kind, count(*) AS cnt").Group("kind").Scan(&kindRows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range kindRows {
		if _, known := byKind[r.Kind]; known {
			byKind[r.Kind] = int(r.Cnt)
		}
	}

	var healthRows []struct {
		Health string
		Cnt    int64
	}
	if err := s.db.WithContext(ctx).Model(&model.Resource{}).
		Select("health, count(*) AS cnt").Group("health").Scan(&healthRows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range healthRows {
		if _, known := byHealth[r.Health]; known {
			byHealth[r.Health] = int(r.Cnt)
		}
	}
	return byKind, byHealth, nil
}

func (s *resourceStore) Size(ctx context.Context) (int, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.Resource{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

func resourceFromEntry(e *registry.Entry) (*model.Resource, error) {
	tags, err := model.NewJSONField(e.Tags)
	if err != nil {
		return nil, err
	}
	caps, err := model.NewJSONField(e.Capabilities)
	if err != nil {
		return nil, err
	}
	auth, err := model.NewJSONField(e.AuthSchemes)
	if err != nil {
		return nil, err
	}
	env, err := model.NewJSONField(e.Env)
	if err != nil {
		return nil, err
	}
	meta, err := model.NewJSONField(e.Metadata)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	createdAt := e.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := e.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	health := e.Health
	if health == "" {
		health = registry.HealthUnknown
	}
	return &model.Resource{
		ID:           e.ID,
		Kind:         string(e.Kind),
		Name:         e.Name,
		Description:  e.Description,
		Endpoint:     e.Endpoint,
		Version:      e.Version,
		Repository:   e.Repository,
		Tags:         tags,
		Capabilities: caps,
		AuthSchemes:  auth,
		Env:          env,
		Metadata:     meta,
		Transport:    string(e.Transport),
		Health:       string(health),
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}

func entryFromResource(row *model.Resource) (*registry.Entry, error) {
	e := &registry.Entry{
		ID:          row.ID,
		Kind:        registry.Kind(row.Kind),
		Name:        row.Name,
		Description: row.Description,
		Endpoint:    row.Endpoint,
		Version:     row.Version,
		Repository:  row.Repository,
		Transport:   registry.Transport(row.Transport),
		Health:      registry.Health(row.Health),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if err := row.Tags.Decode(&e.Tags); err != nil {
		return nil, err
	}
	if err := row.Capabilities.Decode(&e.Capabilities); err != nil {
		return nil, err
	}
	if err := row.AuthSchemes.Decode(&e.AuthSchemes); err != nil {
		return nil, err
	}
	if err := row.Env.Decode(&e.Env); err != nil {
		return nil, err
	}
	if err := row.Metadata.Decode(&e.Metadata); err != nil {
		return nil, err
	}
	return e, nil
}
