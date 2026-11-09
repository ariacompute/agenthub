package sandbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Manager is the single writer for leases, so its failure paths decide what
// happens when the database, the provider or the ceiling says no. These cases
// drive those paths directly instead of hoping a handler reaches them.

func testRegistry(t *testing.T, providers ...Provider) *Registry {
	t.Helper()
	registry := NewRegistry(RegistryOptions{Default: ProviderMock, Clock: time.Now})
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			t.Fatalf("register %s: %v", provider.Name(), err)
		}
	}
	return registry
}

func newManager(t *testing.T, registry *Registry, store Store, tune func(*ManagerOptions)) (*Manager, *clock) {
	t.Helper()
	tick := &clock{at: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	opts := ManagerOptions{
		Registry:    registry,
		Store:       store,
		Clock:       tick.now,
		Defaults:    Defaults{Provider: ProviderMock, Network: NetworkNone, TimeoutMS: 1000, MaxOutputBytes: 4096},
		IdleTTL:     10 * time.Minute,
		MaxTTL:      time.Hour,
		MaxPerOwner: 4, MaxTotal: 16,
	}
	if tune != nil {
		tune(&opts)
	}
	return NewManager(opts), tick
}

// faultyStore fails exactly one method, so a case can prove that the Manager
// stops rather than carrying on with half an answer.
type faultyStore struct {
	Store
	onSaveLease error
	onFindLease error
	onGetLease  error
	onCount     error
	onExpired   error
	onGetNode   error
}

func (f *faultyStore) SaveLease(ctx context.Context, l *Lease) error {
	if f.onSaveLease != nil {
		return f.onSaveLease
	}
	return f.Store.SaveLease(ctx, l)
}

func (f *faultyStore) FindLease(ctx context.Context, ownerID, resourceID, scope, provider string) (*Lease, error) {
	if f.onFindLease != nil {
		return nil, f.onFindLease
	}
	return f.Store.FindLease(ctx, ownerID, resourceID, scope, provider)
}

func (f *faultyStore) GetLease(ctx context.Context, id string) (*Lease, error) {
	if f.onGetLease != nil {
		return nil, f.onGetLease
	}
	return f.Store.GetLease(ctx, id)
}

func (f *faultyStore) CountLeases(ctx context.Context, ownerID string) (int, error) {
	if f.onCount != nil {
		return 0, f.onCount
	}
	return f.Store.CountLeases(ctx, ownerID)
}

func (f *faultyStore) ExpiredLeases(ctx context.Context, now time.Time, limit int) ([]*Lease, error) {
	if f.onExpired != nil {
		return nil, f.onExpired
	}
	return f.Store.ExpiredLeases(ctx, now, limit)
}

func (f *faultyStore) GetNode(ctx context.Context, id string) (*Node, error) {
	if f.onGetNode != nil {
		return nil, f.onGetNode
	}
	return f.Store.GetNode(ctx, id)
}

type createFailer struct{ providerFunc }

func (createFailer) Create(context.Context, Spec) (Handle, error) {
	return Handle{}, Error(CodeUnsupported)
}

type slowCreateProvider struct {
	providerFunc
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (s *slowCreateProvider) Create(_ context.Context, spec Spec) (Handle, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return Handle{ID: spec.LeaseID, Provider: s.name, Ref: "ref-" + spec.LeaseID, Spec: spec}, nil
}

func (s *slowCreateProvider) created() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type attachProvider struct{ providerFunc }

func (attachProvider) Attach(context.Context, Handle, ExecRequest) (Session, error) {
	return nopSession{}, nil
}

type nopSession struct{}

func (nopSession) RoundTrip(context.Context, []byte) ([]byte, error) { return []byte("ok"), nil }

func (nopSession) Close() error { return nil }

func TestNewManagerResolvesItsOwnDefaults(t *testing.T) {
	// Only the provider is named: the store, the clock, both TTLs and both
	// ceilings have to come from NewManager itself.
	manager := NewManager(ManagerOptions{
		Registry: testRegistry(t, NewMockProvider()),
		Defaults: Defaults{Provider: ProviderMock, Network: NetworkNone},
	})
	ctx := context.Background()
	tick := time.Now()

	lease, err := manager.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"})
	if err != nil {
		t.Fatalf("a manager built from zero options must still lease: %v", err)
	}
	// Defaults: 30 minutes idle, and a store that keeps nothing still leases
	// correctly inside one process.
	if idle := lease.ExpiresAt.Sub(lease.RenewedAt); idle != 30*time.Minute {
		t.Fatalf("idle ttl = %v, want 30m", idle)
	}
	if lease.CreatedAt.Before(tick.Add(-time.Minute)) {
		t.Fatalf("the clock must fall back to time.Now, got %v", lease.CreatedAt)
	}
	if profile := manager.Defaults(); profile.Provider != ProviderMock || profile.Image != DefaultImage {
		t.Fatalf("defaults = %+v", profile)
	}
	if _, err := manager.Get(ctx, "nope"); err == nil {
		t.Fatal("a database-free manager still reports a missing lease")
	} else if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("expected %s, got %s", CodeNotFound, code)
	}
}

func TestConcurrentAcquiresShareOneCreation(t *testing.T) {
	provider := &slowCreateProvider{
		providerFunc: providerFunc{name: ProviderMock, desc: Descriptor{Layer: LayerNone}},
		delay:        50 * time.Millisecond,
	}
	manager, _ := newManager(t, testRegistry(t, provider), NewMemoryStore(), nil)

	const callers = 6
	var wg sync.WaitGroup
	ids := make([]string, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, err := manager.Acquire(context.Background(), LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "ticket"})
			if err == nil {
				ids[i] = lease.ID
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("every caller must get the same environment: %v", ids)
		}
	}
	// A parallel flow step must not start six containers.
	if got := provider.created(); got != 1 {
		t.Fatalf("provider.Create called %d times, want 1", got)
	}
}

