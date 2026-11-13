package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/audit"
	"agenthub/internal/model"
)

// auditStore is the PostgreSQL implementation of audit.Store.
//
// Every method runs under the identity carried by ctx. The policies in
// 026_rls_policies.sql allow INSERT only for the system identity, SELECT for
// system / bypass / audit_read, and DELETE only for the retention identity, so a
// bug here cannot silently rewrite history.
type auditStore struct{ db *gorm.DB }

// NewAuditStore returns an audit.Store backed by db.
func NewAuditStore(db *gorm.DB) audit.Store {
	return &auditStore{db: db}
}

func (s *auditStore) Append(ctx context.Context, events []audit.Event) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([]model.AuditLog, 0, len(events))
	for _, ev := range events {
		meta, err := model.NewJSONField(ev.Metadata)
		if err != nil {
			// Metadata is decorative; losing it must not lose the event.
			meta = nil
		}
		rows = append(rows, model.AuditLog{
			EventID:      ev.EventID,
			Ts:           ev.Time.UTC(),
			RequestID:    ev.RequestID,
			Source:       ev.Source,
			ActorKind:    ev.ActorKind,
			ActorID:      ev.ActorID,
			ActorLabel:   ev.ActorLabel,
			OwnerID:      ev.OwnerID,
			IP:           ev.IP,
			Method:       ev.Method,
			Path:         ev.Path,
			Action:       ev.Action,
			ResourceID:   ev.ResourceID,
			ResourceKind: ev.ResourceKind,
			Status:       ev.Status,
			ErrorCode:    ev.ErrorCode,
			ReadOnly:     ev.ReadOnly,
			LatencyMS:    ev.LatencyMS,
			UserAgent:    ev.UserAgent,
			Metadata:     meta,
			PrevHash:     ev.PrevHash,
			Hash:         ev.Hash,
		})
	}
	return withIdentity(ctx, s.db, func(db *gorm.DB) error {
		return db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "event_id"}},
			DoNothing: true,
		}).Create(&rows).Error
	})
}

// chainTail is the type parameter of LastHash: scoped needs a single result
// value, and a two value result cannot be a type parameter.
type chainTail struct {
	Hash string
	ID   int64
}

func (s *auditStore) LastHash(ctx context.Context) (string, int64, error) {
	tail, err := scoped(ctx, s.db, func(db *gorm.DB) (chainTail, error) {
		var row model.AuditLog
		if err := db.Order("id DESC").Limit(1).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return chainTail{}, nil
			}
			return chainTail{}, err
		}
		return chainTail{Hash: row.Hash, ID: row.ID}, nil
	})
	return tail.Hash, tail.ID, err
}

// Anchor reads the trusted seed left by the last retention pass. An empty
// hash means the trail has never been pruned through the recorder.
func (s *auditStore) Anchor(ctx context.Context) (audit.Anchor, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (audit.Anchor, error) {
		// Find, not Take: an unpruned trail is the normal case, and Take would
		// log "record not found" on every /api/audit/verify call.
		var rows []model.AuditChainAnchor
		if err := db.Where("id = ?", 1).Limit(1).Find(&rows).Error; err != nil {
			return audit.Anchor{}, err
		}
		if len(rows) == 0 {
			return audit.Anchor{}, nil
		}
		return audit.Anchor{Hash: rows[0].Hash, EventID: rows[0].EventID}, nil
	})
}

func (s *auditStore) List(ctx context.Context, q audit.Query) ([]audit.Event, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) ([]audit.Event, error) {
		var rows []model.AuditLog
		limit := q.Limit
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		if err := filtered(db, q).Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
			return nil, err
		}
		return auditEventsFromRows(rows)
	})
}

func (s *auditStore) Get(ctx context.Context, eventID string) (*audit.Event, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (*audit.Event, error) {
		var row model.AuditLog
		err := db.Where("event_id = ?", eventID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		ev, err := auditEventFromRow(&row)
		if err != nil {
			return nil, err
		}
		return &ev, nil
	})
}

func (s *auditStore) Stats(ctx context.Context, q audit.Query) (audit.Stats, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (audit.Stats, error) {
		var stats audit.Stats
		base := func() *gorm.DB { return filtered(db.Model(&model.AuditLog{}), q) }
		if err := base().Count(&stats.Total).Error; err != nil {
			return stats, err
		}
		denied := []int{401, 403, 429}
		if err := base().Where("status IN ?", denied).Count(&stats.Denied).Error; err != nil {
			return stats, err
		}
		if err := base().Where("status >= 400 AND status NOT IN ?", denied).
			Count(&stats.Failed).Error; err != nil {
			return stats, err
		}
		if err := base().Where("read_only = ?", false).Count(&stats.Written).Error; err != nil {
			return stats, err
		}
		return stats, nil
	})
}

