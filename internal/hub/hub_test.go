package hub

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// testConfig builds a configuration for the in-memory hub. DatabaseURL is
// filled in so the struct is valid, but no repository is ever opened: the hub
// falls back to its in-memory stores when Persistence is zero valued.
func testConfig() *config.Config {
	return &config.Config{
		HTTPPort:        "3500",
		Env:             "dev",
		MaxParallel:     4,
		CLITimeout:      time.Second,
		CLIAllowlist:    []string{"echo", "ls"},
		MaxOutputBytes:  1024,
		EventBuffer:     64,
		PushEnabled:     true,
		DatabaseURL:     testDatabaseURL,
		PushRecordLimit: 100,
	}
}

// testDatabaseURL is a placeholder: unit tests never connect.
const testDatabaseURL = "postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable"

func testOptions() adapter.Options {
	return adapter.Options{CLITimeout: time.Second, CLIAllowlist: []string{"echo", "ls"}, MaxOutputBytes: 1024}
}

func newTestHub(t *testing.T, entries ...*registry.Entry) (*Hub, *registry.Registry, *MockNotifier) {
	t.Helper()
	reg := registry.New()
	for _, e := range entries {
		stored, err := reg.Upsert(e)
		if err != nil {
			t.Fatalf("seed entry %q: %v", e.Name, err)
		}
		// Mirror the generated id back so tests can reference e.ID directly.
		*e = *stored
	}
	notifier := &MockNotifier{}
	return New(testConfig(), reg, testOptions(), notifier, Persistence{}), reg, notifier
}

func userMessage(text string) a2a.Message {
	return a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(text))
}

func sendParams(text string, metadata map[string]any) a2a.MessageSendParams {
	return a2a.MessageSendParams{Message: userMessage(text), Metadata: metadata}
}

func rpcCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var e *a2a.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *a2a.Error, got %T (%v)", err, err)
	}
	return e.Code
}

func errCode(t *testing.T, err error) httputil.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return httputil.AsError(err).Code
}

// ---------------------------------------------------------------------------
// state machine + store
// ---------------------------------------------------------------------------

func TestCanTransition(t *testing.T) {
	legal := map[a2a.TaskState][]a2a.TaskState{
		a2a.TaskStateSubmitted: {a2a.TaskStateWorking, a2a.TaskStateFailed, a2a.TaskStateCanceled},
		a2a.TaskStateWorking:   {a2a.TaskStateInputRequired, a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateCanceled},
		a2a.TaskStateInputRequired: {
			a2a.TaskStateWorking, a2a.TaskStateFailed, a2a.TaskStateCanceled,
		},
	}
	for from, tos := range legal {
		for _, to := range tos {
			if !CanTransition(from, to) {
				t.Errorf("expected %s -> %s to be legal", from, to)
			}
		}
	}

	illegal := []struct{ from, to a2a.TaskState }{
		{a2a.TaskStateSubmitted, a2a.TaskStateSubmitted},
		{a2a.TaskStateSubmitted, a2a.TaskStateCompleted},
		{a2a.TaskStateSubmitted, a2a.TaskStateInputRequired},
		{a2a.TaskStateWorking, a2a.TaskStateSubmitted},
		{a2a.TaskStateInputRequired, a2a.TaskStateCompleted},
		{a2a.TaskStateCompleted, a2a.TaskStateWorking},
		{a2a.TaskStateFailed, a2a.TaskStateWorking},
		{a2a.TaskStateCanceled, a2a.TaskStateWorking},
		{a2a.TaskStateUnknown, a2a.TaskStateWorking},
		{a2a.TaskStateWorking, a2a.TaskStateUnknown},
		{a2a.TaskStateWorking, "bogus"},
		{"bogus", a2a.TaskStateWorking},
	}
	for _, c := range illegal {
		if CanTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be illegal", c.from, c.to)
		}
	}
}