func TestAcquireStopsWhenTheStoreFails(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("database down")

	findFails, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: NewMemoryStore(), onFindLease: boom}, nil)
	if _, err := findFails.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"}); !errors.Is(err, boom) {
		t.Fatalf("find error must surface, got %v", err)
	}

	countFails, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: NewMemoryStore(), onCount: boom}, nil)
	if _, err := countFails.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"}); !errors.Is(err, boom) {
		t.Fatalf("ceiling check error must surface, got %v", err)
	}
	// An anonymous caller skips the per-owner count and hits the global one, which
	// must fail the same way.
	if _, err := countFails.Acquire(ctx, LeaseRequest{ResourceID: "a"}); !errors.Is(err, boom) {
		t.Fatalf("global ceiling check error must surface, got %v", err)
	}

	saveFails, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: NewMemoryStore(), onSaveLease: boom}, nil)
	if _, err := saveFails.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"}); !errors.Is(err, boom) {
		t.Fatalf("save error must surface, got %v", err)
	}

	// The reuse path reads the row back and rewrites it; a failing write there is
	// also a failure, not a silently renewed lease.
	store := NewMemoryStore()
	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-1", OwnerID: "u1", ResourceID: "a", Scope: "default", Provider: ProviderMock,
		State: StateReady, CreatedAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		RenewedAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	renewFails, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: store, onSaveLease: boom}, nil)
	if _, err := renewFails.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"}); !errors.Is(err, boom) {
		t.Fatalf("renewal write error must surface, got %v", err)
	}
}

func TestAFailedCreateRecordsTheLeaseAsFailed(t *testing.T) {
	store := NewMemoryStore()
	manager, _ := newManager(t, testRegistry(t, createFailer{providerFunc{name: ProviderMock, desc: Descriptor{Layer: LayerNone}}}), store, nil)

	_, err := manager.Acquire(context.Background(), LeaseRequest{OwnerID: "u1", ResourceID: "a"})
	if err == nil {
		t.Fatal("a provider that cannot create must fail the acquire")
	}
	leases, err := store.ListLeases(context.Background(), "u1")
	if err != nil || len(leases) != 1 {
		t.Fatalf("the failed attempt must be visible for the audit trail: %v (%v)", leases, err)
	}
	if leases[0].State != StateFailed {
		t.Fatalf("state = %q, want failed", leases[0].State)
	}
}

func TestExecOnReportsACeiling(t *testing.T) {
	manager, _ := newManager(t, testRegistry(t, NewMockProvider()), NewMemoryStore(), func(o *ManagerOptions) {
		o.MaxPerOwner = 1
	})
	ctx := context.Background()
	if _, _, err := manager.ExecOn(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"}, ExecRequest{Command: "echo"}); err != nil {
		t.Fatalf("first exec: %v", err)
	}
	_, _, err := manager.ExecOn(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "second"}, ExecRequest{Command: "echo"})
	if code := codeOf(t, err); code != CodeLimitReached {
		t.Fatalf("expected %s, got %s", CodeLimitReached, code)
	}
}

