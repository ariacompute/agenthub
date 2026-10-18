// Package hub orchestrates A2A tasks: it owns the task state machine, the
// artifacts a run accumulates, the span timeline and the event fan-out, and it
// dispatches work to the adapter that matches the selected resource.
package hub

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// Machine readable error codes produced by the hub.
const (
	CodeTaskNotFound           = httputil.Code("task_not_found")
	CodeInvalidStateTransition = httputil.Code("invalid_state_transition")
	CodeNoResource             = httputil.Code("no_resource_available")
	CodeTaskNotResumable       = httputil.Code("task_not_resumable")
	CodeInvalidFlow            = httputil.Code("invalid_flow")
	// CodeRestoring is answered while a checkpoint is being restored: the
	// database is mid-rewrite, so no new task may start.
	CodeRestoring = httputil.Code("restore_in_progress")
)

// PendingInput records why a task was parked in input-required.
type PendingInput struct {
	Prompt  string `json:"prompt"`
	AskedAt string `json:"askedAt"`
}

type taskRecord struct {
	task    *a2a.Task
	spans   []Span
	push    *a2a.PushNotificationConfig
	pending *PendingInput
}

// TaskStore owns every task known to agenthub.
//
// It keeps the in-memory implementation used by the tests and, when a
// TaskRepository is attached, delegates every read and write to it so tasks
// survive a restart. The public surface is identical in both modes.
type TaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*taskRecord
	repo  TaskRepository
}

// NewTaskStore builds an empty, in-memory store.
func NewTaskStore() *TaskStore {
	return &TaskStore{tasks: map[string]*taskRecord{}}
}

// NewTaskStoreWithRepository builds a store that persists through repo.
func NewTaskStoreWithRepository(repo TaskRepository) *TaskStore {
	return &TaskStore{tasks: map[string]*taskRecord{}, repo: repo}
}

