// Package hub orchestrates A2A tasks: it owns the task state machine, the
// artifacts a run accumulates, the span timeline and the event fan-out, and it
// dispatches work to the adapter that matches the selected resource.
package hub

import (
	"errors"
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
type TaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*taskRecord
}

// NewTaskStore builds an empty store.
func NewTaskStore() *TaskStore {
	return &TaskStore{tasks: map[string]*taskRecord{}}
}

// Create registers a submitted task for the inbound message.
func (s *TaskStore) Create(resourceID string, msg a2a.Message) *a2a.Task {
	task := a2a.NewTask(registry.NewID("task"), msg, resourceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = &taskRecord{task: task}
	cp := *task
	cp.Artifacts = nil
	cp.History = append([]a2a.Message{}, task.History...)
	return &cp
}

// Get returns a copy of a task, or nil when unknown.
func (s *TaskStore) Get(id string) *a2a.Task {
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

// AddArtifact appends (or extends) an artifact on a task.
func (s *TaskStore) AddArtifact(id string, art a2a.Artifact) (*a2a.Task, error) {
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

func cloneTask(t *a2a.Task) *a2a.Task {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Artifacts = append([]a2a.Artifact(nil), t.Artifacts...)
	cp.History = append([]a2a.Message(nil), t.History...)
	return &cp
}
