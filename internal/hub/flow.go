package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
)

// Flow primitives. They mirror the graph vocabulary of a workflow engine
// (sequential, conditional, concurrent) without pulling in a dependency: the
// hub needs three primitives to express "model -> skill -> mcp -> cli" relays.
const (
	FlowThen     = "then"
	FlowBranch   = "branch"
	FlowParallel = "parallel"
)

// Step is one node of a flow. Nested Steps carry the arms of a branch or the
// members of a parallel block.
type Step struct {
	Op       string `json:"op"`
	Resource string `json:"resource,omitempty"`
	Label    string `json:"label,omitempty"`
	When     string `json:"when,omitempty"`
	Steps    []Step `json:"steps,omitempty"`
}

// FlowContext is the accumulated state a branch condition can inspect.
type FlowContext struct {
	Text string            `json:"text"`
	Kind string            `json:"kind,omitempty"`
	Keys map[string]string `json:"keys,omitempty"`
}

// Outcome is the result of invoking one resource inside a flow.
type Outcome struct {
	ResourceID    string         `json:"resourceId"`
	Kind          string         `json:"kind"`
	Label         string         `json:"label"`
	Text          string         `json:"text"`
	Artifacts     []a2a.Artifact `json:"artifacts,omitempty"`
	RequiresInput bool           `json:"requiresInput,omitempty"`
	Prompt        string         `json:"prompt,omitempty"`
}

// RunResult aggregates every outcome of a flow execution.
type RunResult struct {
	Outcomes      []Outcome `json:"outcomes"`
	RequiresInput bool      `json:"requiresInput,omitempty"`
	Prompt        string    `json:"prompt,omitempty"`
	Text          string    `json:"text"`
}

// Invoker resolves a resource selector and runs it.
type Invoker func(ctx context.Context, taskID string, selector string, prompt string) (Outcome, error)

// Runner executes flows and records one span per step.
type Runner struct {
	store       *TaskStore
	broker      *Broker
	invoke      Invoker
	maxParallel int
}

// NewRunner builds a flow runner.
func NewRunner(store *TaskStore, broker *Broker, invoke Invoker, maxParallel int) *Runner {
	if maxParallel < 1 {
		maxParallel = 1
	}
	return &Runner{store: store, broker: broker, invoke: invoke, maxParallel: maxParallel}
}

// Run executes steps in order, stopping early when a step asks for input.
func (r *Runner) Run(ctx context.Context, taskID string, steps []Step, fc FlowContext) (RunResult, error) {
	var res RunResult
	for _, step := range steps {
		out, err := r.runStep(ctx, taskID, step, fc)
		if err != nil {
			return res, err
		}
		res.Outcomes = append(res.Outcomes, out.Outcomes...)
		if out.Text != "" {
			if res.Text != "" {
				res.Text += "\n"
			}
			res.Text += out.Text
			fc.Text += "\n" + out.Text
		}
		if out.RequiresInput {
			res.RequiresInput = true
			res.Prompt = out.Prompt
			return res, nil
		}
	}
	return res, nil
}

func (r *Runner) runStep(ctx context.Context, taskID string, step Step, fc FlowContext) (RunResult, error) {
	op := strings.ToLower(strings.TrimSpace(step.Op))
	if op == "" {
		op = FlowThen
	}
	switch op {
	case FlowThen:
		return r.runThen(ctx, taskID, step, fc)
	case FlowBranch:
		return r.runBranch(ctx, taskID, step, fc)
	case FlowParallel:
		return r.runParallel(ctx, taskID, step, fc)
	default:
		return RunResult{}, &httputil.Error{Code: CodeInvalidFlow}
	}
}

func (r *Runner) runThen(ctx context.Context, taskID string, step Step, fc FlowContext) (RunResult, error) {
	label := step.Label
	if label == "" {
		label = "then:" + step.Resource
	}
	span := r.store.StartSpan(taskID, step.Resource, "", label)
	out, err := r.invoke(ctx, taskID, step.Resource, fc.Text)
	if err != nil {
		finished := r.store.FinishSpan(span, SpanStatusError, codeOf(err))
		r.publishSpan(finished)
		return RunResult{}, err
	}
	status := SpanStatusOK
	if out.RequiresInput {
		status = SpanStatusInputRequired
	}
	finished := r.store.FinishSpan(span, status, "")
	r.publishSpan(finished)
	var res RunResult
	res.Outcomes = []Outcome{out}
	res.Text = out.Text
	res.RequiresInput = out.RequiresInput
	res.Prompt = out.Prompt
	return res, nil
}