func TestTaskStoreLifecycleAndErrors(t *testing.T) {
	store := NewTaskStore()
	task := store.Create("res-1", userMessage("hello"))
	if task.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("new task state = %s, want submitted", task.Status.State)
	}
	if store.Get(task.ID) == nil {
		t.Fatal("expected the task to be retrievable")
	}

	if _, err := store.SetState(task.ID, a2a.TaskStateWorking, nil); err != nil {
		t.Fatalf("submitted -> working: %v", err)
	}
	if _, err := store.SetState(task.ID, a2a.TaskStateSubmitted, nil); err == nil {
		t.Fatal("working -> submitted must be rejected")
	} else if errCode(t, err) != CodeInvalidStateTransition {
		t.Fatalf("code = %s, want %s", errCode(t, err), CodeInvalidStateTransition)
	}

	unknown := "task_missing"
	_, errSetState := store.SetState(unknown, a2a.TaskStateWorking, nil)
	_, errArtifact := store.AddArtifact(unknown, a2a.Artifact{Name: "x"})
	errHistory := store.AppendHistory(unknown, userMessage("x"))
	errPending := store.SetPending(unknown, "q")
	errPush := store.SetPush(unknown, &a2a.PushNotificationConfig{URL: "http://x"})
	for name, err := range map[string]error{
		"setState":   errSetState,
		"artifact":   errArtifact,
		"history":    errHistory,
		"setPending": errPending,
		"setPush":    errPush,
	} {
		if err == nil {
			t.Fatalf("%s: expected an error for an unknown task", name)
		}
		if errCode(t, err) != CodeTaskNotFound {
			t.Fatalf("%s: code = %s, want %s", name, errCode(t, err), CodeTaskNotFound)
		}
	}
	if store.Get(unknown) != nil || store.Pending(unknown) != nil || store.Push(unknown) != nil || store.Spans(unknown) != nil {
		t.Fatal("unknown tasks must read back as empty")
	}

	// Artifacts and history accumulate.
	if _, err := store.AddArtifact(task.ID, a2a.Artifact{Name: "a", Parts: []a2a.Part{a2a.NewTextPart("x")}}); err != nil {
		t.Fatalf("add artifact: %v", err)
	}
	if err := store.AppendHistory(task.ID, a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("y"))); err != nil {
		t.Fatalf("append history: %v", err)
	}
	got := store.Get(task.ID)
	if len(got.Artifacts) != 1 || len(got.History) != 2 {
		t.Fatalf("artifacts=%d history=%d, want 1 and 2", len(got.Artifacts), len(got.History))
	}

	// Reading a task returns a copy: mutating it must not corrupt the store.
	got.Artifacts = append(got.Artifacts, a2a.Artifact{Name: "injected"})
	if len(store.Get(task.ID).Artifacts) != 1 {
		t.Fatal("store returned a shared slice instead of a copy")
	}

	// input-required parks a pending question; leaving it clears the question.
	if _, err := store.SetState(task.ID, a2a.TaskStateInputRequired, nil); err != nil {
		t.Fatalf("working -> input-required: %v", err)
	}
	if err := store.SetPending(task.ID, "approval_required:res-1"); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	if p := store.Pending(task.ID); p == nil || !strings.HasPrefix(p.Prompt, "approval_required") {
		t.Fatalf("pending = %+v, want the approval prompt", p)
	}
	// input-required can only be left through working (or a terminal failure).
	if _, err := store.SetState(task.ID, a2a.TaskStateCompleted, nil); err == nil {
		t.Fatal("input-required -> completed must be rejected")
	}
	if _, err := store.SetState(task.ID, a2a.TaskStateWorking, nil); err != nil {
		t.Fatalf("input-required -> working: %v", err)
	}
	if store.Pending(task.ID) != nil {
		t.Fatal("leaving input-required must clear the pending question")
	}
	if _, err := store.SetState(task.ID, a2a.TaskStateCompleted, nil); err != nil {
		t.Fatalf("working -> completed: %v", err)
	}
	if got := store.Get(task.ID); got.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", got.Status.State)
	}

	// Push config round trip.
	cfg := &a2a.PushNotificationConfig{URL: "http://callback", Token: "t"}
	if err := store.SetPush(task.ID, cfg); err != nil {
		t.Fatalf("set push: %v", err)
	}
	if got := store.Push(task.ID); got == nil || got.URL != cfg.URL {
		t.Fatalf("push = %+v, want %+v", got, cfg)
	}
	if len(store.List()) != 1 {
		t.Fatalf("list = %d tasks, want 1", len(store.List()))
	}
}

// ---------------------------------------------------------------------------
// spans
// ---------------------------------------------------------------------------

