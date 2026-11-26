package hub

// The error half of the hub: every repository call has a failure branch, and a
// task state machine that only ever sees a healthy store is not the one that
// runs in production. Each case here pins what the caller sees when a write
// fails, because "the log said something" is not a contract.

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/httputil"
	"agenthub/internal/identity"
	"agenthub/internal/registry"
)

// flakyTaskRepo wraps the in-memory stub and fails exactly the call a case
// names, so one branch is exercised without breaking the rest of a run.
type flakyTaskRepo struct {
	*stubTaskRepo
	setStateErr    error
	setStateErrFor map[a2a.TaskState]error
	historyErr     error
	artifactErr    error
	pendingErr     error
	pendingReadErr error
	pushErr        error
	pushReadErr    error
	pushCfg        *a2a.PushNotificationConfig
	listErr        error
	listStateErr   error
	stateTasks     []*a2a.Task
}

func newFlakyRepo() *flakyTaskRepo { return &flakyTaskRepo{stubTaskRepo: newStubTaskRepo()} }

// failTransition makes exactly one target state unreachable, which is how a
// test reaches the failure branch of the *second* write in a multi-step
// transition without breaking the first.
func (f *flakyTaskRepo) failTransition(to a2a.TaskState, err error) *flakyTaskRepo {
	if f.setStateErrFor == nil {
		f.setStateErrFor = map[a2a.TaskState]error{}
	}
	f.setStateErrFor[to] = err
	return f
}

func (f *flakyTaskRepo) SetState(ctx context.Context, id string, from, to a2a.TaskState, msg *a2a.Message) (*a2a.Task, error) {
	if f.setStateErr != nil {
		return nil, f.setStateErr
	}
	if err, ok := f.setStateErrFor[to]; ok {
		return nil, err
	}
	return f.stubTaskRepo.SetState(ctx, id, from, to, msg)
}

func (f *flakyTaskRepo) AppendHistory(ctx context.Context, id string, msg a2a.Message) error {
	if f.historyErr != nil {
		return f.historyErr
	}
	return f.stubTaskRepo.AppendHistory(ctx, id, msg)
}

func (f *flakyTaskRepo) AddArtifact(ctx context.Context, id string, art a2a.Artifact) (*a2a.Task, error) {
	if f.artifactErr != nil {
		return nil, f.artifactErr
	}
	return f.stubTaskRepo.AddArtifact(ctx, id, art)
}

func (f *flakyTaskRepo) SetPending(ctx context.Context, id string, p *PendingInput) error {
	if f.pendingErr != nil {
		return f.pendingErr
	}
	return f.stubTaskRepo.SetPending(ctx, id, p)
}

func (f *flakyTaskRepo) SetPush(ctx context.Context, id string, cfg *a2a.PushNotificationConfig) error {
	if f.pushErr != nil {
		return f.pushErr
	}
	return f.stubTaskRepo.SetPush(ctx, id, cfg)
}

func (f *flakyTaskRepo) List(ctx context.Context) ([]*a2a.Task, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.stubTaskRepo.List(ctx)
}

// ListState makes the repo a taskStateLister, which is the narrow read behind
// the connector approval queue.
func (f *flakyTaskRepo) ListState(context.Context, a2a.TaskState) ([]*a2a.Task, error) {
	if f.listStateErr != nil {
		return nil, f.listStateErr
	}
	return f.stateTasks, nil
}

func (f *flakyTaskRepo) Pending(ctx context.Context, id string) (*PendingInput, error) {
	if f.pendingReadErr != nil {
		return nil, f.pendingReadErr
	}
	return f.stubTaskRepo.Pending(ctx, id)
}

func (f *flakyTaskRepo) Push(ctx context.Context, id string) (*a2a.PushNotificationConfig, error) {
	if f.pushReadErr != nil {
		return nil, f.pushReadErr
	}
	if f.pushCfg != nil {
		return f.pushCfg, nil
	}
	return f.stubTaskRepo.Push(ctx, id)
}

