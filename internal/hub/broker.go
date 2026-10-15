package hub

import (
	"sync"

	"agenthub/internal/a2a"
)

// Event kinds fan out to SSE subscribers.
const (
	EventKindStatus   = "status"
	EventKindArtifact = "artifact"
	EventKindMessage  = "message"
	EventKindSpan     = "span"
)

// Event is one frame broadcast for a task.
type Event struct {
	Seq      uint64                       `json:"seq"`
	TaskID   string                       `json:"taskId"`
	Kind     string                       `json:"kind"`
	At       string                       `json:"at"`
	Status   *a2a.TaskStatusUpdateEvent   `json:"status,omitempty"`
	Artifact *a2a.TaskArtifactUpdateEvent `json:"artifact,omitempty"`
	Message  *a2a.Message                 `json:"message,omitempty"`
	Span     *Span                        `json:"span,omitempty"`
}

// Broker fans task events out to every subscriber.
//
// Subscribers get a buffered channel; a slow consumer loses the oldest frame
// instead of blocking the run, so one stuck SSE client can never stall a task.
type Broker struct {
	mu   sync.Mutex
	next uint64
	subs map[string]map[uint64]chan Event
}

// NewBroker builds an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: map[string]map[uint64]chan Event{}}
}

// Subscribe registers a subscriber for one task. The returned cancel function
// must be called by the consumer when it stops reading.
func (b *Broker) Subscribe(taskID string, buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	if b.subs[taskID] == nil {
		b.subs[taskID] = map[uint64]chan Event{}
	}
	b.subs[taskID][id] = ch
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if m, ok := b.subs[taskID]; ok {
				delete(m, id)
				if len(m) == 0 {
					delete(b.subs, taskID)
				}
			}
			close(ch)
		})
	}
	return ch, cancel
}

// Publish delivers one event to every subscriber of the task.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	e.Seq = b.next
	b.next++
	targets := make([]chan Event, 0, 4)
	for _, ch := range b.subs[e.TaskID] {
		targets = append(targets, ch)
	}
	b.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- e:
		default:
			// Drop the oldest frame so the newest state always wins.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}

// SubscriberCount reports how many consumers currently watch a task.
func (b *Broker) SubscriberCount(taskID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[taskID])
}