func TestSpansRecordTimeline(t *testing.T) {
	store := NewTaskStore()
	task := store.Create("res-1", userMessage("hello"))

	span := store.StartSpan(task.ID, "res-1", string(registry.KindModel), "invoke:res-1")
	if span.ID == "" || span.TaskID != task.ID || span.Status != SpanStatusOK {
		t.Fatalf("span = %+v", span)
	}
	if len(store.Spans(task.ID)) != 0 {
		t.Fatal("a started span must not be visible before it finishes")
	}

	finished := store.FinishSpan(span, SpanStatusError, "adapter_timeout")
	if finished.Status != SpanStatusError || finished.Code != "adapter_timeout" {
		t.Fatalf("finished = %+v", finished)
	}
	if finished.EndAt == "" || finished.DurationMS < 0 {
		t.Fatalf("finished span has no duration: %+v", finished)
	}

	spans := store.Spans(task.ID)
	if len(spans) != 1 || spans[0].ID != span.ID {
		t.Fatalf("spans = %+v, want one span", spans)
	}
	// The returned span is a copy: mutating it must not rewrite the timeline.
	spans[0].Status = "tampered"
	if store.Spans(task.ID)[0].Status != SpanStatusError {
		t.Fatal("Spans must return copies")
	}

	// A span for an unknown task is dropped instead of panicking.
	orphan := store.StartSpan("task_missing", "", "", "orphan")
	store.FinishSpan(orphan, SpanStatusOK, "")
	if store.Spans("task_missing") != nil {
		t.Fatal("spans of unknown tasks must be dropped")
	}
}

// ---------------------------------------------------------------------------
// broker
// ---------------------------------------------------------------------------

func TestBrokerFanOutAndUnsubscribe(t *testing.T) {
	b := NewBroker()
	ch1, cancel1 := b.Subscribe("task-1", 4)
	ch2, _ := b.Subscribe("task-1", 4)
	if b.SubscriberCount("task-1") != 2 {
		t.Fatalf("subscribers = %d, want 2", b.SubscriberCount("task-1"))
	}

	b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus, At: "now"})
	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Kind != EventKindStatus || ev.Seq == 0 && ev.At != "now" {
				t.Fatalf("subscriber %d got %+v", i, ev)
			}
		default:
			t.Fatalf("subscriber %d received nothing", i)
		}
	}

	// Publishing to another task must not leak.
	b.Publish(Event{TaskID: "task-2", Kind: EventKindMessage})
	select {
	case ev := <-ch1:
		t.Fatalf("cross-task delivery: %+v", ev)
	default:
	}

	cancel1()
	cancel1() // idempotent
	if b.SubscriberCount("task-1") != 1 {
		t.Fatalf("after cancel subscribers = %d, want 1", b.SubscriberCount("task-1"))
	}
	if _, ok := <-ch1; ok {
		t.Fatal("cancelled subscriber channel must be closed")
	}
	// The remaining subscriber keeps working.
	b.Publish(Event{TaskID: "task-1", Kind: EventKindSpan})
	select {
	case ev := <-ch2:
		if ev.Kind != EventKindSpan {
			t.Fatalf("remaining subscriber got %+v", ev)
		}
	default:
		t.Fatal("remaining subscriber received nothing")
	}
}

func TestBrokerDropsOldestForSlowConsumers(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe("task-1", 1)
	defer cancel()

	for i := 0; i < 5; i++ {
		b.Publish(Event{TaskID: "task-1", Kind: EventKindStatus, Seq: uint64(i)})
	}
	ev := <-ch
	if ev.Seq != 5 {
		t.Fatalf("slow consumer got seq %d, want the newest frame (5)", ev.Seq)
	}
	select {
	case extra := <-ch:
		t.Fatalf("unexpected extra frame %+v", extra)
	default:
	}
}

// ---------------------------------------------------------------------------
// flow primitives
// ---------------------------------------------------------------------------

func TestMatchCondition(t *testing.T) {
	cases := []struct {
		when string
		fc   FlowContext
		want bool
	}{
		{"", FlowContext{Text: "x"}, true},
		{"always", FlowContext{}, true},
		{"true", FlowContext{}, true},
		{"contains:HELLO", FlowContext{Text: "say hello world"}, true},
		{"contains:bye", FlowContext{Text: "say hello"}, false},
		{"kind:model", FlowContext{Kind: "model"}, true},
		{"kind:cli", FlowContext{Kind: "model"}, false},
		{"has:answer", FlowContext{Keys: map[string]string{"answer": "42"}}, true},
		{"has:answer", FlowContext{Keys: map[string]string{"other": "1"}}, false},
		{"has:answer", FlowContext{}, false},
		{"unknown:thing", FlowContext{Text: "x"}, false},
	}
	for _, c := range cases {
		if got := MatchCondition(c.when, c.fc); got != c.want {
			t.Errorf("MatchCondition(%q) = %v, want %v", c.when, got, c.want)
		}
	}
}

