package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"agenthub/internal/httputil"
)

// ManagerOptions configures lease handling.
type ManagerOptions struct {
	Registry    *Registry
	Store       Store
	Defaults    Defaults
	IdleTTL     time.Duration
	MaxTTL      time.Duration
	MaxPerOwner int
	MaxTotal    int
	Bus         *Bus
	Clock       Clock
	Log         *slog.Logger
}

// Manager is the only component that knows where execution happens. It turns a
// (owner, resource, scope) key into a lease, and a lease into an ExecResult.
//
// It is the single writer for leases, which is what lets the Store port stay
// simple: no conditional updates, no optimistic locking, one goroutine (well,
// one mutex) deciding.
type Manager struct {
	opts ManagerOptions

	mu      sync.Mutex
	flights map[string]*flight
	handles map[string]Handle
}

type flight struct {
	done  chan struct{}
	lease *Lease
	err   error
}

// NewManager builds a manager. A nil store falls back to NopStore, so an
// instance without a database still leases correctly inside one process.
func NewManager(opts ManagerOptions) *Manager {
	if opts.Store == nil {
		opts.Store = NopStore()
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 30 * time.Minute
	}
	if opts.MaxTTL <= 0 {
		opts.MaxTTL = 2 * time.Hour
	}
	if opts.MaxPerOwner <= 0 {
		opts.MaxPerOwner = 4
	}
	if opts.MaxTotal <= 0 {
		opts.MaxTotal = 16
	}
	return &Manager{
		opts:    opts,
		flights: map[string]*flight{},
		handles: map[string]Handle{},
	}
}

// Defaults exposes the resolved instance defaults, for the console's summary.
func (m *Manager) Defaults() Profile { return m.opts.Defaults.Profile() }

// Acquire returns the lease for a key, creating it when there is none. Concurrent
// callers for the same key share one creation (single flight): a parallel flow
// step must not start four containers.
func (m *Manager) Acquire(ctx context.Context, req LeaseRequest) (*Lease, error) {
	profile := Resolve(m.opts.Defaults.Profile(), req.Profile)
	if req.Provider != "" {
		profile.Provider = req.Provider
	}
	if err := ValidateProfile(profile); err != nil {
		return nil, err
	}
	scope := NormalizeScope(req.Scope)
	if strings.TrimSpace(req.OwnerID) == "" {
		// Anonymous callers never reuse: the evaluation report's "unnamed
		// resource is anonymously reachable" must not grow into "anonymous
		// long-lived container".
		return m.create(ctx, req.OwnerID, req.ResourceID, scope, profile)
	}

	key := strings.Join([]string{req.OwnerID, req.ResourceID, scope, profile.Provider}, "\x00")
	m.mu.Lock()
	if f, ok := m.flights[key]; ok {
		m.mu.Unlock()
		<-f.done
		return f.lease, f.err
	}
	f := &flight{done: make(chan struct{})}
	m.flights[key] = f
	m.mu.Unlock()

	lease, err := m.acquireLocked(ctx, req.OwnerID, req.ResourceID, scope, profile)
	f.lease, f.err = lease, err
	close(f.done)
	m.mu.Lock()
	delete(m.flights, key)
	m.mu.Unlock()
	return lease, err
}

func (m *Manager) acquireLocked(ctx context.Context, ownerID, resourceID, scope string, profile Profile) (*Lease, error) {
	existing, err := m.opts.Store.FindLease(ctx, ownerID, resourceID, scope, profile.Provider)
	if err != nil {
		return nil, err
	}
	now := m.opts.Clock()
	if existing != nil && existing.State == StateReady && !existing.Expired(now) {
		existing.RenewedAt = now
		existing.ExpiresAt = m.nextExpiry(existing, now)
		if err := m.opts.Store.SaveLease(ctx, existing); err != nil {
			return nil, err
		}
		m.rememberHandle(existing)
		if m.opts.Bus != nil {
			m.opts.Bus.Publish(Event{Kind: EventLeaseRenewed, Subject: subjectFor(ownerID), LeaseID: existing.ID,
				Payload: map[string]any{"provider": existing.Provider, "reused": true}})
		}
		return existing, nil
	}
	return m.create(ctx, ownerID, resourceID, scope, profile)
}

