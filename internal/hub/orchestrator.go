package hub

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// Emit delivers one streaming frame to a subscriber.
type Emit func(event string, payload any) error

// PushRecord is one callback captured by the mock notifier.
type PushRecord struct {
	TaskID string                     `json:"taskId"`
	URL    string                     `json:"url"`
	State  a2a.TaskState              `json:"state"`
	Config a2a.PushNotificationConfig `json:"config"`
}

// PushNotifier delivers the terminal callback registered for a task.
type PushNotifier interface {
	Notify(ctx context.Context, cfg a2a.PushNotificationConfig, taskID string, state a2a.TaskState) error
}

// MockNotifier records callbacks in memory: it never performs network I/O, so
// push notification behaviour stays assertable in tests.
//
// TODO(real): POST the A2A task payload to cfg.URL with the configured auth.
type MockNotifier struct {
	mu      sync.Mutex
	records []PushRecord
}

// Notify records one callback.
func (n *MockNotifier) Notify(ctx context.Context, cfg a2a.PushNotificationConfig, taskID string, state a2a.TaskState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.records = append(n.records, PushRecord{TaskID: taskID, URL: cfg.URL, State: state, Config: cfg})
	slog.Info("hub: push notification recorded", "task", taskID, "state", state)
	return nil
}

// Records returns a copy of every recorded callback.
func (n *MockNotifier) Records() []PushRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]PushRecord, len(n.records))
	copy(out, n.records)
	return out
}

// Hub is the orchestration entry point used by the HTTP layer.
type Hub struct {
	cfg    *config.Config
	reg    *registry.Registry
	store  *TaskStore
	broker *Broker
	opts   adapter.Options
	pusher PushNotifier
}

// New wires the hub together.
func New(cfg *config.Config, reg *registry.Registry, opts adapter.Options, pusher PushNotifier) *Hub {
	return &Hub{
		cfg:    cfg,
		reg:    reg,
		store:  NewTaskStore(),
		broker: NewBroker(),
		opts:   opts,
		pusher: pusher,
	}
}

// runner builds a per-request flow runner: the hub itself stays immutable so
// concurrent requests never share mutable state.
func (h *Hub) runner(emit Emit) *Runner {
	return NewRunner(h.store, h.broker, h.invoker(emit), h.cfg.MaxParallel)
}

// Registry exposes the catalogue for the control plane.
func (h *Hub) Registry() *registry.Registry { return h.reg }

// Store exposes the task store for the control plane.
func (h *Hub) Store() *TaskStore { return h.store }

// Broker exposes the event fan-out for SSE endpoints.
func (h *Hub) Broker() *Broker { return h.broker }

// Send runs a task to completion (or to input-required) and returns it.
func (h *Hub) Send(ctx context.Context, params a2a.MessageSendParams) (*a2a.Task, error) {
	return h.run(ctx, params, nil)
}

// Stream runs a task while pushing every frame to emit.
func (h *Hub) Stream(ctx context.Context, params a2a.MessageSendParams, emit Emit) (*a2a.Task, error) {
	return h.run(ctx, params, emit)
}

// Get returns a task, optionally trimming its history.
func (h *Hub) Get(id string, historyLength *int) (*a2a.Task, error) {
	task := h.store.Get(id)
	if task == nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	if historyLength != nil && *historyLength >= 0 && len(task.History) > *historyLength {
		task.History = task.History[len(task.History)-*historyLength:]
	}
	return task, nil
}

// Cancel parks a task in canceled.
func (h *Hub) Cancel(id string) (*a2a.Task, error) {
	task := h.store.Get(id)
	if task == nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	if task.Status.State.IsTerminal() {
		return nil, a2a.NewError(a2a.CodeTaskNotCancelable, a2a.MsgTaskNotCancelable)
	}
	updated, err := h.store.SetState(id, a2a.TaskStateCanceled, nil)
	if err != nil {
		return nil, transitionError(err)
	}
	h.publishStatus(updated, nil, true)
	h.notifyPush(updated)
	return updated, nil
}

