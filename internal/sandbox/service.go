package sandbox

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ServiceOptions configures the facade.
type ServiceOptions struct {
	Manager    *Manager
	Registry   *Registry
	Bus        *Bus
	Queue      *JobQueue
	Interval   time.Duration
	NodeOnline time.Duration
	Clock      Clock
	Log        *slog.Logger
}

// Service is the facade the HTTP layer and the adapters talk to. It owns the
// sweeper goroutine, the event bus and the counters, exactly like router.Service:
// a handler never starts a goroutine of its own, and a test never has to stop one.
type Service struct {
	manager  *Manager
	registry *Registry
	bus      *Bus
	queue    *JobQueue
	sweeper  *Sweeper
	clock    Clock
	nodeTTL  time.Duration
	log      *slog.Logger

	statsMu sync.Mutex
	stats   Stats
	live    bool
}

// NewService builds the facade.
func NewService(opts ServiceOptions) *Service {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.NodeOnline <= 0 {
		opts.NodeOnline = 90 * time.Second
	}
	s := &Service{
		manager:  opts.Manager,
		registry: opts.Registry,
		bus:      opts.Bus,
		queue:    opts.Queue,
		clock:    opts.Clock,
		nodeTTL:  opts.NodeOnline,
		log:      opts.Log,
	}
	s.sweeper = NewSweeper(SweeperOptions{
		Interval: opts.Interval,
		Clock:    opts.Clock,
		Log:      opts.Log,
		Sweep:    s.sweepOnce,
	})
	return s
}

// Run starts the beat.
func (s *Service) Run(ctx context.Context) error {
	s.statsMu.Lock()
	s.live = true
	s.statsMu.Unlock()
	defer func() {
		s.statsMu.Lock()
		s.live = false
		s.statsMu.Unlock()
	}()
	return s.sweeper.Run(ctx)
}

// Stop ends the beat and releases subscribers. Order matters: stop deciding
// first, then stop talking to anyone.
func (s *Service) Stop() {
	s.sweeper.Stop()
	if s.bus != nil {
		s.bus.Close()
	}
}

// Kick asks for one extra sweep (after a manual exec, or from the console).
func (s *Service) Kick() { s.sweeper.Kick() }

// Live reports whether the beat is running, which the console uses to tell
// "sandbox is on" from "sandbox is off".
func (s *Service) Live() bool {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.live
}

// Manager exposes the lease manager to the adapters.
func (s *Service) Manager() *Manager { return s.manager }

// Registry exposes the catalogue.
func (s *Service) Registry() *Registry { return s.registry }

// Queue exposes the node queue to the node handlers.
func (s *Service) Queue() *JobQueue { return s.queue }

// Exec runs one command on a lease.
func (s *Service) Exec(ctx context.Context, leaseID string, req ExecRequest) (ExecResult, error) {
	return s.manager.Exec(ctx, leaseID, req)
}

// ExecOn acquires and runs in one step, which is what the adapters need.
func (s *Service) ExecOn(ctx context.Context, leaseReq LeaseRequest, req ExecRequest) (ExecResult, *Lease, error) {
	return s.manager.ExecOn(ctx, leaseReq, req)
}

// Attach opens an interactive session.
func (s *Service) Attach(ctx context.Context, leaseID string, req ExecRequest) (Session, error) {
	return s.manager.Attach(ctx, leaseID, req)
}

// AttachOn leases an environment and opens a session inside it, which is what the
// MCP stdio transport needs.
func (s *Service) AttachOn(ctx context.Context, leaseReq LeaseRequest, req ExecRequest) (Session, *Lease, error) {
	return s.manager.AttachOn(ctx, leaseReq, req)
}

// Acquire leases an environment (used by the computers surface).
func (s *Service) Acquire(ctx context.Context, req LeaseRequest) (*Lease, error) {
	return s.manager.Acquire(ctx, req)
}

// Renew extends a lease.
func (s *Service) Renew(ctx context.Context, id string) (*Lease, error) {
	return s.manager.Renew(ctx, id)
}

// Terminate destroys a lease.
func (s *Service) Terminate(ctx context.Context, id string) error {
	return s.manager.Terminate(ctx, id)
}

// Get returns one lease.
func (s *Service) Get(ctx context.Context, id string) (*Lease, error) {
	return s.manager.Get(ctx, id)
}

// List returns the leases visible to an owner.
func (s *Service) List(ctx context.Context, ownerID string) ([]*Lease, error) {
	return s.manager.List(ctx, ownerID)
}

