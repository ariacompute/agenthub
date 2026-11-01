package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"agenthub/internal/model"
	"agenthub/internal/router"

	"gorm.io/gorm"
)

// routerStore implements router.Store over GORM. Its behaviour has to match the
// in-memory implementation in package router line for line, which is what the
// integration test pins: the unit tests run on memory, production runs here.
type routerStore struct{ db *gorm.DB }

// NewRouterStore returns a router.Store backed by the shared GORM handle.
func NewRouterStore(db *gorm.DB) router.Store { return &routerStore{db: db} }

func jsonOrNil(v any) model.JSONField {
	field, err := model.NewJSONField(v)
	if err != nil {
		slog.Warn("router store: encode json", "error", err)
		return nil
	}
	return field
}

func toDomainTrigger(r *model.RouterTrigger) *router.Trigger {
	t := &router.Trigger{
		ID:          r.ID,
		Source:      r.Source,
		SourceID:    r.SourceID,
		ThreadID:    r.ThreadID,
		ActorID:     r.ActorID,
		ActorName:   r.ActorName,
		ActorKind:   r.ActorKind,
		Kind:        r.Kind,
		Body:        r.Body,
		Explicit:    r.Explicit,
		DedupKey:    r.DedupKey,
		Disposition: r.Disposition,
		DecisionID:  r.DecisionID,
		TicketID:    r.TicketID,
		Handbacks:   r.Handbacks,
		DueAt:       r.DueAt.UTC(),
		At:          r.At.UTC(),
		UpdatedAt:   r.UpdatedAt.UTC(),
	}
	if len(r.Meta) > 0 {
		var meta map[string]any
		if err := r.Meta.Decode(&meta); err == nil {
			t.Meta = meta
		}
	}
	return t
}

func fromDomainTrigger(t *router.Trigger) model.RouterTrigger {
	return model.RouterTrigger{
		ID:          t.ID,
		Source:      t.Source,
		SourceID:    t.SourceID,
		ThreadID:    t.ThreadID,
		ActorID:     t.ActorID,
		ActorName:   t.ActorName,
		ActorKind:   t.ActorKind,
		Kind:        t.Kind,
		Body:        t.Body,
		Explicit:    t.Explicit,
		Meta:        jsonOrNil(t.Meta),
		DedupKey:    t.DedupKey,
		Disposition: t.Disposition,
		DecisionID:  t.DecisionID,
		TicketID:    t.TicketID,
		Handbacks:   t.Handbacks,
		DueAt:       t.DueAt.UTC(),
		At:          t.At.UTC(),
		UpdatedAt:   t.UpdatedAt.UTC(),
	}
}