func TestExecRebuildsAHandleFromTheLeaseRow(t *testing.T) {
	store := NewMemoryStore()
	manager, tick := newManager(t, testRegistry(t, NewMockProvider()), store, nil)
	ctx := context.Background()

	// A lease that survived a restart: the row carries everything the provider
	// needs, so a cached handle is not required.
	now := tick.now()
	lease := &Lease{
		ID: "lease-restart", OwnerID: "u1", ResourceID: "a", Scope: "default", Provider: ProviderMock,
		Ref: "mock-ref", State: StateReady, CreatedAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour),
		Profile: Profile{Provider: ProviderMock, TimeoutMS: 1000, MaxOutputBytes: 4096},
	}
	if err := store.SaveLease(ctx, lease); err != nil {
		t.Fatalf("save lease: %v", err)
	}

	res, err := manager.Exec(ctx, lease.ID, ExecRequest{Command: "echo"})
	if err != nil {
		t.Fatalf("exec after restart: %v", err)
	}
	if res.Provider != ProviderMock || res.Ref != "mock-ref" {
		t.Fatalf("receipt = %+v", res)
	}
	// The rebuilt handle is cached, so the second call does not rebuild it.
	if _, err := manager.Exec(ctx, lease.ID, ExecRequest{Command: "echo"}); err != nil {
		t.Fatalf("second exec: %v", err)
	}
}

func TestAcquireRemembersAHandleItLearnedFromTheRow(t *testing.T) {
	store := NewMemoryStore()
	manager, tick := newManager(t, testRegistry(t, NewMockProvider()), store, nil)
	ctx := context.Background()
	now := tick.now()

	// A lease that outlived the process: the Manager has no cached handle, so the
	// reuse path has to rebuild one from the row.
	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-restart", OwnerID: "u1", ResourceID: "a", Scope: "default", Provider: ProviderMock,
		Ref: "mock-ref", State: StateReady, CreatedAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour),
		Profile: Profile{Provider: ProviderMock, TimeoutMS: 1000, MaxOutputBytes: 4096},
	}); err != nil {
		t.Fatalf("save lease: %v", err)
	}

	lease, err := manager.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.ID != "lease-restart" {
		t.Fatalf("the live lease must be reused, got %s", lease.ID)
	}
	if _, ok := manager.handleFor(lease); !ok {
		t.Fatal("the handle must be remembered after the reuse")
	}
}

func TestExecRefusesALeaseWhoseProviderIsGone(t *testing.T) {
	store := NewMemoryStore()
	manager, tick := newManager(t, testRegistry(t, NewMockProvider()), store, nil)
	ctx := context.Background()
	now := tick.now()
	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-ghost", OwnerID: "u1", Provider: "ghost", Ref: "ref", State: StateReady,
		CreatedAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save lease: %v", err)
	}
	if _, err := manager.Exec(ctx, "lease-ghost", ExecRequest{Command: "echo"}); err == nil {
		t.Fatal("an unregistered provider must be refused, not substituted")
	}
}

