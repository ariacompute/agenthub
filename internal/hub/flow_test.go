package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

func TestRunnerThen(t *testing.T) {
	store := NewTaskStore()
	calls := 0
	inv := func(ctx context.Context, taskID, selector, prompt string) (Outcome, error) {
		calls++
		return Outcome{ResourceID: selector, Text: "out:" + selector, Label: selector}, nil
	}
	r := NewRunner(store, nil, inv, 2)
	res, err := r.Run(context.Background(), "t1", DefaultSteps("r1"), FlowContext{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 || res.Text != "out:r1" || len(res.Outcomes) != 1 {
		t.Fatalf("Run then = %+v, calls=%d", res, calls)
	}
}

func TestRunnerBranch(t *testing.T) {
	store := NewTaskStore()
	inv := func(ctx context.Context, taskID, selector, prompt string) (Outcome, error) {
		return Outcome{ResourceID: selector, Text: "out:" + selector}, nil
	}
	r := NewRunner(store, nil, inv, 2)

	steps := []Step{{
		Op: FlowBranch,
		Steps: []Step{
			{When: "contains:hello", Steps: []Step{{Op: FlowThen, Resource: "a"}}},
			{When: "", Steps: []Step{{Op: FlowThen, Resource: "b"}}},
		},
	}}
	// "hello" matches the first arm.
	res, err := r.Run(context.Background(), "t1", steps, FlowContext{Text: "please say hello"})
	if err != nil || res.Text != "out:a" {
		t.Fatalf("branch hello = %+v, %v", res, err)
	}
	// No match for the first arm falls through to the default (empty) arm.
	res, err = r.Run(context.Background(), "t2", steps, FlowContext{Text: "goodbye"})
	if err != nil || res.Text != "out:b" {
		t.Fatalf("branch default = %+v, %v", res, err)
	}
}

func TestRunnerParallel(t *testing.T) {
	store := NewTaskStore()
	var got []string
	inv := func(ctx context.Context, taskID, selector, prompt string) (Outcome, error) {
		got = append(got, selector)
		return Outcome{ResourceID: selector, Text: "out:" + selector}, nil
	}
	r := NewRunner(store, nil, inv, 4)
	steps := []Step{{Op: FlowParallel, Steps: []Step{
		{Op: FlowThen, Resource: "a"},
		{Op: FlowThen, Resource: "b"},
		{Op: FlowThen, Resource: "c"},
	}}}
	res, err := r.Run(context.Background(), "t1", steps, FlowContext{})
	if err != nil {
		t.Fatalf("Run parallel: %v", err)
	}
	if len(res.Outcomes) != 3 || len(got) != 3 {
		t.Fatalf("parallel outcomes = %d, got=%v", len(res.Outcomes), got)
	}
	if !strings.Contains(res.Text, "out:a") || !strings.Contains(res.Text, "out:b") || !strings.Contains(res.Text, "out:c") {
		t.Fatalf("parallel text = %q", res.Text)
	}

	// maxParallel caps how many members actually run.
	limited := NewRunner(store, nil, inv, 1)
	res, err = limited.Run(context.Background(), "t2", steps, FlowContext{})
	if err != nil {
		t.Fatalf("Run parallel limited: %v", err)
	}
	if len(res.Outcomes) != 1 {
		t.Fatalf("limited parallel should run 1 member, ran %d", len(res.Outcomes))
	}
}

func TestRunnerStopsOnInputRequired(t *testing.T) {
	store := NewTaskStore()
	inv := func(ctx context.Context, taskID, selector, prompt string) (Outcome, error) {
		if selector == "needs-input" {
			return Outcome{ResourceID: selector, RequiresInput: true, Prompt: "provide more"}, nil
		}
		return Outcome{ResourceID: selector, Text: "out:" + selector}, nil
	}
	r := NewRunner(store, nil, inv, 2)
	steps := []Step{
		{Op: FlowThen, Resource: "first"},
		{Op: FlowThen, Resource: "needs-input"},
		{Op: FlowThen, Resource: "third"},
	}
	res, err := r.Run(context.Background(), "t1", steps, FlowContext{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.RequiresInput || res.Prompt != "provide more" {
		t.Fatalf("expected input-required, got %+v", res)
	}
	if len(res.Outcomes) != 2 {
		t.Fatalf("should have stopped after needs-input, outcomes=%d", len(res.Outcomes))
	}
}

func TestRunnerUnknownOp(t *testing.T) {
	store := NewTaskStore()
	inv := func(ctx context.Context, taskID, selector, prompt string) (Outcome, error) {
		return Outcome{ResourceID: selector}, nil
	}
	r := NewRunner(store, nil, inv, 2)
	steps := []Step{{Op: "bogus", Resource: "x"}}
	if _, err := r.Run(context.Background(), "t1", steps, FlowContext{}); httputil.AsError(err).Code != CodeInvalidFlow {
		t.Fatalf("unknown op err = %v, want %q", err, CodeInvalidFlow)
	}
}

func TestStartAndFinishSpan(t *testing.T) {
	store := NewTaskStore()
	s := store.StartSpan("t1", "r1", "kind", "label")
	if s.ID == "" || s.TaskID != "t1" || s.ResourceID != "r1" || s.Status != SpanStatusOK {
		t.Fatalf("StartSpan = %+v", s)
	}
	fin := store.FinishSpan(s, SpanStatusInputRequired, "input_required")
	if fin.Status != SpanStatusInputRequired || fin.Code != "input_required" {
		t.Fatalf("FinishSpan = %+v", fin)
	}
	if fin.DurationMS < 0 {
		t.Fatalf("DurationMS = %d", fin.DurationMS)
	}
	// The returned span must have its private start cleared so it is safe to serialise.
	if fin.start != (time.Time{}) {
		t.Fatal("finished span must not carry the private start time")
	}
}
