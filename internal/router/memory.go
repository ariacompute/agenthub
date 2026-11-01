package router

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-process Store. It is what the unit tests run on, and what
// an instance without a database would use: no dependency, same semantics as the
// GORM implementation.
//
// Every read returns a copy, so a caller mutating a returned trigger cannot reach
// into the store behind the engine's back — the single writer discipline starts
// with the port.
type MemoryStore struct {
	mu sync.RWMutex

	triggers map[string]*Trigger
	// triggerOrder keeps insertion order so the API can show "newest first"
	// without sorting by a timestamp that may collide.
	triggerOrder []string
	dedup        map[string]string

	threads map[string]*Thread
	// subs is threadID -> ticketID -> subscription: the many-to-many relation
	// answered from both directions by an index below.
	subs map[string]map[string]Subscription

	tickets     map[string]*Ticket
	assignments map[string][]*Assignment
	decisions   []*Decision

	policies map[string]*Policy
	events   []*Event
	seq      uint64
}

// NewMemoryStore returns an empty in-memory store preloaded with the default
// policy set, so an engine built on it has the same behaviour as a migrated
// database.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		triggers:    map[string]*Trigger{},
		dedup:       map[string]string{},
		threads:     map[string]*Thread{},
		subs:        map[string]map[string]Subscription{},
		tickets:     map[string]*Ticket{},
		assignments: map[string][]*Assignment{},
		policies:    map[string]*Policy{},
	}
	for _, p := range DefaultPolicies() {
		cp := *p
		s.policies[p.Source] = &cp
	}
	return s
}

// SeedPolicy installs a policy, used by tests and by the service when it applies
// the configured defaults.
func (s *MemoryStore) SeedPolicy(p *Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.policies[cp.Source] = &cp
}

func cloneTrigger(t *Trigger) *Trigger {
	if t == nil {
		return nil
	}
	cp := *t
	if t.Meta != nil {
		cp.Meta = make(map[string]any, len(t.Meta))
		for k, v := range t.Meta {
			cp.Meta[k] = v
		}
	}
	return &cp
}

func cloneTicket(t *Ticket) *Ticket {
	if t == nil {
		return nil
	}
	cp := *t
	if t.EndedAt != nil {
		end := *t.EndedAt
		cp.EndedAt = &end
	}
	return &cp
}

func (s *MemoryStore) AppendTrigger(_ context.Context, t *Trigger) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.DedupKey != "" {
		if _, ok := s.dedup[t.DedupKey]; ok {
			return false, nil
		}
		s.dedup[t.DedupKey] = t.ID
	}
	s.triggers[t.ID] = cloneTrigger(t)
	s.triggerOrder = append(s.triggerOrder, t.ID)
	return true, nil
}

func (s *MemoryStore) ClaimDueTriggers(_ context.Context, now time.Time, limit int) ([]*Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Trigger, 0, limit)
	for _, id := range s.triggerOrder {
		if len(out) >= limit {
			break
		}
		t, ok := s.triggers[id]
		if !ok || t.Disposition != string(DispositionPending) {
			continue
		}
		if t.DueAt.After(now) {
			continue
		}
		// Move the due time past the claim window: a second pass cannot take
		// the same row twice even if the first one is still deciding.
		t.DueAt = now.Add(time.Minute)
		out = append(out, cloneTrigger(t))
	}
	return out, nil
}

func (s *MemoryStore) SaveTrigger(_ context.Context, t *Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.triggers[t.ID]; !ok {
		s.triggerOrder = append(s.triggerOrder, t.ID)
	}
	s.triggers[t.ID] = cloneTrigger(t)
	return nil
}

func (s *MemoryStore) GetTrigger(_ context.Context, id string) (*Trigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.triggers[id]
	if !ok {
		return nil, nil
	}
	return cloneTrigger(t), nil
}

func (s *MemoryStore) ListTriggers(_ context.Context, q TriggerQuery) ([]*Trigger, error) {
	q.Normalize()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Trigger, 0, q.Limit)
	for i := len(s.triggerOrder) - 1; i >= 0 && len(out) < q.Limit; i-- {
		t, ok := s.triggers[s.triggerOrder[i]]
		if !ok {
			continue
		}
		if q.Source != "" && t.Source != q.Source {
			continue
		}
		if q.Disposition != "" && t.Disposition != q.Disposition {
			continue
		}
		if q.ThreadID != "" && t.ThreadID != q.ThreadID {
			continue
		}
		if q.TicketID != "" && t.TicketID != q.TicketID {
			continue
		}
		out = append(out, cloneTrigger(t))
	}
	return out, nil
}

func (s *MemoryStore) CountTriggers(_ context.Context, disposition string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, t := range s.triggers {
		if t.Disposition == disposition {
			n++
		}
	}
	return n, nil
}

func (s *MemoryStore) SaveThread(_ context.Context, th *Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *th
	s.threads[th.ID] = &cp
	return nil
}

func (s *MemoryStore) GetThread(_ context.Context, id string) (*Thread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	th, ok := s.threads[id]
	if !ok {
		return nil, nil
	}
	cp := *th
	return &cp, nil
}

func (s *MemoryStore) SaveSubscription(_ context.Context, sub *Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs[sub.ThreadID] == nil {
		s.subs[sub.ThreadID] = map[string]Subscription{}
	}
	s.subs[sub.ThreadID][sub.TicketID] = *sub
	return nil
}

func (s *MemoryStore) DropSubscription(_ context.Context, threadID, ticketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if byTicket, ok := s.subs[threadID]; ok {
		delete(byTicket, ticketID)
		if len(byTicket) == 0 {
			delete(s.subs, threadID)
		}
	}
	return nil
}