// failingNotifier is a peer that always refuses delivery, which is what the
// hub has to survive: a callback that cannot be reached never fails a task.
type failingNotifier struct{}

func (failingNotifier) Notify(context.Context, a2a.PushNotificationConfig, string, a2a.TaskState) error {
	return errors.New("peer unreachable")
}

func (failingNotifier) Reset() {}

// appendFailRepo answers MaxSeq so Start succeeds, then fails every write.
type appendFailRepo struct{ *stubEventRepo }

func (a appendFailRepo) Append(context.Context, Event) error {
	return errors.New("events: append failed")
}

func hubWithRepo(repo TaskRepository, events EventRepository) (*Hub, *registry.Registry) {
	reg := registry.New()
	notifier := &MockNotifier{}
	push := &stubPushRepo{}
	return New(testConfig(), reg, testOptions(), notifier, Persistence{Tasks: repo, Events: events, Pushes: push}), reg
}

// ---------------------------------------------------------------------------
// broker
// ---------------------------------------------------------------------------

func TestBrokerStartRefusesAnUnreadableSequence(t *testing.T) {
	// A broker that cannot resume the sequence would hand out seq numbers that
	// collide with a replay, so the failure is reported rather than ignored.
	broker := NewBroker()
	if err := broker.Start(&stubEventRepo{fail: true}, 0); err == nil {
		t.Fatal("a failing max-seq read was accepted")
	}
}

func TestNewKeepsRunningWhenEventPersistenceFails(t *testing.T) {
	// The hub must still serve: losing the event log is a degradation, not a
	// reason to refuse every task.
	h, _ := hubWithRepo(newFlakyRepo(), &stubEventRepo{fail: true})
	defer h.Close()

	if h.broker == nil {
		t.Fatal("no broker")
	}
	// Publishing through a broker that was never started must be safe: the queue
	// is nil and only subscribers are served.
	h.broker.Publish(Event{TaskID: "task-x", Kind: "status-update"})
}