// Resume continues a task parked in input-required with the caller's answer.
func (h *Hub) Resume(ctx context.Context, params a2a.TaskResumeParams) (*a2a.Task, error) {
	task := h.store.Get(params.ID)
	if task == nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	if task.Status.State != a2a.TaskStateInputRequired {
		return nil, a2a.NewError(a2a.CodeTaskNotResumable, a2a.MsgTaskNotResumable)
	}
	answer := params.Message
	if answer.Role == "" {
		answer.Role = a2a.RoleUser
	}
	if strings.TrimSpace(answer.Text()) == "" {
		answer.Parts = []a2a.Part{a2a.NewTextPart("approved")}
	}
	if err := h.store.AppendHistory(params.ID, answer); err != nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	updated, err := h.store.SetState(params.ID, a2a.TaskStateWorking, nil)
	if err != nil {
		return nil, transitionError(err)
	}
	h.publishStatus(updated, nil, false)

	// Resuming feeds the caller's answer back into the flow as the new input.
	prompt := answer.Text()
	steps := DefaultSteps(task.ResourceID)
	res, err := h.runner(nil).Run(ctx, params.ID, steps, FlowContext{Text: prompt})
	if err != nil {
		return h.fail(params.ID, err, nil)
	}
	if res.RequiresInput {
		return h.park(params.ID, res.Prompt, nil)
	}
	return h.complete(ctx, params.ID, res, nil)
}

// SetPush registers the push notification callback for a task.
func (h *Hub) SetPush(params a2a.TaskPushNotificationConfigParams) (*a2a.PushNotificationConfig, error) {
	if !h.cfg.PushEnabled {
		return nil, a2a.NewError(a2a.CodePushNotificationNotSupported, a2a.MsgPushNotificationNotSupported)
	}
	cfg := params.Config()
	if cfg == nil || strings.TrimSpace(cfg.URL) == "" {
		return nil, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams)
	}
	if err := h.store.SetPush(params.TaskID, cfg); err != nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	cp := *cfg
	return &cp, nil
}

// GetPush returns the registered callback for a task.
func (h *Hub) GetPush(taskID string) (*a2a.PushNotificationConfig, error) {
	cfg := h.store.Push(taskID)
	if cfg == nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	cp := *cfg
	return &cp, nil
}

// run is the shared implementation of message/send and message/stream.
func (h *Hub) run(ctx context.Context, params a2a.MessageSendParams, emit Emit) (*a2a.Task, error) {
	if len(params.Message.Parts) == 0 {
		return nil, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams)
	}
	entry, err := h.selectResource(params)
	if err != nil {
		return nil, err
	}
	if params.Configuration != nil && params.Configuration.PushNotification != nil {
		if !h.cfg.PushEnabled {
			return nil, a2a.NewError(a2a.CodePushNotificationNotSupported, a2a.MsgPushNotificationNotSupported)
		}
	}

	msg := params.Message
	if msg.Role == "" {
		msg.Role = a2a.RoleUser
	}
	task := h.store.Create(entry.ID, msg)

	working, err := h.store.SetState(task.ID, a2a.TaskStateWorking, nil)
	if err != nil {
		return nil, transitionError(err)
	}
	h.publishStatus(working, emit, false)

	steps, err := h.flowFor(params, entry.ID)
	if err != nil {
		return h.fail(task.ID, err, emit)
	}
	res, err := h.runner(emit).Run(ctx, task.ID, steps, FlowContext{Text: msg.Text(), Kind: string(entry.Kind)})
	if err != nil {
		return h.fail(task.ID, err, emit)
	}
	if res.RequiresInput {
		return h.park(task.ID, res.Prompt, emit)
	}
	return h.complete(ctx, task.ID, res, emit)
}

func (h *Hub) complete(ctx context.Context, taskID string, res RunResult, emit Emit) (*a2a.Task, error) {
	reply := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart(res.Text))
	updated, err := h.store.SetState(taskID, a2a.TaskStateCompleted, &reply)
	if err != nil {
		return nil, transitionError(err)
	}
	if err := h.store.AppendHistory(taskID, reply); err != nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	h.publishMessage(taskID, reply, emit)
	h.publishStatus(updated, emit, true)
	h.notifyPush(updated)
	return h.store.Get(taskID), nil
}

