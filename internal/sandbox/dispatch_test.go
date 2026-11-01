package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// This file covers the seams that never touch a real backend: the node job
// protocol, the facade's goroutine and its delegations, the catalogue's ordering
// and the failure paths a store can produce. Everything here runs offline.

// ---- node protocol ----

func nodeLoop(queue *JobQueue, stop <-chan struct{}, answer func(job *Job)) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		job, ok := queue.Claim("node-1", []string{"remote"}, 200*time.Millisecond)
		if !ok {
			continue
		}
		answer(job)
	}
}

func TestTheRemoteProviderWaitsForANodeToAnswer(t *testing.T) {
	queue := NewJobQueue()
	provider := NewRemoteProvider(queue)
	stop := make(chan struct{})
	defer close(stop)
	go nodeLoop(queue, stop, func(job *Job) {
		_ = queue.Complete(job.ID, ExecResult{ExitCode: 0, Stdout: "from-node"}, "")
	})

	res, err := provider.Exec(context.Background(), Handle{ID: "lease-1", Ref: "remote-ref"}, ExecRequest{Command: "echo"})
	if err != nil {
		t.Fatalf("exec over the node protocol: %v", err)
	}
	if res.Stdout != "from-node" || res.ExitCode != 0 {
		t.Fatalf("receipt = %+v", res)
	}
	if res.Provider != "remote" || res.Ref != "remote-ref" {
		t.Fatalf("the receipt must name the boundary and the environment: %+v", res)
	}
}

func TestTheRemoteProviderReportsANodeFailure(t *testing.T) {
	queue := NewJobQueue()
	provider := NewRemoteProvider(queue)
	stop := make(chan struct{})
	defer close(stop)
	go nodeLoop(queue, stop, func(job *Job) {
		_ = queue.Complete(job.ID, ExecResult{}, "the node went away")
	})

	_, err := provider.Exec(context.Background(), Handle{ID: "lease-1"}, ExecRequest{Command: "echo"})
	if code := codeOf(t, err); code != CodeNodeOffline {
		t.Fatalf("expected %s, got %s", CodeNodeOffline, code)
	}
}

func TestTheRemoteProviderGivesUpWhenTheCallerDoes(t *testing.T) {
	queue := NewJobQueue()
	provider := NewRemoteProvider(queue)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := provider.Exec(ctx, Handle{ID: "lease-1"}, ExecRequest{Command: "echo"})
	if code := codeOf(t, err); code != CodeTimeout {
		t.Fatalf("expected %s, got %s", CodeTimeout, code)
	}
	// The job must be closed out, otherwise it would sit in the queue forever and
	// a node would eventually run work nobody is waiting for.
	if queue.Pending() != 0 {
		t.Fatalf("a cancelled job must not stay pending, got %d", queue.Pending())
	}
}