// Execs returns the audit trail of one lease.
func (s *Service) Execs(ctx context.Context, id string, limit int) ([]*ExecRecord, error) {
	return s.manager.Execs(ctx, id, limit)
}

// Catalog returns the provider catalogue.
func (s *Service) Catalog(ctx context.Context) []ProviderInfo {
	return s.registry.Catalog(ctx)
}

// RefreshCatalog clears the probe cache, for the console's refresh action.
func (s *Service) RefreshCatalog(ctx context.Context) []ProviderInfo {
	return s.registry.ProbeAll(ctx)
}

// Stats is the five-counter water level plus the last sweep's outcome.
func (s *Service) Stats(ctx context.Context) Stats {
	active, recent, err := s.manager.Counts(ctx, s.clock().Add(-time.Hour))
	passes, reclaimed, lastAt, lastErr := s.sweeper.Counters()
	out := Stats{
		ActiveLeases: active,
		RecentExecs:  recent,
		LastSweepAt:  lastAt,
		Sweeps:       passes,
		Reclaimed:    reclaimed,
		LastError:    lastErr,
	}
	if err != nil {
		out.LastError = err.Error()
	}
	if s.queue != nil {
		out.PendingJobs = s.queue.Pending()
	}
	out.OnlineNodes = s.onlineNodes(ctx)
	return out
}

// Nodes lists registered nodes with a derived status: a stale heartbeat is
// offline, because a crashed desktop must not keep looking available.
func (s *Service) Nodes(ctx context.Context) ([]*Node, error) {
	nodes, err := s.manager.opts.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	for _, n := range nodes {
		if now.Sub(n.LastSeenAt) > s.nodeTTL {
			n.Status = NodeOffline
		} else {
			n.Status = NodeOnline
		}
	}
	return nodes, nil
}

// RegisterNode records a new execution node.
func (s *Service) RegisterNode(ctx context.Context, node *Node) error {
	return s.manager.opts.Store.SaveNode(ctx, node)
}

// Node returns one registered node, or CodeNotFound. The node endpoints use it
// for the ownership check, which is why it is an error and not a nil result: an
// unknown node and somebody else's node must look the same to the caller.
func (s *Service) Node(ctx context.Context, id string) (*Node, error) {
	node, err := s.manager.opts.Store.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, Error(CodeNotFound)
	}
	return node, nil
}

// TouchNode records a heartbeat.
func (s *Service) TouchNode(ctx context.Context, id string) error {
	node, err := s.manager.opts.Store.GetNode(ctx, id)
	if err != nil {
		return err
	}
	if node == nil {
		return Error(CodeNotFound)
	}
	node.LastSeenAt = s.clock()
	node.Status = NodeOnline
	return s.manager.opts.Store.SaveNode(ctx, node)
}

// RemoveNode unregisters a node.
func (s *Service) RemoveNode(ctx context.Context, id string) error {
	return s.manager.opts.Store.DeleteNode(ctx, id)
}

// SaveJob writes a finished job for the audit trail.
func (s *Service) SaveJob(ctx context.Context, job *Job) error {
	return s.manager.opts.Store.SaveJob(ctx, job)
}

// Subscribe opens the live stream.
func (s *Service) Subscribe(ownerID string) (<-chan Event, func()) {
	if s.bus == nil {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	return s.bus.Subscribe(subjectFor(ownerID), 64)
}

// EventsSince replays frames after since and reports the current maximum, which
// lets a console know whether it has caught up.
func (s *Service) EventsSince(ctx context.Context, ownerID string, since uint64, limit int) ([]*Event, uint64, error) {
	if limit <= 0 {
		limit = 200
	}
	events, err := s.manager.opts.Store.EventsSince(ctx, subjectFor(ownerID), since, limit)
	if err != nil {
		return nil, 0, err
	}
	max, err := s.manager.opts.Store.MaxSeq(ctx)
	if err != nil {
		return events, 0, err
	}
	return events, max, nil
}

func (s *Service) sweepOnce(ctx context.Context) (int, error) {
	return s.manager.Sweep(ctx)
}

func (s *Service) onlineNodes(ctx context.Context) int {
	nodes, err := s.manager.opts.Store.ListNodes(ctx)
	if err != nil {
		return 0
	}
	now := s.clock()
	count := 0
	for _, n := range nodes {
		if now.Sub(n.LastSeenAt) <= s.nodeTTL {
			count++
		}
	}
	return count
}