func (r *Runner) runBranch(ctx context.Context, taskID string, step Step, fc FlowContext) (RunResult, error) {
	label := step.Label
	if label == "" {
		label = "branch"
	}
	span := r.store.StartSpan(taskID, "", "", label)
	matched := -1
	for i, arm := range step.Steps {
		if MatchCondition(arm.When, fc) {
			matched = i
			break
		}
	}
	if matched < 0 {
		finished := r.store.FinishSpan(span, SpanStatusSkipped, "")
		r.publishSpan(finished)
		return RunResult{}, nil
	}
	res, err := r.Run(ctx, taskID, step.Steps[matched].Steps, fc)
	status := SpanStatusOK
	if err != nil {
		status = SpanStatusError
	}
	finished := r.store.FinishSpan(span, status, "")
	r.publishSpan(finished)
	return res, err
}

func (r *Runner) runParallel(ctx context.Context, taskID string, step Step, fc FlowContext) (RunResult, error) {
	label := step.Label
	if label == "" {
		label = "parallel"
	}
	span := r.store.StartSpan(taskID, "", "", label)

	members := step.Steps
	if len(members) > r.maxParallel {
		members = members[:r.maxParallel]
	}
	results := make([]RunResult, len(members))
	errs := make([]error, len(members))
	sem := make(chan struct{}, r.maxParallel)
	var wg sync.WaitGroup
	for i, member := range members {
		wg.Add(1)
		go func(i int, member Step) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Each branch reads its own copy of the context: parallel members
			// must not observe each other's partial output.
			local := FlowContext{Text: fc.Text, Kind: fc.Kind, Keys: fc.Keys}
			res, err := r.Run(ctx, taskID, []Step{member}, local)
			results[i] = res
			errs[i] = err
		}(i, member)
	}
	wg.Wait()

	status := SpanStatusOK
	var res RunResult
	for i := range results {
		if errs[i] != nil {
			status = SpanStatusError
			continue
		}
		res.Outcomes = append(res.Outcomes, results[i].Outcomes...)
		if results[i].Text != "" {
			if res.Text != "" {
				res.Text += "\n"
			}
			res.Text += results[i].Text
		}
		if results[i].RequiresInput && !res.RequiresInput {
			res.RequiresInput = true
			res.Prompt = results[i].Prompt
		}
	}
	finished := r.store.FinishSpan(span, status, "")
	r.publishSpan(finished)
	for i := range errs {
		if errs[i] != nil {
			return res, errs[i]
		}
	}
	return res, nil
}

func (r *Runner) publishSpan(span Span) {
	if r.broker == nil {
		return
	}
	cp := span
	r.broker.Publish(Event{
		TaskID: span.TaskID,
		Kind:   EventKindSpan,
		At:     span.EndAt,
		Span:   &cp,
	})
}

// MatchCondition evaluates a branch guard. An empty guard is the default arm.
func MatchCondition(when string, fc FlowContext) bool {
	when = strings.TrimSpace(when)
	switch when {
	case "", "always", "true":
		return true
	}
	if v, ok := strings.CutPrefix(when, "contains:"); ok {
		return strings.Contains(strings.ToLower(fc.Text), strings.ToLower(v))
	}
	if v, ok := strings.CutPrefix(when, "kind:"); ok {
		return strings.EqualFold(fc.Kind, v)
	}
	if v, ok := strings.CutPrefix(when, "has:"); ok {
		if fc.Keys == nil {
			return false
		}
		_, ok := fc.Keys[v]
		return ok
	}
	return false
}

// ParseSteps accepts a flow description as a JSON array, a JSON string
// containing one, or a single object. Anything else is rejected.
func ParseSteps(v any) ([]Step, error) {
	if v == nil {
		return nil, nil
	}
	switch raw := v.(type) {
	case []Step:
		return raw, nil
	case []any:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, &httputil.Error{Code: CodeInvalidFlow}
		}
		var out []Step
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, &httputil.Error{Code: CodeInvalidFlow}
		}
		return out, nil
	case string:
		var out []Step
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, &httputil.Error{Code: CodeInvalidFlow}
		}
		return out, nil
	case map[string]any:
		data, err := json.Marshal([]any{raw})
		if err != nil {
			return nil, &httputil.Error{Code: CodeInvalidFlow}
		}
		var out []Step
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, &httputil.Error{Code: CodeInvalidFlow}
		}
		return out, nil
	default:
		return nil, &httputil.Error{Code: CodeInvalidFlow}
	}
}

// DefaultSteps is the flow used when a request carries no explicit one: a
// single hop to the selected resource.
func DefaultSteps(selector string) []Step {
	return []Step{{Op: FlowThen, Resource: selector, Label: "invoke:" + selector}}
}

func codeOf(err error) string {
	return string(httputil.AsError(err).Code)
}