func TestRemoteDestroyIsBestEffort(t *testing.T) {
	queue := NewJobQueue()
	provider := NewRemoteProvider(queue)
	stop := make(chan struct{})
	defer close(stop)
	go nodeLoop(queue, stop, func(job *Job) {
		_ = queue.Complete(job.ID, ExecResult{}, "")
	})

	if err := provider.Destroy(context.Background(), Handle{ID: "lease-1"}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestTheRemoteProviderDescribesItsBoundary(t *testing.T) {
	provider := NewRemoteProvider(nil)
	if provider.Queue() == nil {
		t.Fatal("a provider without a queue must build one instead of panicking on first exec")
	}
	if provider.Name() != "remote" {
		t.Fatalf("name = %q", provider.Name())
	}
	desc, err := provider.Describe(context.Background())
	if err != nil || desc.Layer != LayerRemote {
		t.Fatalf("describe = %+v (%v)", desc, err)
	}
	if avail, err := provider.Probe(context.Background()); err != nil || !avail.Available {
		t.Fatalf("probe = %+v (%v)", avail, err)
	}
	h, err := provider.Create(context.Background(), Spec{LeaseID: "lease-2"})
	if err != nil || h.Ref != "lease-2" {
		t.Fatalf("handle = %+v (%v)", h, err)
	}
	if _, err := provider.Attach(context.Background(), h, ExecRequest{}); err == nil {
		t.Fatal("the job protocol is one request per response: attach must be refused")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if state, err := provider.Status(context.Background(), h); err != nil || state != StateReady {
		t.Fatalf("status = %q (%v)", state, err)
	}
	if id := newJobID(); !strings.HasPrefix(id, "job_") {
		t.Fatalf("job id = %q", id)
	}
}

func TestTheQueueTracksWaitersAndJobs(t *testing.T) {
	queue := NewJobQueue()
	queue.EnterWait()
	queue.EnterWait()
	if queue.Waiters() != 2 {
		t.Fatalf("waiters = %d, want 2", queue.Waiters())
	}
	queue.LeaveWait()
	queue.LeaveWait()
	queue.LeaveWait() // an extra leave must not push the counter negative
	if queue.Waiters() != 0 {
		t.Fatalf("waiters = %d, want 0", queue.Waiters())
	}

	job := &Job{ID: "job-1", Provider: "docker", Op: JobExec}
	queue.Enqueue(job)
	if job.State != JobQueued {
		t.Fatalf("enqueue must mark the job queued, got %q", job.State)
	}
	if got, ok := queue.Get("job-1"); !ok || got.ID != "job-1" {
		t.Fatalf("get = %+v ok=%v", got, ok)
	}
	if _, ok := queue.Get("missing"); ok {
		t.Fatal("an unknown job must report false")
	}
	if queue.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", queue.Pending())
	}
	// A node that advertises nothing is only eligible for the remote wildcard.
	if _, ok := queue.Claim("node-1", nil, 0); ok {
		t.Fatal("a docker job must not go to a node with no capabilities")
	}
}

func TestAClaimWakesUpWhenWorkArrives(t *testing.T) {
	queue := NewJobQueue()
	go func() {
		time.Sleep(50 * time.Millisecond)
		queue.Enqueue(&Job{ID: "job-late", Provider: "remote", Op: JobExec})
	}()

	job, ok := queue.Claim("node-1", []string{"remote"}, 2*time.Second)
	if !ok || job.ID != "job-late" {
		t.Fatalf("claim = %+v ok=%v", job, ok)
	}
	if job.State != JobClaimed || job.NodeID != "node-1" {
		t.Fatalf("a claimed job must remember who took it: %+v", job)
	}
}

func TestCompletingAJobTwiceRecordsTheFirstAnswer(t *testing.T) {
	queue := NewJobQueue()
	done := queue.Enqueue(&Job{ID: "job-2", Provider: "remote", Op: JobExec})
	if err := queue.Complete("job-2", ExecResult{ExitCode: 2}, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := queue.Complete("job-2", ExecResult{}, "changed my mind"); err != nil {
		t.Fatalf("a duplicate answer must not panic on a closed channel: %v", err)
	}
	stored, ok := queue.Get("job-2")
	if !ok || stored.State != JobFailed || stored.Error == "" {
		t.Fatalf("stored = %+v", stored)
	}
	select {
	case <-done:
	default:
		t.Fatal("the waiting caller must be woken")
	}
}

// ---- facade ----

func TestTheFacadeRunsTheBeatOnlyWhileStarted(t *testing.T) {
	service, _ := newTestService(t, func(_ *ManagerOptions, opts *ServiceOptions) {
		opts.Interval = time.Hour
	})
	if service.Live() {
		t.Fatal("a facade that was never started must not report live")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for !service.Live() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !service.Live() {
		t.Fatal("Run must report the beat as live")
	}
	service.Kick()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the beat must stop with the context")
	}
	deadline = time.Now().Add(2 * time.Second)
	for service.Live() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if service.Live() {
		t.Fatal("a stopped facade must report not live")
	}
}

func TestExecOnKeepsAnOwnedLeaseAndReleasesAnAnonymousOne(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	res, owned, err := service.ExecOn(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"}, ExecRequest{Command: "echo"})
	if err != nil {
		t.Fatalf("exec on an owned key: %v", err)
	}
	if owned == nil || res.Provider != ProviderMock {
		t.Fatalf("res = %+v lease = %+v", res, owned)
	}
	if kept, err := service.Get(ctx, owned.ID); err != nil || kept.State != StateReady {
		t.Fatalf("an owned environment must survive the call: %+v (%v)", kept, err)
	}

	_, anonymous, err := service.ExecOn(ctx, LeaseRequest{ResourceID: "a"}, ExecRequest{Command: "echo"})
	if err != nil {
		t.Fatalf("exec on an anonymous key: %v", err)
	}
	gone, err := service.Get(ctx, anonymous.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gone.State != StateTerminated {
		t.Fatalf("nobody owns it, so it must be handed back: %q", gone.State)
	}
}

func TestAttachAndAttachOnRefuseProvidersWithoutSessions(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	lease, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := service.Attach(ctx, lease.ID, ExecRequest{Command: "mcp"}); err == nil {
		t.Fatal("the mock has no process to attach to")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if _, err := service.Attach(ctx, "nope", ExecRequest{}); err == nil {
		t.Fatal("an unknown lease must be a not-found")
	} else if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("expected %s, got %s", CodeNotFound, code)
	}

	_, anonymous, err := service.AttachOn(ctx, LeaseRequest{ResourceID: "a"}, ExecRequest{Command: "mcp"})
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if anonymous == nil {
		t.Fatal("the caller needs the lease id even when attach failed")
	}
	returned, err := service.Get(ctx, anonymous.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if returned.State != StateTerminated {
		t.Fatalf("a failed anonymous attach must release its environment, got %q", returned.State)
	}
}

func TestTheFacadeExposesItsQueueAndAuditWriter(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	if service.Queue() == nil || service.Manager() == nil || service.Registry() == nil {
		t.Fatal("the facade must expose the pieces the HTTP layer needs")
	}
	if err := service.SaveJob(ctx, &Job{ID: "job-1", Provider: ProviderMock, Op: JobExec, State: JobDone}); err != nil {
		t.Fatalf("save job: %v", err)
	}
	service.Queue().Enqueue(&Job{ID: "job-2", Provider: "remote", Op: JobExec})
	if stats := service.Stats(ctx); stats.PendingJobs != 1 {
		t.Fatalf("pending jobs = %d, want 1", stats.PendingJobs)
	}
}

func TestFindMatchesOnlyALiveKey(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	req := LeaseRequest{OwnerID: "u1", ResourceID: "a", Scope: "s1"}

	if found, err := service.Manager().Find(ctx, req); err != nil || found != nil {
		t.Fatalf("an empty key must find nothing: %+v (%v)", found, err)
	}
	lease, err := service.Acquire(ctx, req)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	found, err := service.Manager().Find(ctx, req)
	if err != nil || found == nil || found.ID != lease.ID {
		t.Fatalf("find = %+v (%v)", found, err)
	}
	if profile := service.Manager().Defaults(); profile.Provider != ProviderMock {
		t.Fatalf("defaults = %+v", profile)
	}
}

// ---- failure paths a store can produce ----

type failingStore struct {
	Store
	err error
}

func (f failingStore) CountLeases(context.Context, string) (int, error) { return 0, f.err }
func (f failingStore) MaxSeq(context.Context) (uint64, error)           { return 0, f.err }
func (f failingStore) ListNodes(context.Context) ([]*Node, error)       { return nil, f.err }
func (f failingStore) AppendEvent(context.Context, *Event) error        { return f.err }

type maxSeqFailer struct{ Store }

func (maxSeqFailer) MaxSeq(context.Context) (uint64, error) {
	return 0, errors.New("sequence unavailable")
}

func TestABrokenStoreIsReportedNotSwallowed(t *testing.T) {
	service, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.Store = failingStore{Store: NewMemoryStore(), err: errors.New("database down")}
	})
	ctx := context.Background()

	if stats := service.Stats(ctx); stats.LastError == "" {
		t.Fatal("the water level must carry the failure instead of showing zeroes")
	}
	if stats := service.Stats(ctx); stats.OnlineNodes != 0 {
		t.Fatalf("online nodes = %d, want 0", stats.OnlineNodes)
	}
	if _, err := service.Nodes(ctx); err == nil {
		t.Fatal("listing nodes must fail loudly")
	}
	if _, _, err := service.EventsSince(ctx, "u1", 0, 0); err == nil {
		t.Fatal("replay must fail loudly")
	}
}

func TestEventsSinceReportsACursorFailure(t *testing.T) {
	service, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.Store = maxSeqFailer{Store: NewMemoryStore()}
	})
	if _, _, err := service.EventsSince(context.Background(), "u1", 0, 10); err == nil {
		t.Fatal("a store that cannot report its cursor must not claim success")
	}
}

func TestTheBusDropsFramesAfterClose(t *testing.T) {
	bus := NewBus(NewMemoryStore(), nil)
	ch, release := bus.Subscribe("owner:u1", 1)

	bus.Publish(Event{Kind: EventLeaseCreated, Subject: "owner:u1"})
	bus.Publish(Event{Kind: EventExec, Subject: "owner:u1"}) // subscriber is full: dropped, never blocking
	bus.Close()
	bus.Close() // safe twice
	bus.Publish(Event{Kind: EventExec, Subject: "owner:u1"})

	frames := 0
	for range ch {
		frames++
	}
	if frames != 1 {
		t.Fatalf("a wedged subscriber must lose the excess, not stall the manager: got %d frames", frames)
	}
	release()
}

func TestAPublishSurvivesAFailedAuditAppend(t *testing.T) {
	bus := NewBus(failingStore{Store: NewMemoryStore(), err: errors.New("append failed")}, nil)
	ch, release := bus.Subscribe("", 4)
	defer release()

	bus.Publish(Event{Kind: EventLeaseCreated})

	select {
	case frame := <-ch:
		if frame.Seq != 0 {
			t.Fatalf("a frame that was not persisted must not claim a sequence, got %d", frame.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("the audit trail failing must not stop the live frame")
	}
}

// ---- catalogue ----

type describeFailer struct{ providerFunc }

func (describeFailer) Describe(context.Context) (Descriptor, error) {
	return Descriptor{}, errors.New("describe failed")
}

type probeFailer struct{ providerFunc }

func (probeFailer) Probe(context.Context) (Availability, error) {
	return Availability{Available: false, Reason: "not installed here"}, nil
}

func TestTheCatalogueOrdersTheDefaultThenByLayer(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Default: "mock", Clock: time.Now})
	register := func(name string, layer Layer) {
		t.Helper()
		if err := registry.Register(providerFunc{name: name, desc: Descriptor{Layer: layer}}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	register("mock", LayerNone) // the default, listed last by layer
	register("zz-none", LayerNone)
	register("aa-process", LayerProcess)
	register("aa-none", LayerNone)
	register("docker", LayerContainer)
	// An unavailable backend sorts after every available one, whatever its layer:
	// the console must show what can run here before what merely exists.
	if err := registry.Register(probeFailer{providerFunc{name: "down", desc: Descriptor{Layer: LayerContainer}}}); err != nil {
		t.Fatalf("register down: %v", err)
	}

	rows := registry.Catalog(context.Background())
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.Name)
	}
	want := []string{"mock", "docker", "aa-process", "aa-none", "zz-none", "down"}
	if !equalStrings(got, want) {
		t.Fatalf("catalog order = %v, want %v", got, want)
	}
	if !rows[0].Default {
		t.Fatal("the entry AGENTHUB_SANDBOX_PROVIDER falls back to must be marked")
	}
}

func TestInfoAndRequireAvailableRouteThroughTheProbe(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Default: ProviderMock, Clock: time.Now})
	if err := registry.Register(NewMockProvider()); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	if err := registry.Register(probeFailer{providerFunc{name: "down", desc: Descriptor{Layer: LayerProcess}}}); err != nil {
		t.Fatalf("register down: %v", err)
	}
	ctx := context.Background()

	info, err := registry.Info(ctx, "")
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if !info.Available || !info.Default {
		t.Fatalf("info = %+v", info)
	}
	if _, err := registry.Info(ctx, "nope"); err == nil {
		t.Fatal("an unknown name must be an error, not an empty row")
	} else if code := codeOf(t, err); code != CodeUnknownProvider {
		t.Fatalf("expected %s, got %s", CodeUnknownProvider, code)
	}
	if _, _, err := registry.RequireAvailable(ctx, "down"); err == nil {
		t.Fatal("a backend that cannot run here must be refused")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	provider, info, err := registry.RequireAvailable(ctx, ProviderMock)
	if err != nil || provider.Name() != ProviderMock || info.Layer != LayerNone {
		t.Fatalf("require available = %+v (%v)", info, err)
	}
}

type probeErrorer struct{ providerFunc }

func (probeErrorer) Probe(context.Context) (Availability, error) {
	return Availability{}, errors.New("probe exploded")
}

func TestAProbeThatErrorsStillRendersAMRow(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Default: "flaky", Clock: time.Now})
	if err := registry.Register(probeErrorer{providerFunc{name: "flaky", desc: Descriptor{Layer: LayerProcess}}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	info, err := registry.Info(context.Background(), "flaky")
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Available || !strings.Contains(info.Reason, "probe exploded") {
		t.Fatalf("a probe error must become a reason, got %+v", info)
	}
}

func TestACatalogueEntrySurvivesADescribeFailure(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Default: "bad", Clock: time.Now})
	if err := registry.Register(describeFailer{providerFunc{name: "bad", desc: Descriptor{Layer: LayerContainer}}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	info, err := registry.Info(context.Background(), "bad")
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	// The row still has to render: a provider that cannot describe itself is
	// listed honestly as unknown instead of vanishing from the console.
	if info.Layer != LayerNone || info.Name != "bad" {
		t.Fatalf("info = %+v", info)
	}
}

func TestResolveRefusesOffAndUnknownNames(t *testing.T) {
	registry := NewRegistry(RegistryOptions{})
	if _, err := registry.Resolve(""); err == nil {
		t.Fatal("no default and no name means the feature is off")
	} else if code := codeOf(t, err); code != CodeDisabled {
		t.Fatalf("expected %s, got %s", CodeDisabled, code)
	}
	if _, err := registry.Resolve(ProviderOff); err == nil {
		t.Fatal("off must not resolve to anything")
	} else if code := codeOf(t, err); code != CodeDisabled {
		t.Fatalf("expected %s, got %s", CodeDisabled, code)
	}
	if err := registry.Register(NewMockProvider()); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Names arrive from JSON, so surrounding whitespace must be tolerated.
	provider, err := registry.Resolve("  " + ProviderMock + "  ")
	if err != nil || provider.Name() != ProviderMock {
		t.Fatalf("resolve = %v (%v)", provider, err)
	}
	if _, err := registry.Resolve("nope"); err == nil {
		t.Fatal("an unknown provider must be refused")
	} else if code := codeOf(t, err); code != CodeUnknownProvider {
		t.Fatalf("expected %s, got %s", CodeUnknownProvider, code)
	}
}

func TestLayerRankFallsBackPastTheKnownLayers(t *testing.T) {
	if got := layerRank(LayerContainer); got != 0 {
		t.Fatalf("the strongest layer must rank first, got %d", got)
	}
	if got := layerRank(Layer("not-a-layer")); got != len(Layers) {
		t.Fatalf("an unknown layer must sort last, got %d", got)
	}
}

func TestTheRegistryRejectsEmptyDefinitionsAndListsItsNames(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Default: ProviderMock})
	if err := registry.Register(nil); err == nil {
		t.Fatal("a nil provider must be refused")
	} else if code := codeOf(t, err); code != CodeUnknownProvider {
		t.Fatalf("expected %s, got %s", CodeUnknownProvider, code)
	}
	if err := registry.Register(providerFunc{name: "   "}); err == nil {
		t.Fatal("a provider without a name cannot be catalogued")
	}
	if err := registry.Register(NewMockProvider()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if names := registry.Names(); len(names) != 1 || names[0] != ProviderMock {
		t.Fatalf("names = %v", names)
	}
	if registry.Default() != ProviderMock {
		t.Fatalf("default = %q", registry.Default())
	}
}