func TestAttachNeedsAHandleAndALiveProvider(t *testing.T) {
	store := NewMemoryStore()
	manager, tick := newManager(t, testRegistry(t, NewMockProvider(), attachProvider{providerFunc{name: "attachable", desc: Descriptor{Layer: LayerProcess}}}), store, nil)
	ctx := context.Background()
	now := tick.now()

	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-orphan", OwnerID: "u1", Provider: ProviderMock, State: StateReady,
		CreatedAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save lease: %v", err)
	}
	if _, err := manager.Attach(ctx, "lease-orphan", ExecRequest{Command: "mcp"}); err == nil {
		t.Fatal("a lease without a reference has no process to attach to")
	} else if code := codeOf(t, err); code != CodeLeaseLost {
		t.Fatalf("expected %s, got %s", CodeLeaseLost, code)
	}

	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-ghost", OwnerID: "u1", Provider: "ghost", Ref: "ref", State: StateReady,
		CreatedAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save lease: %v", err)
	}
	if _, err := manager.Attach(ctx, "lease-ghost", ExecRequest{Command: "mcp"}); err == nil {
		t.Fatal("an unregistered provider cannot hold a session")
	}

	// A provider that can attach returns a usable session through both entry
	// points, including the anonymous one that must then be released.
	lease, err := manager.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Provider: "attachable"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	session, err := manager.Attach(ctx, lease.ID, ExecRequest{Command: "mcp"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	_ = session.Close()

	session, anonymous, err := manager.AttachOn(ctx, LeaseRequest{ResourceID: "b", Provider: "attachable"}, ExecRequest{Command: "mcp"})
	if err != nil || session == nil || anonymous == nil {
		t.Fatalf("attach on = %v %+v (%v)", session, anonymous, err)
	}
	// Unlike ExecOn, a successful attach hands the lease back to the caller: the
	// session outlives the call, so releasing it here would kill the process the
	// caller is about to talk to.
	returned, err := manager.Get(ctx, anonymous.ID)
	if err != nil || returned.State != StateReady {
		t.Fatalf("an attached lease must stay live: %+v (%v)", returned, err)
	}
	if err := manager.Terminate(ctx, anonymous.ID); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	if _, lease, err := manager.AttachOn(ctx, LeaseRequest{ResourceID: "c", Provider: "ghost"}, ExecRequest{Command: "mcp"}); err == nil {
		t.Fatal("attach on an unknown provider must fail")
	} else if lease != nil {
		t.Fatalf("a failed acquire has no lease to hand back, got %+v", lease)
	}
}

func TestRenewAndTerminateReportStoreFailures(t *testing.T) {
	boom := errors.New("database down")
	ctx := context.Background()

	getFails, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: NewMemoryStore(), onGetLease: boom}, nil)
	if _, err := getFails.Renew(ctx, "lease-1"); !errors.Is(err, boom) {
		t.Fatalf("renew must surface the read error, got %v", err)
	}
	if err := getFails.Terminate(ctx, "lease-1"); !errors.Is(err, boom) {
		t.Fatalf("terminate must surface the read error, got %v", err)
	}

	store := NewMemoryStore()
	_, tick := newManager(t, testRegistry(t, NewMockProvider()), store, nil)
	now := tick.now()
	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-1", OwnerID: "u1", Provider: ProviderMock, Ref: "ref", State: StateReady,
		CreatedAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save lease: %v", err)
	}
	writeFails, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: store, onSaveLease: boom}, nil)
	if _, err := writeFails.Renew(ctx, "lease-1"); !errors.Is(err, boom) {
		t.Fatalf("renew must surface the write error, got %v", err)
	}
}

func TestSweepReportsAStoreFailure(t *testing.T) {
	boom := errors.New("database down")
	manager, _ := newManager(t, testRegistry(t, NewMockProvider()),
		&faultyStore{Store: NewMemoryStore(), onExpired: boom}, nil)
	if _, err := manager.Sweep(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("sweep must surface the read error, got %v", err)
	}
}

func TestFindHonoursTheProviderOverride(t *testing.T) {
	manager, _ := newManager(t, testRegistry(t, NewMockProvider()), NewMemoryStore(), nil)
	ctx := context.Background()
	req := LeaseRequest{OwnerID: "u1", ResourceID: "a"}
	if _, err := manager.Acquire(ctx, req); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	found, err := manager.Find(ctx, req)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	// The override changes the reuse key: the same (owner, resource, scope) under
	// a different provider is a different environment.
	other := req
	other.Provider = "bwrap"
	missing, err := manager.Find(ctx, other)
	if err != nil {
		t.Fatalf("find with an override: %v", err)
	}
	if found == nil || missing != nil {
		t.Fatalf("found = %+v, missing = %+v", found, missing)
	}
}

func TestTheIdleTTLFallsBackToTheProfile(t *testing.T) {
	manager, _ := newManager(t, testRegistry(t, NewMockProvider()), NewMemoryStore(), nil)
	lease, err := manager.Acquire(context.Background(), LeaseRequest{
		OwnerID: "u1", ResourceID: "a",
		Profile: Profile{IdleTTLSeconds: 60},
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if idle := lease.ExpiresAt.Sub(lease.RenewedAt); idle != time.Minute {
		t.Fatalf("idle ttl = %v, want the resource's 60s", idle)
	}
}

func TestNodeLookupsReportStoreFailures(t *testing.T) {
	boom := errors.New("database down")
	service, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.Store = &faultyStore{Store: NewMemoryStore(), onGetNode: boom}
	})
	ctx := context.Background()
	if _, err := service.Node(ctx, "node-1"); !errors.Is(err, boom) {
		t.Fatalf("node lookup must surface the read error, got %v", err)
	}
	if err := service.TouchNode(ctx, "node-1"); !errors.Is(err, boom) {
		t.Fatalf("heartbeat must surface the read error, got %v", err)
	}
}

