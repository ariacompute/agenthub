package store

import (
	"context"
	"time"

	"agenthub/internal/model"
	"agenthub/internal/sandbox"

	"gorm.io/gorm"
)

// sandboxStore implements sandbox.Store over GORM. Its behaviour has to match
// sandbox.MemoryStore line for line — the two are exercised by the same cases,
// and the memory one is what every unit test runs on.
type sandboxStore struct{ db *gorm.DB }

// NewSandboxStore returns a sandbox.Store backed by the shared GORM handle.
func NewSandboxStore(db *gorm.DB) sandbox.Store { return &sandboxStore{db: db} }

func toDomainLease(row *model.SandboxLease) *sandbox.Lease {
	if row == nil {
		return nil
	}
	lease := &sandbox.Lease{
		ID:          row.ID,
		OwnerID:     row.OwnerID,
		ResourceID:  row.ResourceID,
		Scope:       row.Scope,
		Provider:    row.Provider,
		Runtime:     row.Runtime,
		NodeID:      row.NodeID,
		State:       row.State,
		Ref:         row.Ref,
		Execs:       row.Execs,
		LastExit:    row.LastExit,
		LastCommand: row.LastCommand,
		CreatedAt:   row.CreatedAt.UTC(),
		RenewedAt:   row.RenewedAt.UTC(),
		ExpiresAt:   row.ExpiresAt.UTC(),
	}
	if row.Profile != nil {
		_ = row.Profile.Decode(&lease.Profile)
	}
	return lease
}

func fromDomainLease(l *sandbox.Lease) *model.SandboxLease {
	row := &model.SandboxLease{
		ID:          l.ID,
		OwnerID:     l.OwnerID,
		ResourceID:  l.ResourceID,
		Scope:       l.Scope,
		Provider:    l.Provider,
		Runtime:     l.Runtime,
		NodeID:      l.NodeID,
		State:       l.State,
		Ref:         l.Ref,
		Profile:     jsonOrNil(l.Profile),
		Execs:       l.Execs,
		LastExit:    l.LastExit,
		LastCommand: l.LastCommand,
		CreatedAt:   l.CreatedAt,
		RenewedAt:   l.RenewedAt,
		ExpiresAt:   l.ExpiresAt,
	}
	return row
}

func (s *sandboxStore) SaveLease(ctx context.Context, l *sandbox.Lease) error {
	return s.db.WithContext(ctx).Save(fromDomainLease(l)).Error
}

func (s *sandboxStore) GetLease(ctx context.Context, id string) (*sandbox.Lease, error) {
	var row model.SandboxLease
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainLease(&row), nil
}

func (s *sandboxStore) FindLease(ctx context.Context, ownerID, resourceID, scope, provider string) (*sandbox.Lease, error) {
	var row model.SandboxLease
	err := s.db.WithContext(ctx).
		Where("owner_id = ? AND resource_id = ? AND scope = ? AND provider = ? AND state IN ?",
			ownerID, resourceID, scope, provider, []string{sandbox.StateStarting, sandbox.StateReady}).
		Order("renewed_at DESC").
		Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainLease(&row), nil
}

func (s *sandboxStore) ListLeases(ctx context.Context, ownerID string) ([]*sandbox.Lease, error) {
	q := s.db.WithContext(ctx).Model(&model.SandboxLease{})
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	var rows []model.SandboxLease
	if err := q.Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*sandbox.Lease, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainLease(&rows[i]))
	}
	return out, nil
}

func (s *sandboxStore) DeleteLease(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.SandboxLease{}).Error
}

func (s *sandboxStore) ExpiredLeases(ctx context.Context, now time.Time, limit int) ([]*sandbox.Lease, error) {
	q := s.db.WithContext(ctx).
		Where("state = ? AND expires_at < ?", sandbox.StateReady, now).
		Order("expires_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []model.SandboxLease
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*sandbox.Lease, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainLease(&rows[i]))
	}
	return out, nil
}