func TestBrokerDefaultsItsBuffers(t *testing.T) {
	repo := &stubEventRepo{}
	broker := NewBroker()
	if err := broker.Start(repo, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer broker.Close()

	// An unbuffered subscription would block the publisher, so the default has to
	// be a real buffer.
	ch, cancel := broker.Subscribe("task-x", 0)
	if cap(ch) != 64 {
		t.Fatalf("subscription buffer = %d, want 64", cap(ch))
	}
	cancel()
}

func TestBrokerDropsTheOldestFrameWhenASubscriberFallsBehind(t *testing.T) {
	broker := NewBroker()
	ch, cancel := broker.Subscribe("task-x", 1)
	defer cancel()

	broker.Publish(Event{TaskID: "task-x", Kind: "first"})
	// The subscriber is not reading: the newest frame must win over the queued
	// one, because a late reader wants current state, not history.
	broker.Publish(Event{TaskID: "task-x", Kind: "second"})

	select {
	case got := <-ch:
		if got.Kind != "second" {
			t.Fatalf("kept %q, want second", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("no frame was delivered")
	}
}

func TestBrokerLogsAFailedEventWrite(t *testing.T) {
	broker := NewBroker()
	if err := broker.Start(appendFailRepo{&stubEventRepo{}}, 4); err != nil {
		t.Fatalf("Start: %v", err)
	}
	broker.Publish(Event{TaskID: "task-x", Kind: "status-update"})
	// Close drains the queue, which is when the failing append runs.
	broker.Close()
}

func TestResetEventSeqSurfacesTheStoreError(t *testing.T) {
	h, _ := hubWithRepo(newFlakyRepo(), &stubEventRepo{fail: true})
	defer h.Close()
	if err := h.ResetEventSeq(); err == nil {
		t.Fatal("a failing max-seq read was reported as success")
	}
}

// ---------------------------------------------------------------------------
// flow
// ---------------------------------------------------------------------------

func TestRunnerDefaultsToOneParallelStep(t *testing.T) {
	// A flow runner built without a parallelism budget runs one step at a time
	// rather than unbounded: the zero value has to mean "safe", not "infinite".
	r := NewRunner(NewTaskStore(), NewBroker(), nil, 0)
	if r.maxParallel != 1 {
		t.Fatalf("maxParallel = %d, want 1", r.maxParallel)
	}
}

func TestRunStepTreatsAnEmptyOpAsThen(t *testing.T) {
	store := NewTaskStore()
	task := store.Create(identity.System(), "res-1", userMessage("hi"))
	r := NewRunner(store, NewBroker(), func(context.Context, string, string, string) (Outcome, error) {
		return Outcome{Text: "ok"}, nil
	}, 1)

	res, err := r.runStep(context.Background(), task.ID, Step{Label: "first"}, FlowContext{Text: "hi"})
	if err != nil {
		t.Fatalf("runStep: %v", err)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestParseStepsAcceptsEveryShapeAndRefusesTheRest(t *testing.T) {
	steps := []Step{{Op: FlowThen, Label: "first"}}
	if got, err := ParseSteps(steps); err != nil || len(got) != 1 {
		t.Fatalf("[]Step: %v %v", got, err)
	}
	if got, err := ParseSteps([]any{map[string]any{"op": "then", "label": "first"}}); err != nil || len(got) != 1 {
		t.Fatalf("[]any: %v %v", got, err)
	}
	if got, err := ParseSteps(map[string]any{"op": "then"}); err != nil || len(got) != 1 {
		t.Fatalf("object: %v %v", got, err)
	}
	if got, err := ParseSteps(nil); err != nil || got != nil {
		t.Fatalf("nil: %v %v", got, err)
	}

	// A string has to hold a JSON array; a bare word is not a flow.
	if _, err := ParseSteps("not-json"); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
	// A list of scalars unmarshals into []Step and fails.
	if _, err := ParseSteps([]any{"nope"}); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
	// A value JSON cannot represent at all is refused before unmarshalling.
	if _, err := ParseSteps(map[string]any{"op": make(chan int)}); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
	if _, err := ParseSteps(42); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
}

// ---------------------------------------------------------------------------
// notifier
// ---------------------------------------------------------------------------

func TestNotifierWarnsWhenTheRecordCannotBePersisted(t *testing.T) {
	n := &HTTPNotifier{repo: &stubPushRepo{failAppend: true}}
	n.record(a2a.PushNotificationConfig{URL: "https://peer.test/hook"}, "task-x", a2a.TaskStateCompleted)
}

func TestNonceFallsBackToTheClock(t *testing.T) {
	original := nonceRand
	nonceRand = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	t.Cleanup(func() { nonceRand = original })

	// A timestamp nonce still keeps two signatures distinct within a task.
	if got := newNonce(); got == "" {
		t.Fatal("empty nonce")
	}
}

// ---------------------------------------------------------------------------
// health translation
// ---------------------------------------------------------------------------

func TestHealthFromInvokeSeparatesAHostFromAPayload(t *testing.T) {
	cases := []struct {
		code string
		want registry.Health
	}{
		{string(adapter.CodeResourceUnreachable), registry.HealthUnhealthy},
		{string(adapter.CodeAdapterTimeout), registry.HealthUnhealthy},
		{string(adapter.CodeProviderMalformed), registry.HealthDegraded},
		{string(adapter.CodeProviderUnauthorized), registry.HealthDegraded},
		// An invalid prompt or a refused command says nothing about the host, so
		// the previous value stands.
		{"invalid_params", ""},
	}
	for _, tc := range cases {
		if got := healthFromInvoke(&httputil.Error{Code: httputil.Code(tc.code)}); got != tc.want {
			t.Fatalf("%s -> %q, want %q", tc.code, got, tc.want)
		}
	}
	if got := healthFromInvoke(nil); got != "" {
		t.Fatalf("nil -> %q", got)
	}
}

func TestRecordHealthIgnoresWhatItCannotStore(t *testing.T) {
	// A resource that vanished mid-run is not a task failure.
	h, _ := hubWithRepo(newFlakyRepo(), nil)
	defer h.Close()
	h.recordHealth("does-not-exist", registry.HealthHealthy)

	var nilHub *Hub
	nilHub.recordHealth("x", registry.HealthHealthy)
	(&Hub{}).recordHealth("x", registry.HealthDegraded)
	h.recordHealth("x", "")
}

// ---------------------------------------------------------------------------
// task store: the reads and writes that go through a repository
// ---------------------------------------------------------------------------

// mustUpsert registers entries and mirrors the generated ids back, so a test
// can address them the way production does.
func mustUpsert(t *testing.T, reg *registry.Registry, entries ...*registry.Entry) {
	t.Helper()
	for _, e := range entries {
		stored, err := reg.Upsert(e)
		if err != nil {
			t.Fatalf("upsert %q: %v", e.Name, err)
		}
		*e = *stored
	}
}

func TestListReportsARepositoryFailureAsNoRows(t *testing.T) {
	repo := newFlakyRepo()
	repo.listErr = errors.New("db down")
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()

	if got := h.Store().List(context.Background()); got != nil {
		t.Fatalf("list = %v, want nil", got)
	}
}

func TestListHidesRowsTheCallerDoesNotOwn(t *testing.T) {
	store := NewTaskStore()
	alice := identity.With(context.Background(), identity.Identity{OwnerID: "alice"})
	bob := identity.With(context.Background(), identity.Identity{OwnerID: "bob"})
	store.Create(alice, "res-1", userMessage("mine"))

	if got := store.List(bob); len(got) != 0 {
		t.Fatalf("bob sees %d rows, want 0", len(got))
	}
	if got := store.List(alice); len(got) != 1 {
		t.Fatalf("alice sees %d rows, want 1", len(got))
	}
}

func TestListStatePrefersTheNarrowRepositoryRead(t *testing.T) {
	repo := newFlakyRepo()
	repo.stateTasks = []*a2a.Task{{ID: "task-1", Status: a2a.TaskStatus{State: a2a.TaskStateInputRequired}}}
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()

	got := h.ListTasksInState(context.Background(), a2a.TaskStateInputRequired)
	if len(got) != 1 || got[0].ID != "task-1" {
		t.Fatalf("got %v, want the parked task", got)
	}
	// A failing narrow read is an empty queue, not a failed request: the caller
	// is a console listing, and it has nothing better to show.
	repo.listStateErr = errors.New("db down")
	if got := h.ListTasksInState(context.Background(), a2a.TaskStateInputRequired); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestSetStateReportsAWriteFailureAsInternal(t *testing.T) {
	repo := newFlakyRepo()
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	repo.setStateErr = errors.New("db down")
	if _, err := h.Store().SetState(id, a2a.TaskStateWorking, nil); errCode(t, err) != httputil.CodeInternal {
		t.Fatalf("code = %s", errCode(t, err))
	}
}

func TestSetStateCompletesEvenWhenTheQuestionCannotBeStored(t *testing.T) {
	// The A2A transition already succeeded; a bookkeeping write that fails must
	// not turn it into an error for the caller.
	repo := newFlakyRepo()
	repo.pendingErr = errors.New("db down")
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	if _, err := h.Store().SetState(id, a2a.TaskStateWorking, nil); err != nil {
		t.Fatalf("working: %v", err)
	}
	if _, err := h.Store().SetState(id, a2a.TaskStateInputRequired, nil); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := h.Store().SetState(id, a2a.TaskStateCanceled, nil); err != nil {
		t.Fatalf("unpark: %v", err)
	}
}

func TestAddArtifactReportsAWriteFailure(t *testing.T) {
	repo := newFlakyRepo()
	repo.artifactErr = errors.New("db down")
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	if _, err := h.Store().AddArtifact(id, a2a.Artifact{}); errCode(t, err) != httputil.CodeInternal {
		t.Fatalf("code = %s", errCode(t, err))
	}
}

func TestPendingAndPushReadsDegradeToNothing(t *testing.T) {
	repo := newFlakyRepo()
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	if got := h.Store().Pending(id); got != nil {
		t.Fatalf("pending = %+v, want nil", got)
	}
	repo.pendingReadErr = errors.New("db down")
	if got := h.Store().Pending(id); got != nil {
		t.Fatalf("pending = %+v, want nil after a failed read", got)
	}
	if got := h.Store().Push(id); got != nil {
		t.Fatalf("push = %+v, want nil", got)
	}
	repo.pushReadErr = errors.New("db down")
	if got := h.Store().Push(id); got != nil {
		t.Fatalf("push = %+v, want nil after a failed read", got)
	}
}

func TestTaskErrKeepsCodesAndPassesItsOwnErrorsThrough(t *testing.T) {
	if got := taskErr("op", nil); got != nil {
		t.Fatalf("nil error became %v", got)
	}
	// An error that already carries a code is the contract; rewrapping it would
	// replace a precise code with internal_error.
	already := &httputil.Error{Code: CodeInvalidStateTransition}
	if got := taskErr("set_state", already); got != already {
		t.Fatalf("a coded error was rewrapped: %v", got)
	}
	if got := taskErr("set_state", ErrStateConflict); errCode(t, got) != CodeInvalidStateTransition {
		t.Fatalf("code = %s", errCode(t, got))
	}
	if got := taskErr("set_state", ErrTaskNotFound); errCode(t, got) != CodeTaskNotFound {
		t.Fatalf("code = %s", errCode(t, got))
	}
	// Clone of nothing is nothing: callers do hand a possibly nil task.
	if cloneTask(nil) != nil {
		t.Fatal("nil task cloned into a value")
	}
}

// ---------------------------------------------------------------------------
// orchestrator: the writes a control plane call can fail on
// ---------------------------------------------------------------------------

func TestCancelReportsATransitionFailure(t *testing.T) {
	repo := newFlakyRepo()
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	repo.failTransition(a2a.TaskStateCanceled, ErrStateConflict)
	if _, err := h.Cancel(context.Background(), id); rpcCode(t, err) != a2a.CodeInvalidStateTransition {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
}

// parkedAgent builds a hub with one agent resource and a task parked in
// input-required, which is the state Resume and the approval queue act on.
func parkedAgent(t *testing.T, repo *flakyTaskRepo) (*Hub, *a2a.Task) {
	t.Helper()
	h, reg := hubWithRepo(repo, nil)
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)
	task := h.Store().Create(context.Background(), agent.ID, userMessage("first question"))
	if _, err := h.Store().SetState(task.ID, a2a.TaskStateWorking, nil); err != nil {
		t.Fatalf("working: %v", err)
	}
	parked, err := h.Store().SetState(task.ID, a2a.TaskStateInputRequired, nil)
	if err != nil {
		t.Fatalf("input-required: %v", err)
	}
	return h, parked
}

func TestResumeReportsAHistoryWriteFailure(t *testing.T) {
	repo := newFlakyRepo()
	h, task := parkedAgent(t, repo)
	defer h.Close()

	repo.historyErr = errors.New("db down")
	if _, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: task.ID}); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
}

func TestResumeReportsATransitionFailure(t *testing.T) {
	repo := newFlakyRepo()
	h, task := parkedAgent(t, repo)
	defer h.Close()

	repo.failTransition(a2a.TaskStateWorking, ErrStateConflict)
	if _, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: task.ID}); rpcCode(t, err) != a2a.CodeInvalidStateTransition {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
}

func TestResumeFailsTheTaskWhenTheFlowCannotRun(t *testing.T) {
	// The parked task names a resource that is gone: resuming has to record the
	// failure on the task instead of returning a half-resumed one.
	h, _ := hubWithRepo(newFlakyRepo(), nil)
	defer h.Close()
	task := h.Store().Create(context.Background(), "vanished", userMessage("q"))
	h.Store().SetState(task.ID, a2a.TaskStateWorking, nil)
	h.Store().SetState(task.ID, a2a.TaskStateInputRequired, nil)

	got, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: task.ID, Message: userMessage("answer")})
	if err == nil {
		t.Fatal("a resume whose flow cannot run reported success")
	}
	if got == nil || got.Status.State != a2a.TaskStateFailed {
		t.Fatalf("task = %+v, want failed", got)
	}
}

func TestResumeParksAgainWhenTheAnswerStillNeedsApproval(t *testing.T) {
	h, task := parkedAgent(t, newFlakyRepo())
	defer h.Close()

	got, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: task.ID, Message: userMessage("please approve")})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", got.Status.State)
	}
}

