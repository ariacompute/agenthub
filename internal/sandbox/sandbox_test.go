package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

// The kernel's own tests: leases, the catalogue and the queue, driven through the
// same memory store the handlers use without a database. Every case here is about
// a boundary — reuse versus a new environment, a ceiling, an expired lease — which
// is exactly the behaviour an operator can never see from the outside until it is
// wrong.

// clock is a hand-wound clock so TTL behaviour is tested without sleeping.
type clock struct{ at time.Time }

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newTestService(t *testing.T, tune func(*ManagerOptions, *ServiceOptions)) (*Service, *clock) {
	t.Helper()
	tick := &clock{at: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}

	registry := NewRegistry(RegistryOptions{Default: ProviderMock, Clock: tick.now})
	if err := registry.Register(NewMockProvider()); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	store := NewMemoryStore()
	bus := NewBus(store, tick.now)
	managerOpts := ManagerOptions{
		Registry: registry,
		Store:    store,
		Defaults: Defaults{
			Provider:       ProviderMock,
			Network:        NetworkNone,
			TimeoutMS:      1000,
			MaxOutputBytes: 4096,
			ArgsPolicy:     ArgsNone,
			IdleTTL:        10 * time.Minute,
		},
		IdleTTL:     10 * time.Minute,
		MaxTTL:      time.Hour,
		MaxPerOwner: 2,
		MaxTotal:    4,
		Bus:         bus,
		Clock:       tick.now,
	}
	serviceOpts := ServiceOptions{
		Registry: registry,
		Bus:      bus,
		Queue:    NewJobQueue(),
		Interval: time.Hour,
		Clock:    tick.now,
	}
	if tune != nil {
		tune(&managerOpts, &serviceOpts)
	}
	serviceOpts.Manager = NewManager(managerOpts)
	service := NewService(serviceOpts)
	t.Cleanup(service.Stop)
	return service, tick
}

// codeOf returns the machine code an error carries, so a test asserts the contract
// the console localises rather than the sentence it happens to hold.
func codeOf(t *testing.T, err error) httputil.Code {
	t.Helper()
	var coded *httputil.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected a coded error, got %v", err)
	}
	return coded.Code
}

func TestAcquireReusesTheEnvironmentForTheSameKey(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	req := LeaseRequest{OwnerID: "u1", ResourceID: "agent-1", Scope: "ticket-7"}

	first, err := service.Acquire(ctx, req)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second, err := service.Acquire(ctx, req)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("a ticket must reuse one environment, got %s and %s", first.ID, second.ID)
	}
	leases, err := service.List(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("expected one lease, got %d", len(leases))
	}
}

func TestADifferentScopeGetsItsOwnEnvironment(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	one, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1", Scope: "ticket-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	two, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1", Scope: "ticket-2"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if one.ID == two.ID {
		t.Fatal("two scopes must not share an environment: one ticket's state would leak into another's")
	}
}

func TestAnEmptyScopeFallsBackToDefault(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.Scope != "default" {
		t.Fatalf("expected the default scope, got %q", lease.Scope)
	}
}

func TestAnonymousCallersNeverReuse(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	first, err := service.Acquire(ctx, LeaseRequest{ResourceID: "agent-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	second, err := service.Acquire(ctx, LeaseRequest{ResourceID: "agent-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("an anonymous caller must not inherit somebody's environment")
	}
}

func TestTheOwnerCeilingIsRefusedWithARetryableCode(t *testing.T) {
	service, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.MaxPerOwner = 1
	})
	ctx := context.Background()

	if _, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "one"}); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	_, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "two"})
	if err == nil {
		t.Fatal("the second environment must be refused")
	}
	// Retryable on purpose: the scheduler's ladder consumes this, so it must not
	// look like a policy refusal.
	if code := codeOf(t, err); code != CodeLimitReached {
		t.Fatalf("expected %s, got %s", CodeLimitReached, code)
	}
}