func TestParseSteps(t *testing.T) {
	steps, err := ParseSteps([]any{
		map[string]any{"op": "then", "resource": "r1"},
		map[string]any{"op": "parallel", "steps": []any{map[string]any{"op": "then", "resource": "r2"}}},
	})
	if err != nil {
		t.Fatalf("parse []any: %v", err)
	}
	if len(steps) != 2 || steps[0].Resource != "r1" || len(steps[1].Steps) != 1 {
		t.Fatalf("steps = %+v", steps)
	}

	if steps, err = ParseSteps(`[{"op":"branch","when":"kind:cli"}]`); err != nil {
		t.Fatalf("parse string: %v", err)
	} else if len(steps) != 1 || steps[0].When != "kind:cli" {
		t.Fatalf("steps = %+v", steps)
	}

	if steps, err = ParseSteps(map[string]any{"op": "then", "resource": "r3"}); err != nil {
		t.Fatalf("parse object: %v", err)
	} else if len(steps) != 1 || steps[0].Resource != "r3" {
		t.Fatalf("steps = %+v", steps)
	}

	if steps, err = ParseSteps(nil); err != nil || steps != nil {
		t.Fatalf("nil flow = %+v, %v", steps, err)
	}

	for _, bad := range []any{42, "not json", []any{"nope"}} {
		if _, err := ParseSteps(bad); err == nil {
			t.Fatalf("ParseSteps(%v) must fail", bad)
		} else if errCode(t, err) != CodeInvalidFlow {
			t.Fatalf("code = %s, want %s", errCode(t, err), CodeInvalidFlow)
		}
	}
}