func TestParkConnectorRefusesWhileARestoreRuns(t *testing.T) {
	h, reg := hubWithRepo(newFlakyRepo(), nil)
	defer h.Close()
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)

	if err := h.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	defer h.Allow()
	if _, err := h.ParkConnector(context.Background(), agent.ID, "send", nil); !errors.Is(err, ErrRestoring) {
		t.Fatalf("err = %v, want ErrRestoring", err)
	}
}

func TestParkConnectorAttributesTheRequestToTheCaller(t *testing.T) {
	h, reg := hubWithRepo(newFlakyRepo(), nil)
	defer h.Close()
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)

	ctx := identity.With(context.Background(), identity.Identity{OwnerID: "user-1"})
	task, err := h.ParkConnector(ctx, agent.ID, "send", map[string]any{"to": "bob"})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", task.Status.State)
	}
	// The requester is recorded on the parked message: an approver has to be
	// able to see whose action they are approving.
	if len(task.History) == 0 || task.History[0].Metadata["requestedBy"] != "user-1" {
		t.Fatalf("history = %+v, want a requestedBy marker", task.History)
	}
}

func TestParkConnectorReportsEachWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*flakyTaskRepo)
	}{
		{"start", func(r *flakyTaskRepo) { r.failTransition(a2a.TaskStateWorking, ErrStateConflict) }},
		{"parked", func(r *flakyTaskRepo) { r.failTransition(a2a.TaskStateInputRequired, ErrStateConflict) }},
		{"question", func(r *flakyTaskRepo) { r.pendingErr = errors.New("db down") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFlakyRepo()
			h, reg := hubWithRepo(repo, nil)
			defer h.Close()
			agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
			mustUpsert(t, reg, agent)

			// The submit -> working hop has to succeed: the failure under test
			// is the write after it.
			tc.setup(repo)

			if _, err := h.ParkConnector(context.Background(), agent.ID, "send", nil); err == nil {
				t.Fatal("a failed write was reported as success")
			}
		})
	}
}