func TestAServiceBuiltFromZeroOptionsStillHasABeat(t *testing.T) {
	// Both the clock and the node TTL have defaults; without them a facade built
	// by hand would panic on the first watermark instead of starting.
	service := NewService(ServiceOptions{})
	t.Cleanup(service.Stop)
	if service.sweeper == nil {
		t.Fatal("every facade owns a sweeper")
	}
	if service.nodeTTL <= 0 || service.clock == nil {
		t.Fatalf("defaults missing: ttl=%v clock=%v", service.nodeTTL, service.clock)
	}
}

func TestAFailedExecIsLoggedWithoutItsCommandLine(t *testing.T) {
	var logged bytes.Buffer
	manager, _ := newManager(t, testRegistry(t,
		execFailer{providerFunc: providerFunc{name: "lost", desc: Descriptor{Layer: LayerProcess}}, err: Error(CodeTimeout)},
	), NewMemoryStore(), func(o *ManagerOptions) {
		o.Log = slog.New(slog.NewTextHandler(&logged, nil))
		// The arguments have to reach the provider for it to fail: Exec checks
		// every one of them against the profile first.
		o.Defaults.ArgsPolicy = ArgsPassthrough
	})
	// A lease ID is "lease_" plus 24 hex digits. A short, hex-looking sentinel
	// such as "abc" can occur inside that random ID by chance, failing this case
	// on a log line that never carried the command line at all. Keep every
	// sentinel outside the hex alphabet so the ID cannot be mistaken for a leak.
	const command, flag, secret = "top-secret-command", "--token", "s3cr3t-value-not-hex"
	ctx := context.Background()
	lease, err := manager.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Provider: "lost"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := manager.Exec(ctx, lease.ID, ExecRequest{Command: command, Args: []string{flag, secret}}); err == nil {
		t.Fatal("the provider refused the command")
	}
	line := logged.String()
	if !strings.Contains(line, "sandbox exec failed") || !strings.Contains(line, lease.ID) {
		t.Fatalf("the log line must identify the lease: %q", line)
	}
	for _, leaked := range []string{command, flag, secret} {
		if strings.Contains(line, leaked) {
			t.Fatalf("the log line must never carry the command line (%q leaked): %q", leaked, line)
		}
	}
}

// Arguments are checked one at a time, inside Exec, against the profile the
// lease was created with.
//
// The adapter path already asked ArgsAllowed before it built a lease, but Exec
// is the funnel every caller shares — POST /api/sandboxes/{id}/exec included —
// and it used to hand caller supplied argv straight to the provider. "The
// command is on the allowlist, the arguments are not" is how an allowed binary is
// talked into running something else.
func TestExecRefusesArgumentsThePolicyForbids(t *testing.T) {
	manager, _ := newManager(t, testRegistry(t, NewMockProvider()), NewMemoryStore(), func(o *ManagerOptions) {
		o.Defaults.ArgsPolicy = ArgsNone
	})
	ctx := context.Background()
	lease, err := manager.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Provider: ProviderMock})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// A bare command is what "no arguments from the caller" leaves room for.
	if _, err := manager.Exec(ctx, lease.ID, ExecRequest{Command: "ls"}); err != nil {
		t.Fatalf("a bare command must still run: %v", err)
	}

	// An argument that would rewrite the command is refused before any provider
	// sees it, and the refusal is not recorded as an execution receipt.
	if _, err := manager.Exec(ctx, lease.ID, ExecRequest{Command: "ls", Args: []string{"-c", "rm -rf /"}}); err == nil {
		t.Fatal("an argument must not slip past a none policy")
	} else if code := codeOf(t, err); code != CodePolicyDenied {
		t.Fatalf("code = %q, want %q", code, CodePolicyDenied)
	}
	execs, err := manager.Execs(ctx, lease.ID, 50)
	if err != nil {
		t.Fatalf("read receipts: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("receipts = %d, want only the bare run: a refused command never ran", len(execs))
	}
}