func TestRunnerThenBranchParallel(t *testing.T) {
	store := NewTaskStore()
	task := store.Create("res-1", userMessage("hello"))

	var calls []string
	var mu sync.Mutex
	invoker := func(ctx context.Context, taskID string, selector string, prompt string) (Outcome, error) {
		mu.Lock()
		calls = append(calls, selector)
		mu.Unlock()
		return Outcome{ResourceID: selector, Kind: "mock", Label: selector, Text: "out:" + selector}, nil
	}
	r := NewRunner(store, nil, invoker, 4)

	steps := []Step{
		{Op: FlowThen, Resource: "a"},
		{Op: FlowBranch, Label: "route", Steps: []Step{
			{When: "contains:nope", Steps: []Step{{Op: FlowThen, Resource: "skipped"}}},
			{When: "contains:out:a", Steps: []Step{{Op: FlowThen, Resource: "b"}}},
		}},
		{Op: FlowParallel, Label: "fan-out", Steps: []Step{
			{Op: FlowThen, Resource: "c"},
			{Op: FlowThen, Resource: "d"},
		}},
	}
	res, err := r.Run(context.Background(), task.ID, steps, FlowContext{Text: "hello"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"a", "b", "c", "d"} {
		if !contains(calls, want) {
			t.Fatalf("expected %q to be invoked, got %v", want, calls)
		}
	}
	if contains(calls, "skipped") {
		t.Fatalf("the non matching arm must not run: %v", calls)
	}
	if len(res.Outcomes) != 4 {
		t.Fatalf("outcomes = %d, want 4", len(res.Outcomes))
	}
	if !strings.Contains(res.Text, "out:b") {
		t.Fatalf("text = %q, want the branch output", res.Text)
	}

	// then:a, branch:route, then:b (inside the matched arm), parallel:fan-out
	// and one span per parallel member (c, d).
	spans := store.Spans(task.ID)
	if len(spans) != 6 {
		t.Fatalf("spans = %d, want 6: %+v", len(spans), spans)
	}

	// A branch with no matching arm produces no outcome and no error.
	empty := NewRunner(store, nil, invoker, 4)
	res, err = empty.Run(context.Background(), task.ID, []Step{
		{Op: FlowBranch, Steps: []Step{{When: "contains:never"}}},
	}, FlowContext{Text: "hello"})
	if err != nil || len(res.Outcomes) != 0 {
		t.Fatalf("unmatched branch = %+v, %v", res, err)
	}

	// Unknown ops are rejected.
	if _, err := empty.Run(context.Background(), task.ID, []Step{{Op: "loop"}}, FlowContext{}); err == nil {
		t.Fatal("unknown op must fail")
	} else if errCode(t, err) != CodeInvalidFlow {
		t.Fatalf("code = %s, want %s", errCode(t, err), CodeInvalidFlow)
	}
}

func TestRunnerParallelRespectsMaxParallelAndPropagatesErrors(t *testing.T) {
	store := NewTaskStore()
	task := store.Create("res-1", userMessage("hello"))

	var mu sync.Mutex
	calls := map[string]int{}
	invoker := func(ctx context.Context, taskID string, selector string, prompt string) (Outcome, error) {
		mu.Lock()
		calls[selector]++
		mu.Unlock()
		if selector == "boom" {
			return Outcome{}, &httputil.Error{Code: "resource_unreachable"}
		}
		return Outcome{ResourceID: selector, Text: "out:" + selector}, nil
	}

	r := NewRunner(store, nil, invoker, 2)
	if _, err := r.Run(context.Background(), task.ID, []Step{{Op: FlowParallel, Steps: []Step{
		{Op: FlowThen, Resource: "a"},
		{Op: FlowThen, Resource: "b"},
		{Op: FlowThen, Resource: "c"},
	}}}, FlowContext{Text: "hello"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	got := len(calls)
	mu.Unlock()
	if got != 2 {
		t.Fatalf("calls = %d, want only maxParallel members (2)", got)
	}

	if _, err := r.Run(context.Background(), task.ID, []Step{{Op: FlowParallel, Steps: []Step{
		{Op: FlowThen, Resource: "a"},
		{Op: FlowThen, Resource: "boom"},
	}}}, FlowContext{}); err == nil {
		t.Fatal("a failing member must surface its error")
	} else if errCode(t, err) != "resource_unreachable" {
		t.Fatalf("code = %s, want resource_unreachable", errCode(t, err))
	}

	spans := store.Spans(task.ID)
	var errored, coded int
	for _, s := range spans {
		if s.Status == SpanStatusError {
			errored++
			if s.Code == "resource_unreachable" {
				coded++
			}
		}
	}
	if errored == 0 || coded == 0 {
		t.Fatalf("expected an errored span carrying the adapter code, got %+v", spans)
	}
}

func TestRunnerStopsAtInputRequired(t *testing.T) {
	store := NewTaskStore()
	task := store.Create("res-1", userMessage("hello"))

	var mu sync.Mutex
	calls := 0
	invoker := func(ctx context.Context, taskID string, selector string, prompt string) (Outcome, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		if selector == "needs-input" {
			return Outcome{ResourceID: selector, Text: "waiting", RequiresInput: true, Prompt: "approval_required:" + selector}, nil
		}
		return Outcome{ResourceID: selector, Text: "out:" + selector}, nil
	}
	r := NewRunner(store, nil, invoker, 4)
	res, err := r.Run(context.Background(), task.ID, []Step{
		{Op: FlowThen, Resource: "needs-input"},
		{Op: FlowThen, Resource: "never"},
	}, FlowContext{Text: "hello"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.RequiresInput || res.Prompt != "approval_required:needs-input" {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("invocations = %d, want 1 (the flow must stop at input-required)", got)
	}
	var parked bool
	for _, s := range store.Spans(task.ID) {
		if s.Status == SpanStatusInputRequired {
			parked = true
		}
	}
	if !parked {
		t.Fatalf("expected an input-required span, got %+v", store.Spans(task.ID))
	}
}

func TestDefaultSteps(t *testing.T) {
	steps := DefaultSteps("res-1")
	if len(steps) != 1 || steps[0].Op != FlowThen || steps[0].Resource != "res-1" {
		t.Fatalf("steps = %+v", steps)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// orchestrator
// ---------------------------------------------------------------------------

func TestHubSendCompletesAndAccumulatesArtifacts(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill", Version: "1.0.0", Capabilities: []string{"search"}}
	h, _, _ := newTestHub(t, skill)

	var id string
	for _, e := range h.Registry().List("", "") {
		id = e.ID
	}
	task, err := h.Send(context.Background(), sendParams("run the manual", map[string]any{"resourceId": id}))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) == 0 {
		t.Fatal("expected at least one artifact")
	}
	if len(task.History) != 2 || task.History[1].Role != a2a.RoleAgent {
		t.Fatalf("history = %+v, want user + agent", task.History)
	}
	if len(h.Store().Spans(task.ID)) == 0 {
		t.Fatal("expected a span timeline")
	}
}

func TestHubSendRejectsBadInputAndMissingResources(t *testing.T) {
	h, _, _ := newTestHub(t) // empty registry

	if _, err := h.Send(context.Background(), a2a.MessageSendParams{Message: a2a.Message{Role: a2a.RoleUser}}); rpcCode(t, err) != a2a.CodeInvalidParams {
		t.Fatalf("code = %d, want invalid_params", rpcCode(t, err))
	}
	if _, err := h.Send(context.Background(), sendParams("hello", nil)); rpcCode(t, err) != a2a.CodeResourceNotFound {
		t.Fatalf("code = %d, want resource_not_found", rpcCode(t, err))
	}
	if _, err := h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": "nope"})); rpcCode(t, err) != a2a.CodeResourceNotFound {
		t.Fatalf("code = %d, want resource_not_found", rpcCode(t, err))
	}
}

func TestHubSelectsByKindAndFallsBackToAgent(t *testing.T) {
	model := &registry.Entry{Kind: registry.KindModel, Name: "demo-model"}
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "demo-agent"}
	h, _, _ := newTestHub(t, model, agent)

	// No selector at all: an agent is the natural entry point.
	task, err := h.Send(context.Background(), sendParams("hello", nil))
	if err != nil {
		t.Fatalf("send without selector: %v", err)
	}
	if task.ResourceID != agent.ID {
		t.Fatalf("resource = %s, want the agent (%s)", task.ResourceID, agent.ID)
	}

	// An explicit resource id wins over the kind hint.
	task, err = h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": model.ID, "kind": "cli"}))
	if err != nil {
		t.Fatalf("send by id: %v", err)
	}
	if task.ResourceID != model.ID {
		t.Fatalf("resource = %s, want %s", task.ResourceID, model.ID)
	}

	// The kind hint is only a fallback, used when the selector does not resolve.
	task, err = h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": "nope", "kind": "model"}))
	if err != nil {
		t.Fatalf("send by kind fallback: %v", err)
	}
	if task.ResourceID != model.ID {
		t.Fatalf("resource = %s, want %s", task.ResourceID, model.ID)
	}
}

func TestHubParksForApprovalAndResumes(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "demo-agent"}
	h, _, _ := newTestHub(t, agent)

	task, err := h.Send(context.Background(), sendParams("please approve the transfer", map[string]any{"resourceId": agent.ID}))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", task.Status.State)
	}
	pending := h.Store().Pending(task.ID)
	if pending == nil || !strings.HasPrefix(pending.Prompt, "approval_required:") {
		t.Fatalf("pending = %+v, want an approval prompt", pending)
	}

	// A completed task cannot be resumed.
	other := h.Store().Create(agent.ID, userMessage("x"))
	if _, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: other.ID}); rpcCode(t, err) != a2a.CodeTaskNotResumable {
		t.Fatalf("code = %d, want task_not_resumable", rpcCode(t, err))
	}
	if _, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: "task_missing"}); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}

	// The answer itself must not re-trigger the approval prompt.
	resumed, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: task.ID, Message: userMessage("approved")})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state after resume = %s, want completed", resumed.Status.State)
	}
	if h.Store().Pending(task.ID) != nil {
		t.Fatal("resuming must clear the pending question")
	}
	if len(resumed.History) < 2 {
		t.Fatalf("history = %+v, want the answer recorded", resumed.History)
	}
}