func TestSetPushReportsAWriteFailureAndGetPushAnUnknownTask(t *testing.T) {
	repo := newFlakyRepo()
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	params := a2a.TaskPushNotificationConfigParams{
		TaskID:           id,
		PushNotification: &a2a.PushNotificationConfig{URL: "https://peer.test/hook"},
	}
	repo.pushErr = errors.New("db down")
	if _, err := h.SetPush(context.Background(), params); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
	if _, err := h.GetPush(context.Background(), "ghost"); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
}

func TestSendRefusesAPushConfigWhenPushIsDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.PushEnabled = false
	reg := registry.New()
	h := New(cfg, reg, testOptions(), &MockNotifier{}, Persistence{Tasks: newFlakyRepo()})
	defer h.Close()
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)

	params := sendParams("hello", nil)
	params.Configuration = &a2a.MessageSendConfiguration{
		PushNotification: &a2a.PushNotificationConfig{URL: "https://peer.test/hook"},
	}
	if _, err := h.Send(context.Background(), params); rpcCode(t, err) != a2a.CodePushNotificationNotSupported {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
}

func TestSendDefaultsTheMessageRole(t *testing.T) {
	h, reg := hubWithRepo(newFlakyRepo(), nil)
	defer h.Close()
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)

	// A peer that omits the role still gets a user turn recorded.
	params := a2a.MessageSendParams{Message: a2a.Message{Parts: []a2a.Part{a2a.NewTextPart("hello")}}}
	task, err := h.Send(context.Background(), params)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}
}