func (h *Hub) park(taskID, prompt string, emit Emit) (*a2a.Task, error) {
	question := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart(prompt))
	updated, err := h.store.SetState(taskID, a2a.TaskStateInputRequired, &question)
	if err != nil {
		return nil, transitionError(err)
	}
	if err := h.store.SetPending(taskID, prompt); err != nil {
		return nil, a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	h.publishMessage(taskID, question, emit)
	h.publishStatus(updated, emit, true)
	h.notifyPush(updated)
	return h.store.Get(taskID), nil
}

func (h *Hub) fail(taskID string, cause error, emit Emit) (*a2a.Task, error) {
	code := string(httputil.AsError(cause).Code)
	reply := a2a.NewMessage(a2a.RoleAgent, a2a.NewDataPart(map[string]any{"code": code}))
	updated, err := h.store.SetState(taskID, a2a.TaskStateFailed, &reply)
	if err != nil {
		// A task that cannot transition already reached a terminal state; the
		// original failure is the one worth reporting.
		return h.store.Get(taskID), adapterError(cause)
	}
	h.publishMessage(taskID, reply, emit)
	h.publishStatus(updated, emit, true)
	h.notifyPush(updated)
	return h.store.Get(taskID), adapterError(cause)
}

func (h *Hub) invoker(emit Emit) Invoker {
	return func(ctx context.Context, taskID string, selector string, prompt string) (Outcome, error) {
		entry, err := h.selectBy(selector)
		if err != nil {
			return Outcome{}, err
		}
		conn, err := adapter.For(entry, h.opts)
		if err != nil {
			return Outcome{}, err
		}
		req := adapter.InvokeRequest{
			ResourceID: entry.ID,
			Entry:      *entry,
			Prompt:     prompt,
			Metadata:   entry.Metadata,
		}
		res, err := conn.Invoke(ctx, req)
		if err != nil {
			return Outcome{}, err
		}
		for _, art := range res.Artifacts {
			if _, err := h.store.AddArtifact(taskID, art); err != nil {
				return Outcome{}, err
			}
			h.publishArtifact(taskID, art, emit)
		}
		return Outcome{
			ResourceID:    entry.ID,
			Kind:          string(entry.Kind),
			Label:         entry.Name,
			Text:          res.Text,
			Artifacts:     res.Artifacts,
			RequiresInput: res.RequiresInput,
			Prompt:        res.Prompt,
		}, nil
	}
}

func (h *Hub) flowFor(params a2a.MessageSendParams, resourceID string) ([]Step, error) {
	if params.Metadata != nil {
		if raw, ok := params.Metadata["flow"]; ok {
			steps, err := ParseSteps(raw)
			if err != nil {
				return nil, err
			}
			if len(steps) > 0 {
				return steps, nil
			}
		}
	}
	return DefaultSteps(resourceID), nil
}

func (h *Hub) selectResource(params a2a.MessageSendParams) (*registry.Entry, error) {
	var selector string
	if params.Metadata != nil {
		for _, key := range []string{"resourceId", "resource_id", "resource"} {
			if v, ok := params.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				selector = v
				break
			}
		}
	}
	entry, err := h.selectBy(selector)
	if err != nil {
		var kindHint string
		if params.Metadata != nil {
			if v, ok := params.Metadata["kind"].(string); ok {
				kindHint = v
			}
		}
		if kindHint != "" {
			if byKind, kerr := h.selectBy(kindHint); kerr == nil {
				return byKind, nil
			}
		}
		return nil, err
	}
	return entry, nil
}