func TestHubResumeWithEmptyAnswerDefaultsToApproved(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "demo-agent"}
	h, _, _ := newTestHub(t, agent)

	task, err := h.Send(context.Background(), sendParams("please approve this", map[string]any{"resourceId": agent.ID}))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	resumed, err := h.Resume(context.Background(), a2a.TaskResumeParams{ID: task.ID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", resumed.Status.State)
	}
}

func TestHubCancel(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill"}
	h, _, _ := newTestHub(t, skill)

	task := h.Store().Create(skill.ID, userMessage("hello"))
	canceled, err := h.Cancel(task.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("state = %s, want canceled", canceled.Status.State)
	}
	if _, err := h.Cancel(task.ID); rpcCode(t, err) != a2a.CodeTaskNotCancelable {
		t.Fatalf("code = %d, want task_not_cancelable", rpcCode(t, err))
	}
	if _, err := h.Cancel("task_missing"); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}
}

func TestHubGetTrimsHistory(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill"}
	h, _, _ := newTestHub(t, skill)

	if _, err := h.Get("task_missing", nil); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}

	task, err := h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": skill.ID}))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	full, err := h.Get(task.ID, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(full.History) != 2 {
		t.Fatalf("history = %d, want 2", len(full.History))
	}
	one := 1
	trimmed, err := h.Get(task.ID, &one)
	if err != nil {
		t.Fatalf("get trimmed: %v", err)
	}
	if len(trimmed.History) != 1 || trimmed.History[0].Role != a2a.RoleAgent {
		t.Fatalf("trimmed history = %+v, want the newest message only", trimmed.History)
	}
}