func (s *auditStore) Segment(ctx context.Context, from time.Time, afterID int64, limit int) ([]audit.Event, error) {
	if limit <= 0 {
		limit = 5000
	}
	return scoped(ctx, s.db, func(db *gorm.DB) ([]audit.Event, error) {
		var rows []model.AuditLog
		query := db.Order("id ASC").Limit(limit)
		if !from.IsZero() {
			query = query.Where("ts >= ?", from.UTC())
		}
		if afterID > 0 {
			query = query.Where("id > ?", afterID)
		}
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		return auditEventsFromRows(rows)
	})
}

func (s *auditStore) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	return scoped(ctx, s.db, func(db *gorm.DB) (int64, error) {
		// Retention removes a prefix of the chain. Remember the last row that
		// disappears: the survivor still points at it through prev_hash, and
		// without that seed a verifier starting from "" calls a healthy chain
		// broken. Read, delete and anchor write share one transaction, so a
		// crash cannot leave a head with no known predecessor. The retention
		// identity is what the policies require for both the delete and the
		// anchor write.
		cutoff = cutoff.UTC()
		// Find keeps a no-op prune quiet: an empty window is the common case.
		var tails []model.AuditLog
		if err := db.Where("ts < ?", cutoff).Order("id DESC").Limit(1).Find(&tails).Error; err != nil {
			return 0, err
		}
		if len(tails) == 0 {
			return 0, nil
		}
		res := db.Where("ts < ?", cutoff).Delete(&model.AuditLog{})
		if res.Error != nil {
			return 0, res.Error
		}
		if res.RowsAffected == 0 {
			return 0, nil
		}
		anchor := model.AuditChainAnchor{
			ID:       1,
			Hash:     tails[0].Hash,
			EventID:  tails[0].EventID,
			PrunedAt: time.Now().UTC(),
		}
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"hash", "event_id", "pruned_at"}),
		}).Create(&anchor).Error; err != nil {
			return 0, err
		}
		return res.RowsAffected, nil
	})
}

// filtered applies the shared WHERE clauses of List and Stats so the counters
// always describe the same rows the table shows.
func filtered(db *gorm.DB, q audit.Query) *gorm.DB {
	if !q.From.IsZero() {
		db = db.Where("ts >= ?", q.From.UTC())
	}
	if !q.To.IsZero() {
		db = db.Where("ts < ?", q.To.UTC())
	}
	if q.ActorID != "" {
		db = db.Where("actor_id = ?", q.ActorID)
	}
	if q.Action != "" {
		db = db.Where("action = ?", q.Action)
	}
	if q.ResourceID != "" {
		db = db.Where("resource_id = ?", q.ResourceID)
	}
	if q.Source != "" {
		db = db.Where("source = ?", q.Source)
	}
	switch q.Outcome {
	case "ok":
		db = db.Where("status < 400")
	case "denied":
		db = db.Where("status IN ?", []int{401, 403, 429})
	case "failed":
		db = db.Where("status >= 400 AND status NOT IN ?", []int{401, 403, 429})
	}
	if q.Search != "" {
		like := "%" + strings.TrimSpace(q.Search) + "%"
		db = db.Where("path ILIKE ? OR action ILIKE ? OR request_id = ? OR resource_id = ?",
			like, like, q.Search, q.Search)
	}
	if q.Before > 0 {
		db = db.Where("id < ?", q.Before)
	}
	return db
}

func auditEventsFromRows(rows []model.AuditLog) ([]audit.Event, error) {
	out := make([]audit.Event, 0, len(rows))
	for i := range rows {
		ev, err := auditEventFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func auditEventFromRow(row *model.AuditLog) (audit.Event, error) {
	ev := audit.Event{
		Seq:          row.ID,
		EventID:      row.EventID,
		Time:         row.Ts.UTC(),
		RequestID:    row.RequestID,
		Source:       row.Source,
		ActorKind:    row.ActorKind,
		ActorID:      row.ActorID,
		ActorLabel:   row.ActorLabel,
		OwnerID:      row.OwnerID,
		IP:           row.IP,
		Method:       row.Method,
		Path:         row.Path,
		Action:       row.Action,
		ResourceID:   row.ResourceID,
		ResourceKind: row.ResourceKind,
		Status:       row.Status,
		ErrorCode:    row.ErrorCode,
		ReadOnly:     row.ReadOnly,
		LatencyMS:    row.LatencyMS,
		UserAgent:    row.UserAgent,
		PrevHash:     row.PrevHash,
		Hash:         row.Hash,
	}
	if err := row.Metadata.Decode(&ev.Metadata); err != nil {
		return ev, err
	}
	return ev, nil
}