func (s *sandboxStore) CountLeases(ctx context.Context, ownerID string) (int, error) {
	q := s.db.WithContext(ctx).Model(&model.SandboxLease{}).
		Where("state IN ?", []string{sandbox.StateStarting, sandbox.StateReady})
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *sandboxStore) AppendExec(ctx context.Context, r *sandbox.ExecRecord) error {
	row := &model.SandboxExec{
		ID:           r.ID,
		LeaseID:      r.LeaseID,
		OwnerID:      r.OwnerID,
		Provider:     r.Provider,
		Command:      r.Command,
		ExitCode:     r.ExitCode,
		DurationMS:   r.DurationMS,
		Truncated:    r.Truncated,
		StdoutSHA256: r.StdoutSHA256,
		StderrSHA256: r.StderrSHA256,
		DryRun:       r.DryRun,
		At:           r.At,
	}
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *sandboxStore) ListExecs(ctx context.Context, leaseID string, limit int) ([]*sandbox.ExecRecord, error) {
	q := s.db.WithContext(ctx).Where("lease_id = ?", leaseID).Order("at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []model.SandboxExec
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*sandbox.ExecRecord, 0, len(rows))
	for i := range rows {
		r := rows[i]
		out = append(out, &sandbox.ExecRecord{
			ID: r.ID, LeaseID: r.LeaseID, OwnerID: r.OwnerID, Provider: r.Provider,
			Command: r.Command, ExitCode: r.ExitCode, DurationMS: r.DurationMS, Truncated: r.Truncated,
			StdoutSHA256: r.StdoutSHA256, StderrSHA256: r.StderrSHA256, DryRun: r.DryRun, At: r.At.UTC(),
		})
	}
	return out, nil
}

func (s *sandboxStore) CountExecsSince(ctx context.Context, since time.Time) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.SandboxExec{}).Where("at > ?", since).Count(&n).Error
	return int(n), err
}

func (s *sandboxStore) SaveNode(ctx context.Context, n *sandbox.Node) error {
	row := &model.SandboxNode{
		ID:           n.ID,
		OwnerID:      n.OwnerID,
		Name:         n.Name,
		APIKeyID:     n.APIKeyID,
		Capabilities: jsonOrNil(n.Capabilities),
		Status:       n.Status,
		LastSeenAt:   n.LastSeenAt,
		CreatedAt:    n.CreatedAt,
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *sandboxStore) GetNode(ctx context.Context, id string) (*sandbox.Node, error) {
	var row model.SandboxNode
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	node := &sandbox.Node{
		ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, APIKeyID: row.APIKeyID,
		Status: row.Status, LastSeenAt: row.LastSeenAt.UTC(), CreatedAt: row.CreatedAt.UTC(),
	}
	if row.Capabilities != nil {
		_ = row.Capabilities.Decode(&node.Capabilities)
	}
	return node, nil
}

func (s *sandboxStore) ListNodes(ctx context.Context) ([]*sandbox.Node, error) {
	var rows []model.SandboxNode
	if err := s.db.WithContext(ctx).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*sandbox.Node, 0, len(rows))
	for i := range rows {
		row := rows[i]
		node := &sandbox.Node{
			ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, APIKeyID: row.APIKeyID,
			Status: row.Status, LastSeenAt: row.LastSeenAt.UTC(), CreatedAt: row.CreatedAt.UTC(),
		}
		if row.Capabilities != nil {
			_ = row.Capabilities.Decode(&node.Capabilities)
		}
		out = append(out, node)
	}
	return out, nil
}

func (s *sandboxStore) DeleteNode(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.SandboxNode{}).Error
}

func (s *sandboxStore) SaveJob(ctx context.Context, j *sandbox.Job) error {
	row := &model.SandboxJob{
		ID:         j.ID,
		NodeID:     j.NodeID,
		LeaseID:    j.LeaseID,
		Provider:   j.Provider,
		Op:         j.Op,
		State:      j.State,
		Command:    j.Request.Command,
		ExitCode:   j.Result.ExitCode,
		Error:      j.Error,
		CreatedAt:  j.CreatedAt,
		FinishedAt: j.FinishedAt,
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *sandboxStore) AppendEvent(ctx context.Context, e *sandbox.Event) error {
	row := &model.SandboxEvent{
		Subject: e.Subject,
		Kind:    e.Kind,
		LeaseID: e.LeaseID,
		Payload: jsonOrNil(e.Payload),
		At:      e.At,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return err
	}
	e.Seq = row.Seq
	return nil
}

func (s *sandboxStore) EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*sandbox.Event, error) {
	q := s.db.WithContext(ctx).Where("subject = ? AND seq > ?", subject, since).Order("seq ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []model.SandboxEvent
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*sandbox.Event, 0, len(rows))
	for i := range rows {
		row := rows[i]
		event := &sandbox.Event{Seq: row.Seq, Kind: row.Kind, Subject: row.Subject, LeaseID: row.LeaseID, At: row.At.UTC()}
		if row.Payload != nil {
			_ = row.Payload.Decode(&event.Payload)
		}
		out = append(out, event)
	}
	return out, nil
}

func (s *sandboxStore) MaxSeq(ctx context.Context) (uint64, error) {
	var max *uint64
	err := s.db.WithContext(ctx).Model(&model.SandboxEvent{}).Select("MAX(seq)").Scan(&max).Error
	if err != nil || max == nil {
		return 0, err
	}
	return *max, nil
}