func (s *routerStore) AppendTrigger(ctx context.Context, t *router.Trigger) (bool, error) {
	row := fromDomainTrigger(t)
	// A dedup collision is the normal path for a redelivered webhook, not an
	// error: ON CONFLICT DO NOTHING covers both the primary key and the partial
	// unique index on dedup_key, and RowsAffected tells the caller it was not
	// fresh.
	res := s.db.WithContext(ctx).Exec(
		`INSERT INTO router_triggers (id, source, source_id, thread_id, actor_id, actor_name, actor_kind,
			kind, body, explicit, meta, dedup_key, disposition, decision_id, ticket_id, handbacks,
			due_at, at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		row.ID, row.Source, row.SourceID, row.ThreadID, row.ActorID, row.ActorName, row.ActorKind,
		row.Kind, row.Body, row.Explicit, row.Meta, row.DedupKey, row.Disposition, row.DecisionID,
		row.TicketID, row.Handbacks, row.DueAt, row.At, row.UpdatedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (s *routerStore) ClaimDueTriggers(ctx context.Context, now time.Time, limit int) ([]*router.Trigger, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []*router.Trigger
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.RouterTrigger
		if err := tx.Where("disposition = ? AND due_at <= ?", string(router.DispositionPending), now.UTC()).
			Order("due_at").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			// Push the due time past the claim window so a concurrent pass cannot
			// take the same row twice.
			rows[i].DueAt = now.UTC().Add(time.Minute)
			if err := tx.Model(&model.RouterTrigger{}).Where("id = ?", rows[i].ID).
				Update("due_at", rows[i].DueAt).Error; err != nil {
				return err
			}
			out = append(out, toDomainTrigger(&rows[i]))
		}
		return nil
	})
	return out, err
}

func (s *routerStore) SaveTrigger(ctx context.Context, t *router.Trigger) error {
	return s.db.WithContext(ctx).Save(ptrTrigger(t)).Error
}

func ptrTrigger(t *router.Trigger) *model.RouterTrigger {
	row := fromDomainTrigger(t)
	return &row
}

func (s *routerStore) GetTrigger(ctx context.Context, id string) (*router.Trigger, error) {
	var row model.RouterTrigger
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainTrigger(&row), nil
}

func (s *routerStore) ListTriggers(ctx context.Context, q router.TriggerQuery) ([]*router.Trigger, error) {
	q.Normalize()
	tx := s.db.WithContext(ctx).Model(&model.RouterTrigger{})
	if q.Source != "" {
		tx = tx.Where("source = ?", q.Source)
	}
	if q.Disposition != "" {
		tx = tx.Where("disposition = ?", q.Disposition)
	}
	if q.ThreadID != "" {
		tx = tx.Where("thread_id = ?", q.ThreadID)
	}
	if q.TicketID != "" {
		tx = tx.Where("ticket_id = ?", q.TicketID)
	}
	var rows []model.RouterTrigger
	if err := tx.Order("at DESC").Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*router.Trigger, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainTrigger(&rows[i]))
	}
	return out, nil
}

func (s *routerStore) CountTriggers(ctx context.Context, disposition string) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.RouterTrigger{}).
		Where("disposition = ?", disposition).Count(&n).Error
	return int(n), err
}

func (s *routerStore) SaveThread(ctx context.Context, th *router.Thread) error {
	row := &model.RouterThread{
		ID:           th.ID,
		Source:       th.Source,
		Title:        th.Title,
		OpenedBy:     th.OpenedBy,
		Closed:       th.Closed,
		CloseOutcome: th.CloseOutcome,
		UpdatedAt:    th.UpdatedAt.UTC(),
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *routerStore) GetThread(ctx context.Context, id string) (*router.Thread, error) {
	var row model.RouterThread
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &router.Thread{
		ID: row.ID, Source: row.Source, Title: row.Title, OpenedBy: row.OpenedBy,
		Closed: row.Closed, CloseOutcome: row.CloseOutcome, UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

func (s *routerStore) SaveSubscription(ctx context.Context, sub *router.Subscription) error {
	row := &model.RouterSubscription{
		ThreadID: sub.ThreadID,
		TicketID: sub.TicketID,
		Own:      sub.Own,
		Context:  sub.Context,
		Closed:   sub.Closed,
		At:       sub.At.UTC(),
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *routerStore) DropSubscription(ctx context.Context, threadID, ticketID string) error {
	return s.db.WithContext(ctx).Where("thread_id = ? AND ticket_id = ?", threadID, ticketID).
		Delete(&model.RouterSubscription{}).Error
}

func (s *routerStore) ListSubscribers(ctx context.Context, threadID string) ([]*router.Subscription, error) {
	var rows []model.RouterSubscription
	if err := s.db.WithContext(ctx).Where("thread_id = ?", threadID).Order("at").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return toDomainSubscriptions(rows), nil
}

func (s *routerStore) ListSubscriptions(ctx context.Context, ticketID string) ([]*router.Subscription, error) {
	var rows []model.RouterSubscription
	if err := s.db.WithContext(ctx).Where("ticket_id = ?", ticketID).Order("at").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return toDomainSubscriptions(rows), nil
}

func toDomainSubscriptions(rows []model.RouterSubscription) []*router.Subscription {
	out := make([]*router.Subscription, 0, len(rows))
	for i := range rows {
		row := rows[i]
		out = append(out, &router.Subscription{
			ThreadID: row.ThreadID,
			TicketID: row.TicketID,
			Own:      row.Own,
			Context:  row.Context,
			Closed:   row.Closed,
			At:       row.At.UTC(),
		})
	}
	return out
}

func (s *routerStore) CountSubscriptions(ctx context.Context) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.RouterSubscription{}).Count(&n).Error
	return int(n), err
}

func toDomainTicket(r *model.RouterTicket) *router.Ticket {
	t := &router.Ticket{
		ID:              r.ID,
		Title:           r.Title,
		Goal:            r.Goal,
		OwnerID:         r.OwnerID,
		State:           r.State,
		Resource:        r.Resource,
		TaskID:          r.TaskID,
		OriginTriggerID: r.OriginTriggerID,
		BatchID:         r.BatchID,
		Focus:           r.Focus,
		Attempts:        r.Attempts,
		NextTryAt:       r.NextTryAt.UTC(),
		LastError:       r.LastError,
		WaitsForPerson:  r.WaitsForPerson,
		CreatedAt:       r.CreatedAt.UTC(),
		UpdatedAt:       r.UpdatedAt.UTC(),
	}
	if r.EndedAt != nil {
		end := r.EndedAt.UTC()
		t.EndedAt = &end
	}
	return t
}

func (s *routerStore) SaveTicket(ctx context.Context, t *router.Ticket) error {
	row := &model.RouterTicket{
		ID:              t.ID,
		Title:           t.Title,
		Goal:            t.Goal,
		OwnerID:         t.OwnerID,
		State:           t.State,
		Resource:        t.Resource,
		TaskID:          t.TaskID,
		OriginTriggerID: t.OriginTriggerID,
		BatchID:         t.BatchID,
		Focus:           t.Focus,
		Attempts:        t.Attempts,
		NextTryAt:       t.NextTryAt.UTC(),
		LastError:       t.LastError,
		WaitsForPerson:  t.WaitsForPerson,
		CreatedAt:       t.CreatedAt.UTC(),
		UpdatedAt:       t.UpdatedAt.UTC(),
	}
	if t.EndedAt != nil {
		end := t.EndedAt.UTC()
		row.EndedAt = &end
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *routerStore) GetTicket(ctx context.Context, id string) (*router.Ticket, error) {
	var row model.RouterTicket
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainTicket(&row), nil
}

func (s *routerStore) ListTickets(ctx context.Context, q router.TicketQuery) ([]*router.Ticket, error) {
	q.Normalize()
	tx := s.db.WithContext(ctx).Model(&model.RouterTicket{})
	if q.State != "" {
		tx = tx.Where("state = ?", q.State)
	}
	if q.Open {
		tx = tx.Where("state NOT IN ?", []string{router.TicketDone, router.TicketFailed, router.TicketCanceled})
	}
	var rows []model.RouterTicket
	if err := tx.Order("created_at DESC").Offset(q.Offset).Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*router.Ticket, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainTicket(&rows[i]))
	}
	return out, nil
}

func (s *routerStore) CountTickets(ctx context.Context, state string) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.RouterTicket{}).Where("state = ?", state).Count(&n).Error
	return int(n), err
}

func (s *routerStore) SaveAssignment(ctx context.Context, a *router.Assignment) error {
	row := &model.RouterAssignment{
		ID:        a.ID,
		TicketID:  a.TicketID,
		State:     a.State,
		TaskID:    a.TaskID,
		Note:      a.Note,
		Error:     a.Error,
		TriggerID: a.TriggerID,
		StartedAt: a.StartedAt.UTC(),
	}
	if a.EndedAt != nil {
		end := a.EndedAt.UTC()
		row.EndedAt = &end
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *routerStore) ListAssignments(ctx context.Context, ticketID string, limit int) ([]*router.Assignment, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []model.RouterAssignment
	if err := s.db.WithContext(ctx).Where("ticket_id = ?", ticketID).
		Order("started_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*router.Assignment, 0, len(rows))
	for i := range rows {
		row := rows[i]
		item := &router.Assignment{
			ID: row.ID, TicketID: row.TicketID, State: row.State, TaskID: row.TaskID,
			Note: row.Note, Error: row.Error, TriggerID: row.TriggerID,
			StartedAt: row.StartedAt.UTC(),
		}
		if row.EndedAt != nil {
			end := row.EndedAt.UTC()
			item.EndedAt = &end
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *routerStore) AppendDecision(ctx context.Context, d *router.Decision) error {
	row := &model.RouterDecision{
		ID:        d.ID,
		TriggerID: d.TriggerID,
		BatchID:   d.BatchID,
		Kind:      d.Kind,
		TicketID:  d.TicketID,
		Resource:  d.Resource,
		By:        d.By,
		Reason:    d.Reason,
		At:        d.At.UTC(),
	}
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *routerStore) ListDecisions(ctx context.Context, q router.TriggerQuery) ([]*router.Decision, error) {
	q.Normalize()
	tx := s.db.WithContext(ctx).Model(&model.RouterDecision{})
	if q.TicketID != "" {
		tx = tx.Where("ticket_id = ?", q.TicketID)
	}
	var rows []model.RouterDecision
	if err := tx.Order("at DESC").Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*router.Decision, 0, len(rows))
	for i := range rows {
		row := rows[i]
		out = append(out, &router.Decision{
			ID: row.ID, TriggerID: row.TriggerID, BatchID: row.BatchID, Kind: row.Kind,
			TicketID: row.TicketID, Resource: row.Resource, By: row.By,
			Reason: row.Reason, At: row.At.UTC(),
		})
	}
	return out, nil
}

func (s *routerStore) GetPolicy(ctx context.Context, source string) (*router.Policy, error) {
	var row model.RouterPolicy
	err := s.db.WithContext(ctx).Where("source = ?", source).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainPolicy(&row), nil
}

func toDomainPolicy(r *model.RouterPolicy) *router.Policy {
	p := &router.Policy{
		Source:     r.Source,
		Mode:       r.Mode,
		QuietStart: r.QuietStart,
		QuietEnd:   r.QuietEnd,
		Resource:   r.Resource,
		UpdatedAt:  r.UpdatedAt.UTC(),
	}
	if len(r.Allow) > 0 {
		_ = r.Allow.Decode(&p.Allow)
	}
	if len(r.Keywords) > 0 {
		_ = r.Keywords.Decode(&p.Keywords)
	}
	return p
}

func (s *routerStore) ListPolicies(ctx context.Context) ([]*router.Policy, error) {
	var rows []model.RouterPolicy
	if err := s.db.WithContext(ctx).Order("source").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*router.Policy, 0, len(rows))
	for i := range rows {
		out = append(out, toDomainPolicy(&rows[i]))
	}
	return out, nil
}

func (s *routerStore) SavePolicy(ctx context.Context, p *router.Policy) error {
	row := &model.RouterPolicy{
		Source:     p.Source,
		Mode:       p.Mode,
		Allow:      jsonOrNil(p.Allow),
		Keywords:   jsonOrNil(p.Keywords),
		QuietStart: p.QuietStart,
		QuietEnd:   p.QuietEnd,
		Resource:   p.Resource,
		UpdatedAt:  p.UpdatedAt.UTC(),
	}
	return s.db.WithContext(ctx).Save(row).Error
}

func (s *routerStore) AppendEvent(ctx context.Context, e *router.Event) error {
	row := &model.RouterEvent{
		Kind:    e.Kind,
		Subject: e.Subject,
		At:      e.At.UTC(),
		Payload: jsonOrNil(e.Payload),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return err
	}
	// The caller publishes the frame it handed in, so the assigned sequence has
	// to land on it: the live stream and the replay must agree.
	e.Seq = row.Seq
	return nil
}

func (s *routerStore) EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*router.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	tx := s.db.WithContext(ctx).Model(&model.RouterEvent{}).Where("seq > ?", since)
	if subject != "" {
		tx = tx.Where("subject = ?", subject)
	}
	var rows []model.RouterEvent
	if err := tx.Order("seq").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*router.Event, 0, len(rows))
	for i := range rows {
		row := rows[i]
		ev := &router.Event{Seq: row.Seq, Kind: row.Kind, Subject: row.Subject, At: row.At.UTC()}
		if len(row.Payload) > 0 {
			var payload map[string]any
			if err := row.Payload.Decode(&payload); err == nil {
				ev.Payload = payload
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s *routerStore) MaxSeq(ctx context.Context) (uint64, error) {
	var seq uint64
	err := s.db.WithContext(ctx).Model(&model.RouterEvent{}).
		Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error
	return seq, err
}

func (s *routerStore) Prune(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 5000
	}
	total := 0
	statements := []struct {
		query string
		args  []any
	}{
		{
			`DELETE FROM router_triggers WHERE id IN (SELECT id FROM router_triggers WHERE updated_at < ? LIMIT ?)`,
			[]any{before.UTC(), limit},
		},
		{
			`DELETE FROM router_decisions WHERE id IN (SELECT id FROM router_decisions WHERE at < ? LIMIT ?)`,
			[]any{before.UTC(), limit},
		},
		{
			`DELETE FROM router_events WHERE seq IN (SELECT seq FROM router_events WHERE at < ? LIMIT ?)`,
			[]any{before.UTC(), limit},
		},
	}
	for _, stmt := range statements {
		res := s.db.WithContext(ctx).Exec(stmt.query, stmt.args...)
		if res.Error != nil {
			return total, res.Error
		}
		total += int(res.RowsAffected)
	}
	return total, nil
}
