package hub

import (
	"context"
	"errors"

	"agenthub/internal/a2a"
)

// Persistence errors a repository implementation returns. They carry no driver
// text: the hub maps them onto the machine readable codes of the A2A contract.
var (
	// ErrTaskNotFound is returned for an unknown task id.
	ErrTaskNotFound = errors.New(string(CodeTaskNotFound))
	// ErrStateConflict is returned when a conditional state update matched no
	// row, i.e. another writer already advanced the task.
	ErrStateConflict = errors.New(string(CodeInvalidStateTransition))
	// ErrRestoring is returned while a checkpoint is being restored. It is not
	// the caller's failure: the same request a moment later is accepted.
	ErrRestoring = errors.New(string(CodeRestoring))
)

// TaskRepository persists tasks and everything a run accumulates: artifacts,
// the span timeline, the parked question and the push configuration.
//
// The default implementation is the in-memory map inside TaskStore; the
// PostgreSQL one lives in internal/store.
type TaskRepository interface {
	// Create stores a freshly built submitted task.
	Create(ctx context.Context, t *a2a.Task) error
	// Get returns the task with its artifacts and history, or ErrTaskNotFound.
	Get(ctx context.Context, id string) (*a2a.Task, error)
	List(ctx context.Context) ([]*a2a.Task, error)
	// SetState performs a conditional update (from -> to) and returns the
	// updated task. It returns ErrTaskNotFound for an unknown id and
	// ErrStateConflict when the task is no longer in state from.
	SetState(ctx context.Context, id string, from, to a2a.TaskState, msg *a2a.Message) (*a2a.Task, error)
	// AddArtifact appends one artifact and returns the updated task.
	AddArtifact(ctx context.Context, id string, art a2a.Artifact) (*a2a.Task, error)
	AppendHistory(ctx context.Context, id string, msg a2a.Message) error
	AddSpan(ctx context.Context, s Span) error
	Spans(ctx context.Context, id string) ([]Span, error)
	// SetPending stores the parked question; a nil pointer clears it.
	SetPending(ctx context.Context, id string, p *PendingInput) error
	// Pending returns the parked question, or nil when the task is not parked.
	Pending(ctx context.Context, id string) (*PendingInput, error)
	// SetPush stores the callback config; a nil pointer clears it.
	SetPush(ctx context.Context, id string, cfg *a2a.PushNotificationConfig) error
	// Push returns the callback config, or nil when none is registered.
	Push(ctx context.Context, id string) (*a2a.PushNotificationConfig, error)
}

// EventRepository persists the frames broadcast for a task so a client that
// reconnects with since=<seq> can replay what it missed.
type EventRepository interface {
	// Append records one frame. The broker treats a failure as best effort.
	Append(ctx context.Context, e Event) error
	// Since returns frames of one task with seq > after, oldest first.
	Since(ctx context.Context, taskID string, after uint64, limit int) ([]Event, error)
	// MaxSeq returns the highest persisted seq, used to resume the broker
	// counter after a restart so replayed and live frames never collide.
	MaxSeq(ctx context.Context) (uint64, error)
}

// PushRepository persists the callbacks the notifier captures.
type PushRepository interface {
	// Append records one callback.
	Append(ctx context.Context, r PushRecord) error
	// List returns callbacks oldest first; an empty taskID means every task.
	List(ctx context.Context, taskID string, limit int) ([]PushRecord, error)
	// Prune keeps only the newest keepPerTask records per task.
	Prune(ctx context.Context, keepPerTask int) error
}

// Persistence bundles the repositories the hub delegates to. A nil field falls
// back to the in-memory behaviour, which is what the unit tests rely on.
type Persistence struct {
	Tasks  TaskRepository
	Events EventRepository
	Pushes PushRepository
}