func TestExecRecordsAnAuditRowWithoutTheOutput(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1", Scope: "s1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// The mock prints its arguments back, so stdout carries the secret marker.
	res, err := service.Exec(ctx, lease.ID, ExecRequest{Command: "echo", Args: []string{"token=SECRET"}, DryRun: true})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Provider != ProviderMock {
		t.Fatalf("expected the mock receipt to name its provider, got %q", res.Provider)
	}

	records, err := service.Execs(ctx, lease.ID, 10)
	if err != nil {
		t.Fatalf("execs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one audit row, got %d", len(records))
	}
	row := records[0]
	if !row.DryRun {
		t.Fatal("a console probe must be recorded as a dry run")
	}
	if row.DurationMS < 0 || row.ExitCode != 0 {
		t.Fatalf("unexpected receipt: exit=%d duration=%d", row.ExitCode, row.DurationMS)
	}
	if row.StdoutSHA256 == "" {
		t.Fatal("the audit row keeps a digest of the output, not the output")
	}
}

func TestExecOnAnUnknownLeaseIsNotFound(t *testing.T) {
	service, _ := newTestService(t, nil)
	_, err := service.Exec(context.Background(), "nope", ExecRequest{Command: "echo"})
	if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("expected %s, got %s", CodeNotFound, code)
	}
}

func TestTerminateClosesTheLease(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1", Scope: "s1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := service.Terminate(ctx, lease.ID); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	closed, err := service.Get(ctx, lease.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if closed.State != StateTerminated {
		t.Fatalf("expected %s, got %s", StateTerminated, closed.State)
	}
	// A refused renewal, not a silent resurrection.
	if _, err := service.Renew(ctx, lease.ID); err == nil {
		t.Fatal("a terminated lease must not be renewable")
	}
}

func TestRenewIsCappedByTheMaxTTL(t *testing.T) {
	service, tick := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.MaxTTL = time.Minute
	})
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "s"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := service.Renew(ctx, lease.ID); err != nil {
		t.Fatalf("renew: %v", err)
	}
	tick.advance(2 * time.Minute)
	_, err = service.Renew(ctx, lease.ID)
	if err == nil {
		t.Fatal("a lease past the hard ceiling must not be kept alive")
	}
	if code := codeOf(t, err); code != CodeLeaseLost {
		t.Fatalf("expected %s, got %s", CodeLeaseLost, code)
	}
}

func TestSweepReclaimsAnExpiredLease(t *testing.T) {
	service, tick := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.IdleTTL = time.Minute
		m.Defaults.IdleTTL = time.Minute
	})
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "s"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tick.advance(5 * time.Minute)

	reclaimed, err := service.Manager().Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("expected one reclaimed lease, got %d", reclaimed)
	}
	gone, err := service.Get(ctx, lease.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gone.State != StateExpired && gone.State != StateTerminated {
		t.Fatalf("expected a closed lease, got %s", gone.State)
	}
}

func TestTheCatalogueReportsAnUnavailableEntryWithItsReason(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	if err := service.Registry().Register(NewBwrapProvider()); err != nil {
		t.Fatalf("register bwrap: %v", err)
	}

	catalog := service.Catalog(ctx)
	if len(catalog) != 2 {
		t.Fatalf("expected two entries, got %d", len(catalog))
	}
	byName := map[string]ProviderInfo{}
	for _, info := range catalog {
		byName[info.Name] = info
	}
	if !byName[ProviderMock].Available {
		t.Fatalf("the mock must always be available: %+v", byName[ProviderMock])
	}
	bwrap := byName["bwrap"]
	if bwrap.Available {
		t.Skip("bwrap is installed on this host; the unavailable path cannot be exercised here")
	}
	if bwrap.Reason == "" {
		t.Fatal("an unavailable entry must say why, otherwise the console cannot explain it")
	}
	if bwrap.Layer != LayerProcess {
		t.Fatalf("expected the process layer, got %s", bwrap.Layer)
	}
}