// selectBy resolves a selector: resource id, exact name, kind name, or empty
// (an agent first, otherwise the lexicographically first entry).
func (h *Hub) selectBy(selector string) (*registry.Entry, error) {
	selector = strings.TrimSpace(selector)
	if h.reg == nil {
		return nil, a2a.NewError(a2a.CodeResourceNotFound, a2a.MsgResourceNotFound)
	}
	entries := h.reg.Snapshot()
	if len(entries) == 0 {
		return nil, a2a.NewError(a2a.CodeResourceNotFound, a2a.MsgResourceNotFound)
	}
	if selector != "" {
		for _, e := range entries {
			if strings.EqualFold(e.ID, selector) {
				return e, nil
			}
		}
		for _, e := range entries {
			if strings.EqualFold(e.Name, selector) {
				return e, nil
			}
		}
		for _, e := range entries {
			if strings.EqualFold(string(e.Kind), selector) {
				return e, nil
			}
		}
		return nil, a2a.NewError(a2a.CodeResourceNotFound, a2a.MsgResourceNotFound)
	}
	for _, e := range entries {
		if e.Kind == registry.KindAgent {
			return e, nil
		}
	}
	return entries[0], nil
}

func (h *Hub) publishStatus(task *a2a.Task, emit Emit, final bool) {
	if task == nil {
		return
	}
	frame := a2a.TaskStatusUpdateEvent{
		Kind:      a2a.KindStatusUpdate,
		TaskID:    task.ID,
		ContextID: task.ContextID,
		Status:    task.Status,
		Final:     final,
	}
	h.broker.Publish(Event{TaskID: task.ID, Kind: EventKindStatus, At: task.Status.Timestamp, Status: &frame})
	if emit != nil {
		if err := emit(a2a.EventTaskStatus, frame); err != nil {
			slog.Debug("hub: emit status frame failed", "error", err)
		}
	}
}

func (h *Hub) publishArtifact(taskID string, art a2a.Artifact, emit Emit) {
	task := h.store.Get(taskID)
	frame := a2a.TaskArtifactUpdateEvent{
		Kind:      a2a.KindArtifactUpdate,
		TaskID:    taskID,
		Artifact:  art,
		LastChunk: art.LastChunk,
	}
	if task != nil {
		frame.ContextID = task.ContextID
	}
	h.broker.Publish(Event{TaskID: taskID, Kind: EventKindArtifact, At: a2a.Timestamp(nowUTC()), Artifact: &frame})
	if emit != nil {
		if err := emit(a2a.EventTaskArtifact, frame); err != nil {
			slog.Debug("hub: emit artifact frame failed", "error", err)
		}
	}
}

func (h *Hub) publishMessage(taskID string, msg a2a.Message, emit Emit) {
	h.broker.Publish(Event{TaskID: taskID, Kind: EventKindMessage, At: a2a.Timestamp(nowUTC()), Message: &msg})
	if emit != nil {
		if err := emit(a2a.EventTaskMessage, msg); err != nil {
			slog.Debug("hub: emit message frame failed", "error", err)
		}
	}
}

func (h *Hub) notifyPush(task *a2a.Task) {
	if task == nil || !h.cfg.PushEnabled || h.pusher == nil {
		return
	}
	cfg := h.store.Push(task.ID)
	if cfg == nil {
		return
	}
	if err := h.pusher.Notify(context.Background(), *cfg, task.ID, task.Status.State); err != nil {
		slog.Warn("hub: push notification failed", "task", task.ID, "error", err)
	}
}

func lastUserText(task *a2a.Task) string {
	for i := len(task.History) - 1; i >= 0; i-- {
		if task.History[i].Role == a2a.RoleUser {
			return task.History[i].Text()
		}
	}
	return ""
}

func adapterError(err error) error {
	if errors.As(err, new(*a2a.Error)) {
		return err
	}
	code := string(httputil.AsError(err).Code)
	rpc := a2a.NewError(a2a.CodeResourceUnreachable, a2a.MsgResourceUnreachable)
	rpc.Data = map[string]string{"code": code}
	return rpc
}

func transitionError(err error) *a2a.Error {
	if isNotFound(err) {
		return a2a.NewError(a2a.CodeTaskNotFound, a2a.MsgTaskNotFound)
	}
	return a2a.NewError(a2a.CodeInvalidStateTransition, a2a.MsgInvalidStateTransition)
}

func isNotFound(err error) bool {
	var e *httputil.Error
	if errors.As(err, &e) {
		return e.Code == CodeTaskNotFound
	}
	return false
}