func TestHubStreamEmitsStatusArtifactAndTerminalFrames(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill"}
	h, _, _ := newTestHub(t, skill)

	var mu sync.Mutex
	events := map[string]int{}
	emit := func(event string, payload any) error {
		mu.Lock()
		events[event]++
		mu.Unlock()
		if frame, ok := payload.(a2a.TaskStatusUpdateEvent); ok {
			mu.Lock()
			events["final:"+string(frame.Status.State)]++
			mu.Unlock()
		}
		return nil
	}
	task, err := h.Stream(context.Background(), sendParams("hello", map[string]any{"resourceId": skill.ID}), emit)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if events[a2a.EventTaskArtifact] == 0 {
		t.Fatalf("no artifact frame emitted: %v", events)
	}
	if events["final:completed"] == 0 {
		t.Fatalf("no terminal status frame emitted: %v", events)
	}
}

func TestHubBrokerReceivesRunEvents(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill"}
	h, _, _ := newTestHub(t, skill)

	task, err := h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": skill.ID}))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Subscribing after the run replays nothing, but must not block or panic.
	late, stop := h.Broker().Subscribe(task.ID, 8)
	defer stop()
	select {
	case ev := <-late:
		t.Fatalf("no replay expected, got %+v", ev)
	default:
	}
}

func TestHubConcurrentSends(t *testing.T) {
	entries := []*registry.Entry{
		{Kind: registry.KindModel, Name: "demo-model"},
		{Kind: registry.KindAgent, Name: "demo-agent"},
		{Kind: registry.KindMCP, Name: "demo-mcp"},
		{Kind: registry.KindSkill, Name: "demo-skill"},
		{Kind: registry.KindCLI, Name: "demo-cli", Metadata: map[string]string{"command": "echo"}},
	}
	h, _, _ := newTestHub(t, entries...)

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kind := string(entries[i%len(entries)].Kind)
			task, err := h.Send(context.Background(), sendParams("hello "+kind, map[string]any{"kind": kind}))
			if err != nil {
				errs <- err
				return
			}
			if task.Status.State != a2a.TaskStateCompleted {
				errs <- errors.New("unexpected state " + string(task.Status.State))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent send failed: %v", err)
	}
	if got := len(h.Store().List()); got != 16 {
		t.Fatalf("tasks = %d, want 16", got)
	}
}

func TestHubFlowBranchAndParallel(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill"}
	model := &registry.Entry{Kind: registry.KindModel, Name: "demo-model"}
	cli := &registry.Entry{Kind: registry.KindCLI, Name: "demo-cli", Metadata: map[string]string{"command": "echo"}}
	h, _, _ := newTestHub(t, skill, model, cli)

	flow := []any{
		map[string]any{"op": "branch", "label": "route", "steps": []any{
			map[string]any{"when": "contains:nope", "steps": []any{map[string]any{"op": "then", "resource": cli.ID}}},
			map[string]any{"when": "contains:hello", "steps": []any{map[string]any{"op": "then", "resource": skill.ID}}},
		}},
		map[string]any{"op": "parallel", "label": "fan-out", "steps": []any{
			map[string]any{"op": "then", "resource": model.ID},
			map[string]any{"op": "then", "resource": cli.ID},
		}},
	}
	task, err := h.Send(context.Background(), sendParams("hello", map[string]any{
		"resourceId": skill.ID,
		"flow":       flow,
	}))
	if err != nil {
		t.Fatalf("send with flow: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) < 3 {
		t.Fatalf("artifacts = %d, want the branch plus both parallel members", len(task.Artifacts))
	}
	if len(h.Store().Spans(task.ID)) < 4 {
		t.Fatalf("spans = %+v, want branch + member spans", h.Store().Spans(task.ID))
	}

	// A malformed flow fails the task instead of silently running the default.
	broken, err := h.Send(context.Background(), sendParams("hello", map[string]any{
		"resourceId": skill.ID,
		"flow":       "not-json",
	}))
	if err == nil {
		t.Fatal("a malformed flow must fail")
	}
	if broken == nil || broken.Status.State != a2a.TaskStateFailed {
		t.Fatalf("state = %+v, want failed", broken)
	}
}

func TestHubPushNotifications(t *testing.T) {
	skill := &registry.Entry{Kind: registry.KindSkill, Name: "demo-skill"}
	h, _, notifier := newTestHub(t, skill)

	task := h.Store().Create(skill.ID, userMessage("hello"))

	if _, err := h.SetPush(a2a.TaskPushNotificationConfigParams{TaskID: task.ID}); rpcCode(t, err) != a2a.CodeInvalidParams {
		t.Fatalf("code = %d, want invalid_params", rpcCode(t, err))
	}
	if _, err := h.SetPush(a2a.TaskPushNotificationConfigParams{TaskID: "task_missing", PushNotification: &a2a.PushNotificationConfig{URL: "http://x"}}); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}
	if _, err := h.GetPush(task.ID); rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}

	cfg, err := h.SetPush(a2a.TaskPushNotificationConfigParams{TaskID: task.ID, PushNotificationAlias: &a2a.PushNotificationConfig{URL: "http://callback", Token: "t"}})
	if err != nil {
		t.Fatalf("set push: %v", err)
	}
	if cfg.URL != "http://callback" {
		t.Fatalf("url = %q", cfg.URL)
	}
	if got, err := h.GetPush(task.ID); err != nil || got.URL != "http://callback" {
		t.Fatalf("get push = %+v, %v", got, err)
	}

	if _, err := h.Cancel(task.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	records := notifier.Records()
	if len(records) != 1 || records[0].TaskID != task.ID || records[0].State != a2a.TaskStateCanceled {
		t.Fatalf("push records = %+v", records)
	}

	// Push disabled is reported as unsupported instead of silently dropping.
	h.cfg.PushEnabled = false
	if _, err := h.SetPush(a2a.TaskPushNotificationConfigParams{TaskID: task.ID, PushNotification: &a2a.PushNotificationConfig{URL: "http://x"}}); rpcCode(t, err) != a2a.CodePushNotificationNotSupported {
		t.Fatalf("code = %d, want push_notification_not_supported", rpcCode(t, err))
	}
}