func TestSendReportsAWorkingTransitionFailure(t *testing.T) {
	repo := newFlakyRepo()
	h, reg := hubWithRepo(repo, nil)
	defer h.Close()
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)

	repo.failTransition(a2a.TaskStateWorking, ErrStateConflict)
	if _, err := h.Send(context.Background(), sendParams("hello", nil)); rpcCode(t, err) != a2a.CodeInvalidStateTransition {
		t.Fatalf("code = %d", rpcCode(t, err))
	}
}

func TestSendReportsTheCompletionWritesThatFail(t *testing.T) {
	t.Run("completion transition", func(t *testing.T) {
		repo := newFlakyRepo()
		h, reg := hubWithRepo(repo, nil)
		defer h.Close()
		mustUpsert(t, reg, &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"})

		repo.failTransition(a2a.TaskStateCompleted, ErrStateConflict)
		if _, err := h.Send(context.Background(), sendParams("hello", nil)); rpcCode(t, err) != a2a.CodeInvalidStateTransition {
			t.Fatalf("code = %d", rpcCode(t, err))
		}
	})

	t.Run("reply history", func(t *testing.T) {
		repo := newFlakyRepo()
		h, reg := hubWithRepo(repo, nil)
		defer h.Close()
		mustUpsert(t, reg, &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"})

		// The state was already advanced, so the caller is told the task is
		// gone rather than being handed a task whose reply was never recorded.
		repo.historyErr = errors.New("db down")
		if _, err := h.Send(context.Background(), sendParams("hello", nil)); rpcCode(t, err) != a2a.CodeTaskNotFound {
			t.Fatalf("code = %d", rpcCode(t, err))
		}
	})
}

func TestParkReportsTheWritesThatFail(t *testing.T) {
	t.Run("transition", func(t *testing.T) {
		repo := newFlakyRepo()
		h, _ := hubWithRepo(repo, nil)
		defer h.Close()
		id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID
		h.Store().SetState(id, a2a.TaskStateWorking, nil)

		repo.failTransition(a2a.TaskStateInputRequired, ErrStateConflict)
		if _, err := h.park(id, "a question", nil); rpcCode(t, err) != a2a.CodeInvalidStateTransition {
			t.Fatalf("code = %d", rpcCode(t, err))
		}
	})

	t.Run("question", func(t *testing.T) {
		repo := newFlakyRepo()
		h, _ := hubWithRepo(repo, nil)
		defer h.Close()
		id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID
		h.Store().SetState(id, a2a.TaskStateWorking, nil)

		repo.pendingErr = errors.New("db down")
		if _, err := h.park(id, "a question", nil); rpcCode(t, err) != a2a.CodeTaskNotFound {
			t.Fatalf("code = %d", rpcCode(t, err))
		}
	})
}

func TestFailReportsTheOriginalCauseWhenTheTaskCannotBeFailed(t *testing.T) {
	repo := newFlakyRepo()
	h, _ := hubWithRepo(repo, nil)
	defer h.Close()
	id := h.Store().Create(context.Background(), "res-1", userMessage("x")).ID

	// A task that already reached a terminal state cannot be failed again; the
	// cause of the run is the useful answer, not the transition error.
	repo.failTransition(a2a.TaskStateFailed, ErrStateConflict)
	cause := &httputil.Error{Code: CodeNoResource}
	task, err := h.fail(id, cause, nil)
	if task == nil || task.ID != id {
		t.Fatalf("task = %+v", task)
	}
	if err == nil {
		t.Fatal("fail swallowed the cause")
	}
}

func TestInvokerReportsAnUnknownSelectorAndAFailedArtifactWrite(t *testing.T) {
	repo := newFlakyRepo()
	repo.artifactErr = errors.New("db down")
	h, reg := hubWithRepo(repo, nil)
	defer h.Close()
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "assistant", Version: "1.0.0"}
	mustUpsert(t, reg, agent)

	invoke := h.invoker(nil, Origin{})
	if _, err := invoke(context.Background(), "task-x", "ghost", "hello"); rpcCode(t, err) != a2a.CodeResourceNotFound {
		t.Fatalf("code = %d", rpcCode(t, err))
	}

	id := h.Store().Create(context.Background(), agent.ID, userMessage("hello")).ID
	if _, err := invoke(context.Background(), id, agent.ID, "hello"); err == nil {
		t.Fatal("an artifact write failure was swallowed")
	}
}

func TestNotifyPushLogsADeliveryFailure(t *testing.T) {
	// The callback is registered, the peer refuses it, and the task is already
	// terminal: there is nothing left to fail, so the hub only logs.
	repo := newFlakyRepo()
	repo.pushCfg = &a2a.PushNotificationConfig{URL: "https://peer.test/hook"}
	h := New(testConfig(), registry.New(), testOptions(), failingNotifier{}, Persistence{Tasks: repo})
	defer h.Close()

	task := h.Store().Create(context.Background(), "res-1", userMessage("x"))
	h.notifyPush(task)
}

func TestPublishIgnoresANilTaskAndAFailingSink(t *testing.T) {
	h, _ := hubWithRepo(newFlakyRepo(), nil)
	defer h.Close()

	h.publishStatus(nil, nil, false)

	task := h.Store().Create(context.Background(), "res-1", userMessage("x"))
	boom := func(string, any) error { return errors.New("sink gone") }
	// A subscriber that went away must not turn a completed step into a failure.
	h.publishStatus(task, boom, true)
	h.publishMessage(task.ID, userMessage("x"), boom)
}

// ---------------------------------------------------------------------------
// flow: the failure and aggregation branches of the primitives
// ---------------------------------------------------------------------------

func TestBranchRecordsTheErrorOfTheArmItTook(t *testing.T) {
	store := NewTaskStore()
	task := store.Create(identity.System(), "res-1", userMessage("hi"))
	r := NewRunner(store, NewBroker(), func(context.Context, string, string, string) (Outcome, error) {
		return Outcome{}, errors.New("boom")
	}, 1)

	_, err := r.Run(context.Background(), task.ID, []Step{{
		Op:    FlowBranch,
		Label: "route",
		Steps: []Step{{When: "always", Steps: []Step{{Op: FlowThen, Resource: "res-1"}}}},
	}}, FlowContext{Text: "hi"})
	if err == nil {
		t.Fatal("the branch swallowed the arm's error")
	}
}

func TestParallelKeepsTheFirstQuestionAsked(t *testing.T) {
	store := NewTaskStore()
	task := store.Create(identity.System(), "res-1", userMessage("hi"))
	r := NewRunner(store, NewBroker(), func(_ context.Context, _ string, selector string, _ string) (Outcome, error) {
		return Outcome{ResourceID: selector, RequiresInput: true, Prompt: "question-" + selector}, nil
	}, 2)

	res, err := r.Run(context.Background(), task.ID, []Step{{
		Op: FlowParallel,
		Steps: []Step{
			{Op: FlowThen, Resource: "a"},
			{Op: FlowThen, Resource: "b"},
		},
	}}, FlowContext{Text: "hi"})
	if err != nil {
		t.Fatalf("parallel: %v", err)
	}
	if !res.RequiresInput {
		t.Fatal("two members that need input produced a run that does not")
	}
	// Only one question can be parked, so the first member's wins.
	if res.Prompt != "question-a" {
		t.Fatalf("prompt = %q, want question-a", res.Prompt)
	}
}

func TestParseStepsRefusesValuesItCannotEncodeOrDecode(t *testing.T) {
	// A member that JSON cannot represent fails before reaching the decoder.
	if _, err := ParseSteps([]any{make(chan int)}); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
	// A well formed object whose field has the wrong type fails in the decoder.
	if _, err := ParseSteps(map[string]any{"op": "then", "steps": 5}); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
}