func TestRegisteringTheSameNameTwiceIsRefused(t *testing.T) {
	service, _ := newTestService(t, nil)
	err := service.Registry().Register(NewMockProvider())
	if err == nil {
		t.Fatal("a duplicate provider must not silently replace the first definition")
	}
	if code := codeOf(t, err); code != CodeUnknownProvider {
		t.Fatalf("expected %s, got %s", CodeUnknownProvider, code)
	}
}

func TestAnUnknownProviderIsRefusedBeforeAnythingRuns(t *testing.T) {
	service, _ := newTestService(t, nil)
	_, err := service.Acquire(context.Background(), LeaseRequest{
		OwnerID: "u1", ResourceID: "a", Scope: "s",
		Provider: "definitely-not-installed",
	})
	if err == nil {
		t.Fatal("an unregistered provider must be refused, not substituted")
	}
	if code := codeOf(t, err); code != CodeUnknownProvider {
		t.Fatalf("expected %s, got %s", CodeUnknownProvider, code)
	}
}

func TestStatsCountWhatTheConsoleShows(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "s"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := service.Exec(ctx, lease.ID, ExecRequest{Command: "echo"}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	stats := service.Stats(ctx)
	if stats.ActiveLeases != 1 {
		t.Fatalf("expected one active lease, got %d", stats.ActiveLeases)
	}
	if stats.RecentExecs != 1 {
		t.Fatalf("expected one recent run, got %d", stats.RecentExecs)
	}
}

func TestEventsAreReplayableInOrder(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "s"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := service.Exec(ctx, lease.ID, ExecRequest{Command: "echo"}); err != nil {
		t.Fatalf("exec: %v", err)
	}

	events, maxSeq, err := service.EventsSince(ctx, "u1", 0, 50)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("a lease and an exec must produce frames")
	}
	var last uint64
	for _, event := range events {
		if event.Seq <= last {
			t.Fatalf("frames must arrive in order: %d after %d", event.Seq, last)
		}
		last = event.Seq
	}
	if maxSeq != last {
		t.Fatalf("the cursor must point at the newest frame: %d vs %d", maxSeq, last)
	}
	// Replaying from the newest frame yields nothing new and no duplicate.
	again, _, err := service.EventsSince(ctx, "u1", maxSeq, 50)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no duplicate frames, got %d", len(again))
	}
}

func TestTheQueueHandsOneJobToOneNode(t *testing.T) {
	queue := NewJobQueue()
	job := &Job{ID: "job-1", Provider: ProviderMock, Op: JobExec}
	done := queue.Enqueue(job)
	if queue.Pending() != 1 {
		t.Fatalf("expected one pending job, got %d", queue.Pending())
	}

	claimed, ok := queue.Claim("node-1", []string{ProviderMock}, 0)
	if !ok || claimed.ID != "job-1" {
		t.Fatalf("expected the job, got %+v ok=%v", claimed, ok)
	}
	if _, ok := queue.Claim("node-2", []string{ProviderMock}, 0); ok {
		t.Fatal("a claimed job must not be handed out twice")
	}

	if err := queue.Complete("job-1", ExecResult{ExitCode: 0}, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completing a job must wake whoever is waiting on it")
	}
}

func TestAJobIsOnlyClaimedByANodeThatCanRunIt(t *testing.T) {
	queue := NewJobQueue()
	queue.Enqueue(&Job{ID: "job-2", Provider: "docker", Op: JobExec})
	if _, ok := queue.Claim("node-1", []string{"bwrap"}, 0); ok {
		t.Fatal("a node must not receive a job for a provider it did not advertise")
	}
	if _, ok := queue.Claim("node-1", []string{"remote"}, 0); !ok {
		t.Fatal("the remote wildcard must accept any provider's job")
	}
}

func TestCompletingAnUnknownJobIsAnError(t *testing.T) {
	queue := NewJobQueue()
	err := queue.Complete("missing", ExecResult{}, "")
	if err == nil {
		t.Fatal("completing a job nobody queued must be reported, not swallowed")
	}
	if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("expected %s, got %s", CodeNotFound, code)
	}
}