// Create registers a submitted task for the inbound message.
func (s *TaskStore) Create(resourceID string, msg a2a.Message) *a2a.Task {
	task := a2a.NewTask(registry.NewID("task"), msg, resourceID)
	if s.repo != nil {
		if err := s.repo.Create(dbCtx(), task); err != nil {
			// The task is still returned so the caller can report a coherent
			// failure; the next transition surfaces the missing row.
			slog.Error("hub: persist task", "task", task.ID, "error", err)
		}
	}
	cp := *task
	cp.Artifacts = nil
	cp.History = append([]a2a.Message{}, task.History...)
	if s.repo != nil {
		return &cp
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = &taskRecord{task: task}
	return &cp
}

// Get returns a copy of a task, or nil when unknown.
func (s *TaskStore) Get(id string) *a2a.Task {
	if s.repo != nil {
		task, err := s.repo.Get(dbCtx(), id)
		if err != nil {
			if !errors.Is(err, ErrTaskNotFound) {
				slog.Error("hub: load task", "task", id, "error", err)
			}
			return nil
		}
		return task
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.tasks[id]
	if !ok {
		return nil
	}
	return cloneTask(rec.task)
}

// List returns every task, newest first.
func (s *TaskStore) List() []*a2a.Task {
	if s.repo != nil {
		tasks, err := s.repo.List(dbCtx())
		if err != nil {
			slog.Error("hub: list tasks", "error", err)
			return nil
		}
		return tasks
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*a2a.Task, 0, len(s.tasks))
	for _, rec := range s.tasks {
		out = append(out, cloneTask(rec.task))
	}
	return out
}

// SetState advances the lifecycle, rejecting illegal transitions.
func (s *TaskStore) SetState(id string, state a2a.TaskState, msg *a2a.Message) (*a2a.Task, error) {
	if s.repo != nil {
		return s.setStatePersisted(id, state, msg)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[id]
	if !ok {
		return nil, &httputil.Error{Code: CodeTaskNotFound}
	}
	if !CanTransition(rec.task.Status.State, state) {
		return nil, &httputil.Error{Code: CodeInvalidStateTransition}
	}
	rec.task.Status = a2a.TaskStatus{State: state, Message: msg, Timestamp: a2a.Timestamp(time.Now())}
	if state == a2a.TaskStateInputRequired {
		rec.pending = &PendingInput{Prompt: "", AskedAt: a2a.Timestamp(time.Now())}
	} else {
		rec.pending = nil
	}
	return cloneTask(rec.task), nil
}

// setStatePersisted validates the transition in Go (the contract and the error
// code stay in one place) and then applies a conditional update so two writers
// can never lose each other's update.
func (s *TaskStore) setStatePersisted(id string, state a2a.TaskState, msg *a2a.Message) (*a2a.Task, error) {
	current, err := s.repo.Get(dbCtx(), id)
	if err != nil {
		return nil, taskErr("set_state", err)
	}
	if !CanTransition(current.Status.State, state) {
		return nil, &httputil.Error{Code: CodeInvalidStateTransition}
	}
	updated, err := s.repo.SetState(dbCtx(), id, current.Status.State, state, msg)
	if err != nil {
		return nil, taskErr("set_state", err)
	}
	// Parking and unparking is best effort: a missing question must never
	// abort a transition the A2A contract already accepted.
	if state == a2a.TaskStateInputRequired {
		if err := s.repo.SetPending(dbCtx(), id, &PendingInput{Prompt: "", AskedAt: a2a.Timestamp(time.Now())}); err != nil {
			slog.Warn("hub: store pending question", "task", id, "error", err)
		}
	} else if err := s.repo.SetPending(dbCtx(), id, nil); err != nil {
		slog.Warn("hub: clear pending question", "task", id, "error", err)
	}
	return updated, nil
}

// AddArtifact appends (or extends) an artifact on a task.
func (s *TaskStore) AddArtifact(id string, art a2a.Artifact) (*a2a.Task, error) {
	if s.repo != nil {
		updated, err := s.repo.AddArtifact(dbCtx(), id, art)
		if err != nil {
			return nil, taskErr("add_artifact", err)
		}
		return updated, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[id]
	if !ok {
		return nil, &httputil.Error{Code: CodeTaskNotFound}
	}
	rec.task.Artifacts = append(rec.task.Artifacts, art)
	return cloneTask(rec.task), nil
}

// AppendHistory records a message on the task history.
func (s *TaskStore) AppendHistory(id string, msg a2a.Message) error {
	if s.repo != nil {
		return taskErr("append_history", s.repo.AppendHistory(dbCtx(), id, msg))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[id]
	if !ok {
		return &httputil.Error{Code: CodeTaskNotFound}
	}
	rec.task.History = append(rec.task.History, msg)
	return nil
}

// SetPending stores the question a parked task is waiting for.
func (s *TaskStore) SetPending(id, prompt string) error {
	if s.repo != nil {
		return taskErr("set_pending", s.repo.SetPending(dbCtx(), id, &PendingInput{
			Prompt:  prompt,
			AskedAt: a2a.Timestamp(time.Now()),
		}))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[id]
	if !ok {
		return &httputil.Error{Code: CodeTaskNotFound}
	}
	rec.pending = &PendingInput{Prompt: prompt, AskedAt: a2a.Timestamp(time.Now())}
	return nil
}

// Pending returns the parked question, if any.
func (s *TaskStore) Pending(id string) *PendingInput {
	if s.repo != nil {
		p, err := s.repo.Pending(dbCtx(), id)
		if err != nil {
			slog.Error("hub: load pending question", "task", id, "error", err)
			return nil
		}
		return p
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.tasks[id]
	if !ok || rec.pending == nil {
		return nil
	}
	cp := *rec.pending
	return &cp
}

// SetPush stores the push notification config for a task.
func (s *TaskStore) SetPush(id string, cfg *a2a.PushNotificationConfig) error {
	if s.repo != nil {
		return taskErr("set_push", s.repo.SetPush(dbCtx(), id, cfg))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[id]
	if !ok {
		return &httputil.Error{Code: CodeTaskNotFound}
	}
	rec.push = cfg
	return nil
}

// Push returns the registered push notification config, if any.
func (s *TaskStore) Push(id string) *a2a.PushNotificationConfig {
	if s.repo != nil {
		cfg, err := s.repo.Push(dbCtx(), id)
		if err != nil {
			slog.Error("hub: load push config", "task", id, "error", err)
			return nil
		}
		return cfg
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.tasks[id]
	if !ok || rec.push == nil {
		return nil
	}
	cp := *rec.push
	return &cp
}

// Spans returns the recorded span timeline of a task.
func (s *TaskStore) Spans(id string) []Span {
	if s.repo != nil {
		spans, err := s.repo.Spans(dbCtx(), id)
		if err != nil {
			slog.Error("hub: load spans", "task", id, "error", err)
			return nil
		}
		return spans
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.tasks[id]
	if !ok {
		return nil
	}
	out := make([]Span, len(rec.spans))
	copy(out, rec.spans)
	return out
}

// CanTransition reports whether the A2A lifecycle allows from -> to.
func CanTransition(from, to a2a.TaskState) bool {
	if from == to {
		return false
	}
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == a2a.TaskStateUnknown {
		return false
	}
	if from.IsTerminal() {
		return false
	}
	switch from {
	case a2a.TaskStateSubmitted:
		return to == a2a.TaskStateWorking || to == a2a.TaskStateFailed || to == a2a.TaskStateCanceled
	case a2a.TaskStateWorking:
		return to == a2a.TaskStateInputRequired || to.IsTerminal()
	case a2a.TaskStateInputRequired:
		return to == a2a.TaskStateWorking || to == a2a.TaskStateFailed || to == a2a.TaskStateCanceled
	default:
		return false
	}
}

// ErrNoResource is returned when the registry cannot satisfy a request.
var ErrNoResource = errors.New(string(CodeNoResource))

// nowUTC is the single clock helper used for event timestamps.
func nowUTC() time.Time { return time.Now().UTC() }

// dbCtx is the context every persistence call uses. Task state must be written
// even when the caller hangs up, so persistence is deliberately detached from
// the request context.
func dbCtx() context.Context { return context.Background() }

// taskErr normalises a repository failure: driver text never reaches the
// caller, only a machine readable code.
func taskErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var he *httputil.Error
	if errors.As(err, &he) {
		return err
	}
	switch {
	case errors.Is(err, ErrTaskNotFound):
		return &httputil.Error{Code: CodeTaskNotFound}
	case errors.Is(err, ErrStateConflict):
		return &httputil.Error{Code: CodeInvalidStateTransition}
	default:
		slog.Error("hub: persistence failure", "op", op, "error", err)
		return &httputil.Error{Code: httputil.CodeInternal}
	}
}

func cloneTask(t *a2a.Task) *a2a.Task {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Artifacts = append([]a2a.Artifact(nil), t.Artifacts...)
	cp.History = append([]a2a.Message(nil), t.History...)
	return &cp
}