// create allocates a new environment and records it.
func (m *Manager) create(ctx context.Context, ownerID, resourceID, scope string, profile Profile) (*Lease, error) {
	if err := m.checkLimits(ctx, ownerID); err != nil {
		return nil, err
	}
	provider, info, err := m.opts.Registry.RequireAvailable(ctx, profile.Provider)
	if err != nil {
		return nil, err
	}
	now := m.opts.Clock()
	lease := &Lease{
		ID:         newLeaseID(),
		OwnerID:    ownerID,
		ResourceID: resourceID,
		Scope:      scope,
		Provider:   info.Name,
		Runtime:    profile.Runtime,
		State:      StateStarting,
		Profile:    profile,
		CreatedAt:  now,
		RenewedAt:  now,
		ExpiresAt:  now.Add(m.idleTTL(profile)),
	}
	spec := Spec{LeaseID: lease.ID, OwnerID: ownerID, ResourceID: resourceID, Scope: scope, Profile: profile}
	handle, err := provider.Create(ctx, spec)
	if err != nil {
		lease.State = StateFailed
		_ = m.opts.Store.SaveLease(ctx, lease)
		return nil, err
	}
	lease.State = StateReady
	lease.Ref = handle.Ref
	m.mu.Lock()
	m.handles[lease.ID] = handle
	m.mu.Unlock()
	if err := m.opts.Store.SaveLease(ctx, lease); err != nil {
		return nil, err
	}
	if m.opts.Bus != nil {
		m.opts.Bus.Publish(Event{Kind: EventLeaseCreated, Subject: subjectFor(ownerID), LeaseID: lease.ID,
			Payload: map[string]any{"provider": lease.Provider, "layer": info.Layer, "isolation": info.Isolation, "resourceId": resourceID}})
	}
	return lease, nil
}

func (m *Manager) checkLimits(ctx context.Context, ownerID string) error {
	if ownerID != "" {
		perOwner, err := m.opts.Store.CountLeases(ctx, ownerID)
		if err != nil {
			return err
		}
		if perOwner >= m.opts.MaxPerOwner {
			return Error(CodeLimitReached)
		}
	}
	total, err := m.opts.Store.CountLeases(ctx, "")
	if err != nil {
		return err
	}
	if total >= m.opts.MaxTotal {
		return Error(CodeLimitReached)
	}
	return nil
}

// Exec runs one command on an existing lease and writes the audit record.
func (m *Manager) Exec(ctx context.Context, leaseID string, req ExecRequest) (ExecResult, error) {
	lease, err := m.Get(ctx, leaseID)
	if err != nil {
		return ExecResult{}, err
	}
	handle, ok := m.handleFor(lease)
	if !ok {
		return ExecResult{}, Error(CodeLeaseLost)
	}
	provider, _, err := m.opts.Registry.RequireAvailable(ctx, lease.Provider)
	if err != nil {
		return ExecResult{}, err
	}
	if req.TimeoutMS <= 0 {
		req.TimeoutMS = lease.Profile.TimeoutMS
	}
	if req.MaxOutputBytes <= 0 {
		req.MaxOutputBytes = lease.Profile.OutputCap()
	}

	res, execErr := provider.Exec(ctx, handle, req)
	now := m.opts.Clock()

	record := &ExecRecord{
		ID:         newLeaseID(),
		LeaseID:    lease.ID,
		OwnerID:    lease.OwnerID,
		Provider:   lease.Provider,
		Command:    scrubCommand(req.Command, req.Args),
		ExitCode:   res.ExitCode,
		DurationMS: res.DurationMS,
		Truncated:  res.Truncated,
		DryRun:     req.DryRun,
		At:         now,
	}
	record.StdoutSHA256 = digest(res.Stdout)
	record.StderrSHA256 = digest(res.Stderr)
	_ = m.opts.Store.AppendExec(ctx, record)

	lease.Execs++
	lease.LastExit = res.ExitCode
	lease.LastCommand = record.Command
	lease.RenewedAt = now
	lease.ExpiresAt = m.nextExpiry(lease, now)
	if execErr != nil {
		var coded *httputil.Error
		if errors.As(execErr, &coded) {
			switch coded.Code {
			case CodeTimeout:
				// A timeout is a result, not a dead lease: the environment is
				// still usable and the next call should find it ready.
				lease.State = StateReady
			case CodeUnsupported, CodeLeaseLost:
				// The environment is gone (container reaped, binary uninstalled).
				// Mark the lease failed so the next Acquire transparently makes a
				// new one; the task does not fail because of it.
				lease.State = StateFailed
			}
		}
	}
	_ = m.opts.Store.SaveLease(ctx, lease)
	if m.opts.Bus != nil {
		m.opts.Bus.Publish(Event{Kind: EventExec, Subject: subjectFor(lease.OwnerID), LeaseID: lease.ID,
			Payload: map[string]any{"provider": lease.Provider, "exitCode": res.ExitCode, "durationMs": res.DurationMS,
				"truncated": res.Truncated, "dryRun": req.DryRun}})
	}
	if execErr != nil && m.opts.Log != nil {
		// Log identifiers only: never the command line, never the output.
		m.opts.Log.Warn("sandbox exec failed", "lease", lease.ID, "provider", lease.Provider, "err", execErr.Error())
	}
	return res, execErr
}