func (s *MemoryStore) ListSubscribers(_ context.Context, threadID string) ([]*Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Subscription, 0, len(s.subs[threadID]))
	for _, sub := range s.subs[threadID] {
		cp := sub
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func (s *MemoryStore) ListSubscriptions(_ context.Context, ticketID string) ([]*Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Subscription, 0, len(s.subs))
	for _, byTicket := range s.subs {
		if sub, ok := byTicket[ticketID]; ok {
			cp := sub
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func (s *MemoryStore) CountSubscriptions(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, byTicket := range s.subs {
		n += len(byTicket)
	}
	return n, nil
}

func (s *MemoryStore) SaveTicket(_ context.Context, t *Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.ID] = cloneTicket(t)
	return nil
}

func (s *MemoryStore) GetTicket(_ context.Context, id string) (*Ticket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tickets[id]
	if !ok {
		return nil, nil
	}
	return cloneTicket(t), nil
}

func (s *MemoryStore) ListTickets(_ context.Context, q TicketQuery) ([]*Ticket, error) {
	q.Normalize()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Ticket, 0, len(s.tickets))
	for _, t := range s.tickets {
		if q.State != "" && t.State != q.State {
			continue
		}
		if q.Open && !t.Open() {
			continue
		}
		out = append(out, cloneTicket(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if q.Offset >= len(out) {
		return []*Ticket{}, nil
	}
	out = out[q.Offset:]
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *MemoryStore) CountTickets(_ context.Context, state string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, t := range s.tickets {
		if t.State == state {
			n++
		}
	}
	return n, nil
}

func (s *MemoryStore) SaveAssignment(_ context.Context, a *Assignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	rows := s.assignments[a.TicketID]
	for i := range rows {
		if rows[i].ID == a.ID {
			rows[i] = &cp
			s.assignments[a.TicketID] = rows
			return nil
		}
	}
	s.assignments[a.TicketID] = append(rows, &cp)
	return nil
}

func (s *MemoryStore) ListAssignments(_ context.Context, ticketID string, limit int) ([]*Assignment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.assignments[ticketID]
	out := make([]*Assignment, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		cp := *rows[i]
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) AppendDecision(_ context.Context, d *Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *d
	s.decisions = append(s.decisions, &cp)
	return nil
}

func (s *MemoryStore) ListDecisions(_ context.Context, q TriggerQuery) ([]*Decision, error) {
	q.Normalize()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Decision, 0, q.Limit)
	for i := len(s.decisions) - 1; i >= 0 && len(out) < q.Limit; i-- {
		d := s.decisions[i]
		if q.TicketID != "" && d.TicketID != q.TicketID {
			continue
		}
		if q.ThreadID != "" {
			// Decisions have no thread of their own; the caller filters by
			// trigger instead. Keep the query honest rather than guessing.
			continue
		}
		out = append(out, &Decision{ID: d.ID, TriggerID: d.TriggerID, BatchID: d.BatchID, Kind: d.Kind, TicketID: d.TicketID, By: d.By, Reason: d.Reason, At: d.At})
	}
	return out, nil
}

func (s *MemoryStore) GetPolicy(_ context.Context, source string) (*Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.policies[source]
	if !ok {
		return nil, nil
	}
	cp := *p
	cp.Allow = append([]string(nil), p.Allow...)
	cp.Keywords = append([]string(nil), p.Keywords...)
	return &cp, nil
}

func (s *MemoryStore) ListPolicies(_ context.Context) ([]*Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Policy, 0, len(s.policies))
	for _, p := range s.policies {
		cp := *p
		cp.Allow = append([]string(nil), p.Allow...)
		cp.Keywords = append([]string(nil), p.Keywords...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

func (s *MemoryStore) SavePolicy(_ context.Context, p *Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.policies[p.Source] = &cp
	return nil
}

func (s *MemoryStore) AppendEvent(_ context.Context, e *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	// The caller keeps the frame it published, so the assigned sequence has to
	// land on it: the live stream and the replay must carry the same numbers.
	e.Seq = s.seq
	cp := *e
	s.events = append(s.events, &cp)
	return nil
}

func (s *MemoryStore) EventsSince(_ context.Context, subject string, since uint64, limit int) ([]*Event, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Event, 0, limit)
	for _, e := range s.events {
		if e.Seq <= since {
			continue
		}
		if subject != "" && e.Subject != subject {
			continue
		}
		cp := *e
		out = append(out, &cp)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) MaxSeq(_ context.Context) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seq, nil
}

func (s *MemoryStore) Prune(_ context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 5000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	kept := s.triggerOrder[:0]
	for _, id := range s.triggerOrder {
		t, ok := s.triggers[id]
		if !ok {
			continue
		}
		if removed < limit && t.UpdatedAt.Before(before) {
			delete(s.triggers, id)
			if t.DedupKey != "" {
				delete(s.dedup, t.DedupKey)
			}
			removed++
			continue
		}
		kept = append(kept, id)
	}
	s.triggerOrder = kept

	freshEvents := s.events[:0]
	for _, e := range s.events {
		if removed < limit && e.At.Before(before) {
			removed++
			continue
		}
		freshEvents = append(freshEvents, e)
	}
	s.events = freshEvents

	freshDecisions := s.decisions[:0]
	for _, d := range s.decisions {
		if removed < limit && d.At.Before(before) {
			removed++
			continue
		}
		freshDecisions = append(freshDecisions, d)
	}
	s.decisions = freshDecisions
	return removed, nil
}
