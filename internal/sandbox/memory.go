package sandbox

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store: what the unit tests run on, and what an
// instance without a database uses. It must behave exactly like the GORM
// implementation; the shared test suite is what pins that down.
type MemoryStore struct {
	mu     sync.RWMutex
	leases map[string]*Lease
	execs  map[string][]*ExecRecord
	nodes  map[string]*Node
	jobs   map[string]*Job
	events map[string][]*Event
	seq    uint64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		leases: map[string]*Lease{},
		execs:  map[string][]*ExecRecord{},
		nodes:  map[string]*Node{},
		jobs:   map[string]*Job{},
		events: map[string][]*Event{},
	}
}

func cloneLease(l *Lease) *Lease {
	if l == nil {
		return nil
	}
	cp := *l
	cp.Profile = l.Profile
	cp.Profile.EgressAllow = cloneStrings(l.Profile.EgressAllow)
	cp.Profile.EnvAllow = cloneStrings(l.Profile.EnvAllow)
	cp.Profile.ArgsAllow = cloneStrings(l.Profile.ArgsAllow)
	return &cp
}

func (m *MemoryStore) SaveLease(_ context.Context, l *Lease) error {
	if l == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leases[l.ID] = cloneLease(l)
	return nil
}

func (m *MemoryStore) GetLease(_ context.Context, id string) (*Lease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneLease(m.leases[id]), nil
}

func (m *MemoryStore) FindLease(_ context.Context, ownerID, resourceID, scope, provider string) (*Lease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *Lease
	for _, l := range m.leases {
		if l.OwnerID != ownerID || l.ResourceID != resourceID || l.Scope != scope || l.Provider != provider {
			continue
		}
		if l.State != StateReady && l.State != StateStarting {
			continue
		}
		if best == nil || l.RenewedAt.After(best.RenewedAt) {
			best = l
		}
	}
	return cloneLease(best), nil
}

func (m *MemoryStore) ListLeases(_ context.Context, ownerID string) ([]*Lease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Lease, 0, len(m.leases))
	for _, l := range m.leases {
		if ownerID != "" && l.OwnerID != ownerID {
			continue
		}
		out = append(out, cloneLease(l))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) DeleteLease(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leases, id)
	return nil
}

func (m *MemoryStore) ExpiredLeases(_ context.Context, now time.Time, limit int) ([]*Lease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Lease, 0)
	for _, l := range m.leases {
		if l.State != StateReady || !l.Expired(now) {
			continue
		}
		out = append(out, cloneLease(l))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) CountLeases(_ context.Context, ownerID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, l := range m.leases {
		if l.State != StateReady && l.State != StateStarting {
			continue
		}
		if ownerID != "" && l.OwnerID != ownerID {
			continue
		}
		n++
	}
	return n, nil
}

func (m *MemoryStore) AppendExec(_ context.Context, r *ExecRecord) error {
	if r == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *r
	m.execs[r.LeaseID] = append(m.execs[r.LeaseID], &cp)
	return nil
}

func (m *MemoryStore) ListExecs(_ context.Context, leaseID string, limit int) ([]*ExecRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := m.execs[leaseID]
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	out := make([]*ExecRecord, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		cp := *rows[i]
		out = append(out, &cp)
	}
	return out, nil
}

func (m *MemoryStore) CountExecsSince(_ context.Context, since time.Time) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, rows := range m.execs {
		for _, r := range rows {
			if r.At.After(since) {
				n++
			}
		}
	}
	return n, nil
}

func (m *MemoryStore) SaveNode(_ context.Context, n *Node) error {
	if n == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *n
	m.nodes[n.ID] = &cp
	return nil
}

func (m *MemoryStore) GetNode(_ context.Context, id string) (*Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := m.nodes[id]
	if n == nil {
		return nil, nil
	}
	cp := *n
	return &cp, nil
}

func (m *MemoryStore) ListNodes(_ context.Context) ([]*Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		cp := *n
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) DeleteNode(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, id)
	return nil
}

func (m *MemoryStore) SaveJob(_ context.Context, j *Job) error {
	if j == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *j
	m.jobs[j.ID] = &cp
	return nil
}

func (m *MemoryStore) AppendEvent(_ context.Context, e *Event) error {
	if e == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	cp := *e
	cp.Seq = m.seq
	e.Seq = m.seq
	m.events[e.Subject] = append(m.events[e.Subject], &cp)
	// The in-memory stream keeps the last 512 frames per subject: replay is a
	// convenience for a reconnecting console, not a ledger. The database keeps
	// the full trail.
	if rows := m.events[e.Subject]; len(rows) > 512 {
		m.events[e.Subject] = rows[len(rows)-512:]
	}
	return nil
}

func (m *MemoryStore) EventsSince(_ context.Context, subject string, since uint64, limit int) ([]*Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Event
	for _, e := range m.events[subject] {
		if e.Seq <= since {
			continue
		}
		cp := *e
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *MemoryStore) MaxSeq(_ context.Context) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.seq, nil
}