// ExecOn is the convenience the adapters use: acquire (or reuse) a lease for a
// key, run one command, and — for an anonymous caller — release it immediately.
func (m *Manager) ExecOn(ctx context.Context, leaseReq LeaseRequest, req ExecRequest) (ExecResult, *Lease, error) {
	lease, err := m.Acquire(ctx, leaseReq)
	if err != nil {
		return ExecResult{}, nil, err
	}
	res, execErr := m.Exec(ctx, lease.ID, req)
	if strings.TrimSpace(leaseReq.OwnerID) == "" {
		// Nobody owns it, so nobody can reuse it: hand the environment back now.
		_ = m.Terminate(ctx, lease.ID)
	}
	return res, lease, execErr
}

// Attach opens an interactive session on a lease.
func (m *Manager) Attach(ctx context.Context, leaseID string, req ExecRequest) (Session, error) {
	lease, err := m.Get(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	handle, ok := m.handleFor(lease)
	if !ok {
		return nil, Error(CodeLeaseLost)
	}
	provider, _, err := m.opts.Registry.RequireAvailable(ctx, lease.Provider)
	if err != nil {
		return nil, err
	}
	return provider.Attach(ctx, handle, req)
}

// AttachOn leases an environment and opens a session inside it. It exists
// because the MCP stdio transport needs the lease id before it can attach, and
// making the caller do two round trips through the Manager would be two chances
// to leak a lease on the error path.
func (m *Manager) AttachOn(ctx context.Context, leaseReq LeaseRequest, req ExecRequest) (Session, *Lease, error) {
	lease, err := m.Acquire(ctx, leaseReq)
	if err != nil {
		return nil, nil, err
	}
	session, err := m.Attach(ctx, lease.ID, req)
	if err != nil {
		if strings.TrimSpace(leaseReq.OwnerID) == "" {
			_ = m.Terminate(ctx, lease.ID)
		}
		return nil, lease, err
	}
	return session, lease, nil
}

// Renew extends a lease, capped by MaxTTL. A lease that reached its ceiling is
// refused rather than silently kept alive forever.
func (m *Manager) Renew(ctx context.Context, leaseID string) (*Lease, error) {
	lease, err := m.Get(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	now := m.opts.Clock()
	if now.Sub(lease.CreatedAt) >= m.opts.MaxTTL {
		return nil, Error(CodeLeaseLost)
	}
	lease.RenewedAt = now
	lease.ExpiresAt = m.nextExpiry(lease, now)
	if lease.State == StateExpired || lease.State == StateTerminated {
		return nil, Error(CodeLeaseLost)
	}
	lease.State = StateReady
	if err := m.opts.Store.SaveLease(ctx, lease); err != nil {
		return nil, err
	}
	if m.opts.Bus != nil {
		m.opts.Bus.Publish(Event{Kind: EventLeaseRenewed, Subject: subjectFor(lease.OwnerID), LeaseID: lease.ID,
			Payload: map[string]any{"expiresAt": lease.ExpiresAt}})
	}
	return lease, nil
}

// Terminate destroys the environment and closes the lease.
func (m *Manager) Terminate(ctx context.Context, leaseID string) error {
	lease, err := m.Get(ctx, leaseID)
	if err != nil {
		return err
	}
	handle, ok := m.handleFor(lease)
	if ok {
		if provider, rerr := m.opts.Registry.Resolve(lease.Provider); rerr == nil {
			_ = provider.Destroy(ctx, handle)
		}
	}
	lease.State = StateTerminated
	lease.ExpiresAt = m.opts.Clock()
	_ = m.opts.Store.SaveLease(ctx, lease)
	m.mu.Lock()
	delete(m.handles, lease.ID)
	m.mu.Unlock()
	if m.opts.Bus != nil {
		m.opts.Bus.Publish(Event{Kind: EventLeaseDestroyed, Subject: subjectFor(lease.OwnerID), LeaseID: lease.ID,
			Payload: map[string]any{"provider": lease.Provider}})
	}
	return nil
}

// Get returns one lease, from the store.
func (m *Manager) Get(ctx context.Context, leaseID string) (*Lease, error) {
	lease, err := m.opts.Store.GetLease(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, Error(CodeNotFound)
	}
	return lease, nil
}

// Find returns the live lease matching a key, or nil.
func (m *Manager) Find(ctx context.Context, req LeaseRequest) (*Lease, error) {
	profile := Resolve(m.opts.Defaults.Profile(), req.Profile)
	if req.Provider != "" {
		profile.Provider = req.Provider
	}
	return m.opts.Store.FindLease(ctx, req.OwnerID, req.ResourceID, NormalizeScope(req.Scope), profile.Provider)
}

// List returns the leases visible to an owner ("" means everything).
func (m *Manager) List(ctx context.Context, ownerID string) ([]*Lease, error) {
	return m.opts.Store.ListLeases(ctx, ownerID)
}

// Execs returns the audit trail of one lease.
func (m *Manager) Execs(ctx context.Context, leaseID string, limit int) ([]*ExecRecord, error) {
	return m.opts.Store.ListExecs(ctx, leaseID, limit)
}

// Sweep reclaims expired leases. It is called by the sweeper's beat, never from
// a request path.
func (m *Manager) Sweep(ctx context.Context) (int, error) {
	now := m.opts.Clock()
	expired, err := m.opts.Store.ExpiredLeases(ctx, now, 64)
	if err != nil {
		return 0, err
	}
	reclaimed := 0
	for _, lease := range expired {
		handle, ok := m.handleFor(lease)
		if ok {
			if provider, rerr := m.opts.Registry.Resolve(lease.Provider); rerr == nil {
				_ = provider.Destroy(ctx, handle)
			}
		}
		lease.State = StateExpired
		_ = m.opts.Store.SaveLease(ctx, lease)
		m.mu.Lock()
		delete(m.handles, lease.ID)
		m.mu.Unlock()
		reclaimed++
		if m.opts.Bus != nil {
			m.opts.Bus.Publish(Event{Kind: EventLeaseExpired, Subject: subjectFor(lease.OwnerID), LeaseID: lease.ID,
				Payload: map[string]any{"provider": lease.Provider, "idleTtlSeconds": lease.Profile.IdleTTLSeconds}})
		}
	}
	return reclaimed, nil
}

// Counts reports the counters the console's water level needs.
func (m *Manager) Counts(ctx context.Context, since time.Time) (active int, recent int, err error) {
	active, err = m.opts.Store.CountLeases(ctx, "")
	if err != nil {
		return 0, 0, err
	}
	recent, err = m.opts.Store.CountExecsSince(ctx, since)
	return active, recent, err
}

func (m *Manager) idleTTL(p Profile) time.Duration {
	if p.IdleTTLSeconds > 0 {
		return time.Duration(p.IdleTTLSeconds) * time.Second
	}
	return m.opts.IdleTTL
}

func (m *Manager) nextExpiry(lease *Lease, now time.Time) time.Time {
	next := now.Add(m.idleTTL(lease.Profile))
	if ceiling := lease.CreatedAt.Add(m.opts.MaxTTL); next.After(ceiling) {
		return ceiling
	}
	return next
}

func (m *Manager) rememberHandle(lease *Lease) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.handles[lease.ID]; !ok {
		m.handles[lease.ID] = Handle{
			ID:       lease.ID,
			Provider: lease.Provider,
			Ref:      lease.Ref,
			Spec: Spec{LeaseID: lease.ID, OwnerID: lease.OwnerID, ResourceID: lease.ResourceID,
				Scope: lease.Scope, Profile: lease.Profile},
		}
	}
}

// handleFor rebuilds a handle after a restart: the lease row carries everything
// the provider needs, which is why Handle.Spec exists.
func (m *Manager) handleFor(lease *Lease) (Handle, bool) {
	m.mu.Lock()
	handle, ok := m.handles[lease.ID]
	m.mu.Unlock()
	if ok {
		return handle, true
	}
	if lease.Ref == "" || lease.State == StateTerminated || lease.State == StateExpired {
		return Handle{}, false
	}
	handle = Handle{
		ID:       lease.ID,
		Provider: lease.Provider,
		Ref:      lease.Ref,
		Spec: Spec{LeaseID: lease.ID, OwnerID: lease.OwnerID, ResourceID: lease.ResourceID,
			Scope: lease.Scope, Profile: lease.Profile},
	}
	m.mu.Lock()
	m.handles[lease.ID] = handle
	m.mu.Unlock()
	return handle, true
}

func subjectFor(ownerID string) string {
	if strings.TrimSpace(ownerID) == "" {
		return "anonymous"
	}
	return "owner:" + ownerID
}

func digest(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newLeaseID() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return "lease_" + hex.EncodeToString(raw[:])
}