func TestMockNotifierHonoursContext(t *testing.T) {
	n := &MockNotifier{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Notify(ctx, a2a.PushNotificationConfig{URL: "http://x"}, "task-1", a2a.TaskStateCompleted); err == nil {
		t.Fatal("a cancelled context must not record a callback")
	}
	if len(n.Records()) != 0 {
		t.Fatal("no records expected")
	}
}

func TestAdapterErrorMapping(t *testing.T) {
	mapped := adapterError(&httputil.Error{Code: "resource_unreachable"})
	var e *a2a.Error
	if !errors.As(mapped, &e) || e.Code != a2a.CodeResourceUnreachable {
		t.Fatalf("mapped = %+v", mapped)
	}
	if data, ok := e.Data.(map[string]string); !ok || data["code"] != "resource_unreachable" {
		t.Fatalf("data = %+v", e.Data)
	}
	// An RPC error passes through untouched.
	original := a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	if adapterError(original) != error(original) {
		t.Fatal("rpc errors must pass through")
	}
}

func TestInvocationRecordsTheHealthItProved(t *testing.T) {
	model := &registry.Entry{Kind: registry.KindModel, Name: "demo-model"}
	// A card that nothing serves: the call fails with resource_unreachable,
	// which is exactly the observation the record has to show.
	down := &registry.Entry{
		Kind: registry.KindAgent,
		Name: "down-agent",
		KindConfig: &registry.KindConfig{
			Agent: &registry.AgentConfig{CardURL: "http://127.0.0.1:1/.well-known/agent-card.json"},
		},
	}
	h, reg, _ := newTestHub(t, model, down)

	healthOf := func(id string) registry.Health {
		t.Helper()
		entry, ok := reg.Get(id)
		if !ok {
			t.Fatalf("entry %s vanished", id)
		}
		return entry.Health
	}

	// Both start unknown: the catalogue no longer declares its own health.
	if got := healthOf(model.ID); got != registry.HealthUnknown {
		t.Fatalf("model initial health = %q, want unknown", got)
	}
	if got := healthOf(down.ID); got != registry.HealthUnknown {
		t.Fatalf("agent initial health = %q, want unknown", got)
	}

	if _, err := h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": model.ID})); err != nil {
		t.Fatalf("send to the model: %v", err)
	}
	if got := healthOf(model.ID); got != registry.HealthHealthy {
		t.Fatalf("model health after a successful call = %q, want healthy", got)
	}

	if _, err := h.Send(context.Background(), sendParams("hello", map[string]any{"resourceId": down.ID})); err == nil {
		t.Fatal("send to the unreachable agent: want an error")
	}
	if got := healthOf(down.ID); got != registry.HealthUnhealthy {
		t.Fatalf("agent health after a failed call = %q, want unhealthy", got)
	}
}