func TestTheStubProviderAndTheNopStoreStayHonest(t *testing.T) {
	mock := NewMockProvider()
	if state, err := mock.Status(context.Background(), Handle{}); err != nil || state != StateReady {
		t.Fatalf("status = %q (%v)", state, err)
	}
	if err := NopStore().DeleteLease(context.Background(), "lease-1"); err != nil {
		t.Fatalf("a store without a database has nothing to delete: %v", err)
	}
}

func TestTheSweeperStopsWithoutTheContext(t *testing.T) {
	service, _ := newTestService(t, func(_ *ManagerOptions, opts *ServiceOptions) {
		// A fast tick exercises the timed pass as well as the kick.
		opts.Interval = 10 * time.Millisecond
	})
	done := make(chan error, 1)
	go func() { done <- service.Run(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	for !service.Live() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	// Stop is the path a shutdown takes: the beat has to end without a context
	// being cancelled, and calling it twice must stay safe.
	service.Stop()
	service.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop must end the beat")
	}
}

// ---- exec failure decides the lease's fate ----

type execFailer struct {
	providerFunc
	err error
}

func (e execFailer) Exec(context.Context, Handle, ExecRequest) (ExecResult, error) {
	return ExecResult{}, e.err
}

func TestAFailedExecDecidesTheLeasesFate(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	registry := service.Registry()
	if err := registry.Register(execFailer{
		providerFunc: providerFunc{name: "lost", desc: Descriptor{Layer: LayerProcess}},
		err:          Error(CodeUnsupported),
	}); err != nil {
		t.Fatalf("register lost: %v", err)
	}
	if err := registry.Register(execFailer{
		providerFunc: providerFunc{name: "slow", desc: Descriptor{Layer: LayerProcess}},
		err:          Error(CodeTimeout),
	}); err != nil {
		t.Fatalf("register slow: %v", err)
	}

	lost, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "a", Provider: "lost"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := service.Exec(ctx, lost.ID, ExecRequest{Command: "x"}); err == nil {
		t.Fatal("the provider refused the command")
	}
	failed, err := service.Get(ctx, lost.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if failed.State != StateFailed {
		t.Fatalf("a lost environment must be marked failed so the next acquire rebuilds it, got %q", failed.State)
	}

	slow, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "b", Scope: "other", Provider: "slow"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := service.Exec(ctx, slow.ID, ExecRequest{Command: "x"}); err == nil {
		t.Fatal("the provider timed out")
	}
	stillThere, err := service.Get(ctx, slow.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stillThere.State != StateReady {
		t.Fatalf("a timeout must not destroy a usable environment, got %q", stillThere.State)
	}

	// A lease row without a reference is a lease whose environment is gone.
	if err := service.manager.opts.Store.SaveLease(ctx, &Lease{ID: "orphan", OwnerID: "u1", Provider: ProviderMock, State: StateReady}); err != nil {
		t.Fatalf("save lease: %v", err)
	}
	if _, err := service.Exec(ctx, "orphan", ExecRequest{Command: "x"}); err == nil {
		t.Fatal("a lease with no handle must be refused")
	} else if code := codeOf(t, err); code != CodeLeaseLost {
		t.Fatalf("expected %s, got %s", CodeLeaseLost, code)
	}
}

// ---- local provider and small helpers ----

func TestTheLocalProviderRunsACommandWithoutAShell(t *testing.T) {
	local := NewLocalProvider()
	ctx := context.Background()
	h, err := local.Create(ctx, Spec{LeaseID: "lease-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := local.Exec(ctx, h, ExecRequest{
		Command: fakeBinary(t, "cat"),
		Stdin:   "through-the-pipe",
		Env:     map[string]string{"INSIDE": "1"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Stdout != "through-the-pipe" || res.Provider != "local" {
		t.Fatalf("receipt = %+v", res)
	}

	session, err := local.Attach(ctx, h, ExecRequest{Command: fakeBinary(t, "mcp")})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	reply, err := session.RoundTrip(ctx, []byte(`{"id":1}`))
	if err != nil || !strings.Contains(string(reply), "echo") {
		t.Fatalf("reply = %q (%v)", reply, err)
	}
	_ = session.Close()

	if _, err := local.Exec(ctx, h, ExecRequest{Command: missingBinary(t)}); err == nil {
		t.Fatal("a missing binary must be reported")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if state, err := local.Status(ctx, h); err != nil || state != StateReady {
		t.Fatalf("status = %q (%v)", state, err)
	}
	if err := local.Destroy(ctx, h); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestUtilityHelpersKeepTheirContracts(t *testing.T) {
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q", got)
	}
	if got := itoa(-42); got != "-42" {
		t.Fatalf("itoa(-42) = %q", got)
	}
	if got := itoa(7); got != "7" {
		t.Fatalf("itoa(7) = %q", got)
	}

	if got := scrubCommand("   ", []string{"a"}); got != "" {
		t.Fatalf("an empty command must stay empty, got %q", got)
	}
	if got := scrubCommand("git", nil); got != "git" {
		t.Fatalf("scrub = %q", got)
	}
	if got := scrubCommand("git", []string{"--token", "abc"}); got != "git (+2 args)" {
		t.Fatalf("the audit trail must never keep the arguments: %q", got)
	}

	if got := firstNonEmpty("", "  ", "b"); got != "b" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", "  "); got != "" {
		t.Fatalf("firstNonEmpty with only blanks = %q", got)
	}

	if envNames(nil) != nil {
		t.Fatal("no variables must mean no allowlist")
	}
	if got := envNames(map[string]string{"A": "1"}); len(got) != 1 || got[0] != "A" {
		t.Fatalf("envNames = %v", got)
	}

	if got := requestTimeout(ExecRequest{}); got != time.Minute {
		t.Fatalf("default timeout = %v", got)
	}
	if got := requestTimeout(ExecRequest{TimeoutMS: 1500}); got != 1500*time.Millisecond {
		t.Fatalf("timeout = %v", got)
	}
}

func TestTheQueueIgnoresAStalePendingEntry(t *testing.T) {
	queue := NewJobQueue()
	queue.Enqueue(&Job{ID: "job-1", Provider: "remote", Op: JobExec})
	queue.Enqueue(&Job{ID: "job-1", Provider: "remote", Op: JobExec}) // same id, twice

	if _, ok := queue.Claim("node-1", []string{"remote"}, 0); !ok {
		t.Fatal("the first claim must succeed")
	}
	if _, ok := queue.Claim("node-1", []string{"remote"}, 0); ok {
		t.Fatal("the stale pending entry must not hand the same job out twice")
	}
}

func TestTheMemoryStoreGuardsAndBounds(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Nil input is a no-op everywhere, not a panic: the audit path calls these
	// with whatever the caller had, including nothing.
	if err := store.SaveLease(ctx, nil); err != nil {
		t.Fatalf("save nil lease: %v", err)
	}
	if err := store.AppendExec(ctx, nil); err != nil {
		t.Fatalf("append nil exec: %v", err)
	}
	if err := store.SaveNode(ctx, nil); err != nil {
		t.Fatalf("save nil node: %v", err)
	}
	if err := store.SaveJob(ctx, nil); err != nil {
		t.Fatalf("save nil job: %v", err)
	}
	if err := store.AppendEvent(ctx, nil); err != nil {
		t.Fatalf("append nil event: %v", err)
	}

	for i, owner := range []string{"u1", "u2"} {
		if err := store.SaveLease(ctx, &Lease{
			ID: "lease-" + owner, OwnerID: owner, Provider: ProviderMock, State: StateReady,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			ExpiresAt: base.Add(10 * time.Hour),
		}); err != nil {
			t.Fatalf("save lease: %v", err)
		}
	}
	if err := store.SaveLease(ctx, &Lease{
		ID: "lease-closed", OwnerID: "u1", Provider: ProviderMock, State: StateTerminated,
		CreatedAt: base, ExpiresAt: base,
	}); err != nil {
		t.Fatalf("save closed lease: %v", err)
	}
	// The most recently created lease comes first, so the console lists newest.
	if all, err := store.ListLeases(ctx, ""); err != nil || len(all) != 3 || all[0].ID != "lease-u2" {
		t.Fatalf("list = %+v (%v)", all, err)
	}
	if only, err := store.ListLeases(ctx, "u1"); err != nil || len(only) != 2 {
		t.Fatalf("filtered list = %+v (%v)", only, err)
	}
	if n, err := store.CountLeases(ctx, "u1"); err != nil || n != 1 {
		t.Fatalf("live leases for u1 = %d (%v)", n, err)
	}

	// Expired means ready and past its deadline; the closed lease must not be
	// handed to a sweep that would try to destroy it again.
	for _, old := range []*Lease{
		{ID: "lease-old", OwnerID: "u1", Provider: ProviderMock, State: StateReady, CreatedAt: base, ExpiresAt: base.Add(time.Minute)},
		{ID: "lease-older", OwnerID: "u2", Provider: ProviderMock, State: StateReady, CreatedAt: base, ExpiresAt: base},
	} {
		if err := store.SaveLease(ctx, old); err != nil {
			t.Fatalf("save old lease: %v", err)
		}
	}
	expired, err := store.ExpiredLeases(ctx, base.Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("expired: %v", err)
	}
	// Oldest deadline first: a bounded sweep reclaims the most overdue first.
	if len(expired) != 2 || expired[0].ID != "lease-older" {
		t.Fatalf("expired = %+v", expired)
	}
	bounded, err := store.ExpiredLeases(ctx, base.Add(2*time.Hour), 1)
	if err != nil || len(bounded) != 1 {
		t.Fatalf("the batch size must be honoured: %+v (%v)", bounded, err)
	}
	unbounded, err := store.ExpiredLeases(ctx, base.Add(2*time.Hour), 0)
	if err != nil || len(unbounded) != 2 {
		t.Fatalf("a zero limit means no limit: %+v (%v)", unbounded, err)
	}

	for i := 0; i < 2; i++ {
		if err := store.AppendExec(ctx, &ExecRecord{ID: "exec", LeaseID: "lease-u1", At: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("append exec: %v", err)
		}
	}
	if rows, err := store.ListExecs(ctx, "lease-u1", 1); err != nil || len(rows) != 1 {
		t.Fatalf("limited execs = %+v (%v)", rows, err)
	}

	for i, id := range []string{"node-a", "node-b"} {
		if err := store.SaveNode(ctx, &Node{ID: id, CreatedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatalf("save node: %v", err)
		}
	}
	if nodes, err := store.ListNodes(ctx); err != nil || len(nodes) != 2 || nodes[0].ID != "node-b" {
		t.Fatalf("nodes = %+v (%v)", nodes, err)
	}

	for i := 0; i < 3; i++ {
		if err := store.AppendEvent(ctx, &Event{Kind: EventExec, Subject: "owner:u1"}); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	if events, err := store.EventsSince(ctx, "owner:u1", 1, 0); err != nil || len(events) != 2 {
		t.Fatalf("replay = %+v (%v)", events, err)
	}
	if events, err := store.EventsSince(ctx, "owner:u1", 0, 1); err != nil || len(events) != 1 {
		t.Fatalf("limited replay = %+v (%v)", events, err)
	}

	// The in-memory stream keeps the last 512 frames per subject.
	for i := 0; i < 513; i++ {
		if err := store.AppendEvent(ctx, &Event{Kind: EventExec, Subject: "owner:u2"}); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	if events, err := store.EventsSince(ctx, "owner:u2", 0, 0); err != nil || len(events) != 512 {
		t.Fatalf("the in-memory trail must stay bounded, got %d entries (%v)", len(events), err)
	}
}

func TestTheMemoryStoreDeletesALease(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if err := store.SaveLease(ctx, &Lease{ID: "lease-1", OwnerID: "u1", State: StateReady}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.DeleteLease(ctx, "lease-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := store.GetLease(ctx, "lease-1")
	if err != nil || got != nil {
		t.Fatalf("get after delete = %+v (%v)", got, err)
	}
}
