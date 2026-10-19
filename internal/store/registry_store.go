package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/model"
	"agenthub/internal/registry"
)

// namespaceStore is the PostgreSQL backed registry.NamespaceStore.
type namespaceStore struct{ db *gorm.DB }

// NewNamespaceStore returns a namespace store backed by db.
func NewNamespaceStore(db *gorm.DB) registry.NamespaceStore {
	return &namespaceStore{db: db}
}

func (s *namespaceStore) UpsertNamespace(ctx context.Context, n *registry.Namespace) (*registry.Namespace, error) {
	if n == nil || n.ID == "" {
		return nil, errors.New("store: upsert namespace: namespace or id is empty")
	}
	row := &model.Registry{
		ID:          n.ID,
		Name:        n.Name,
		Description: n.Description,
		AutoApprove: n.AutoApprove,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = time.Now().UTC()
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "description", "auto_approve", "updated_at"}),
	}).Create(row).Error
	if err != nil {
		return nil, err
	}
	return namespaceFromRow(row), nil
}

func (s *namespaceStore) GetNamespace(ctx context.Context, id string) (*registry.Namespace, error) {
	var row model.Registry
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, registry.ErrNotFound
		}
		return nil, err
	}
	return namespaceFromRow(&row), nil
}

func (s *namespaceStore) ListNamespaces(ctx context.Context) ([]*registry.Namespace, error) {
	var rows []model.Registry
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*registry.Namespace, 0, len(rows))
	for i := range rows {
		out = append(out, namespaceFromRow(&rows[i]))
	}
	return out, nil
}

func (s *namespaceStore) DeleteNamespace(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Registry{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func namespaceFromRow(row *model.Registry) *registry.Namespace {
	return &registry.Namespace{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		AutoApprove: row.AutoApprove,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

// sourceStore is the PostgreSQL backed registry.SourceStore.
type sourceStore struct{ db *gorm.DB }

// NewSourceStore returns a registry source store backed by db.
func NewSourceStore(db *gorm.DB) registry.SourceStore {
	return &sourceStore{db: db}
}

func (s *sourceStore) UpsertSource(ctx context.Context, src *registry.RegistrySource) (*registry.RegistrySource, error) {
	if src == nil || src.ID == "" {
		return nil, errors.New("store: upsert source: source or id is empty")
	}
	row := sourceRowFromSource(src)
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"namespace", "kind", "name", "url", "auth_scheme", "auth_token_env",
			"fixture", "enabled", "schedule", "updated_at",
		}),
	}).Create(row).Error
	if err != nil {
		return nil, err
	}
	return sourceFromRow(row), nil
}

func (s *sourceStore) GetSource(ctx context.Context, id string) (*registry.RegistrySource, error) {
	var row model.RegistrySource
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, registry.ErrNotFound
		}
		return nil, err
	}
	return sourceFromRow(&row), nil
}

func (s *sourceStore) ListSources(ctx context.Context, namespace string) ([]*registry.RegistrySource, error) {
	q := s.db.WithContext(ctx).Model(&model.RegistrySource{})
	if namespace != "" {
		q = q.Where("namespace = ?", namespace)
	}
	var rows []model.RegistrySource
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*registry.RegistrySource, 0, len(rows))
	for i := range rows {
		out = append(out, sourceFromRow(&rows[i]))
	}
	return out, nil
}

func (s *sourceStore) DeleteSource(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.RegistrySource{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registry.ErrNotFound
	}
	return nil
}

// MarkSync records the outcome of one run. It only touches the sync columns so
// a concurrent run cannot clobber the configuration another request just saved.
func (s *sourceStore) MarkSync(ctx context.Context, id string, r registry.SyncResult) error {
	updates := map[string]any{
		"last_sync_status": r.Status,
		"last_sync_count":  r.Imported + r.Updated,
		"last_sync_error":  r.Error,
		"updated_at":       time.Now().UTC(),
	}
	// A recorded run always happened at some point; a caller that left At zero
	// still gets a timestamp rather than a stale or null last_sync_at.
	at := r.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	updates["last_sync_at"] = at.UTC()
	res := s.db.WithContext(ctx).Model(&model.RegistrySource{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func sourceRowFromSource(src *registry.RegistrySource) *model.RegistrySource {
	row := &model.RegistrySource{
		ID:             src.ID,
		Namespace:      src.EffectiveNamespace(),
		Kind:           src.Kind,
		Name:           src.Name,
		URL:            src.URL,
		AuthScheme:     src.AuthScheme,
		AuthTokenEnv:   src.AuthTokenEnv,
		Fixture:        src.Fixture,
		Enabled:        src.Enabled,
		Schedule:       src.Schedule,
		LastSyncAt:     src.LastSyncAt,
		LastSyncStatus: src.LastSyncStatus,
		LastSyncCount:  src.LastSyncCount,
		LastSyncError:  src.LastSyncError,
		CreatedAt:      src.CreatedAt,
		UpdatedAt:      src.UpdatedAt,
	}
	if row.Namespace == "" {
		row.Namespace = registry.DefaultNamespace
	}
	if row.AuthScheme == "" {
		row.AuthScheme = registry.AuthSchemeNone
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = time.Now().UTC()
	}
	return row
}

func sourceFromRow(row *model.RegistrySource) *registry.RegistrySource {
	src := &registry.RegistrySource{
		ID:             row.ID,
		Namespace:      row.Namespace,
		Kind:           row.Kind,
		Name:           row.Name,
		URL:            row.URL,
		AuthScheme:     row.AuthScheme,
		AuthTokenEnv:   row.AuthTokenEnv,
		Fixture:        row.Fixture,
		Enabled:        row.Enabled,
		Schedule:       row.Schedule,
		LastSyncAt:     row.LastSyncAt,
		LastSyncStatus: row.LastSyncStatus,
		LastSyncCount:  row.LastSyncCount,
		LastSyncError:  row.LastSyncError,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if src.Namespace == "" {
		src.Namespace = registry.DefaultNamespace
	}
	return src
}
