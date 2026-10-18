package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"agenthub/internal/a2a"
)

// Event kinds fan out to SSE subscribers.
const (
	EventKindStatus   = "status"
	EventKindArtifact = "artifact"
	EventKindMessage  = "message"
	EventKindSpan     = "span"
)

// eventWriteTimeout bounds a single persistence attempt so a slow database can
// never stall the writer goroutine indefinitely.
const eventWriteTimeout = 5 * time.Second

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
//
// When an EventRepository is attached the same frames are also persisted. That
// write is asynchronous and best effort: the queue is bounded, a full queue
// drops the frame and a failing database is only warned about. Event
// persistence must never become back pressure on task execution.
type Broker struct {
	mu   sync.Mutex
	next uint64
	subs map[string]map[uint64]chan Event

	events EventRepository
	queue  chan Event
	done   chan struct{}
	wg     sync.WaitGroup
}

// NewBroker builds an empty broker with no persistence.
func NewBroker() *Broker {
	return &Broker{subs: map[string]map[uint64]chan Event{}}
}

// Start attaches an event repository and launches the asynchronous writer.
//
// The sequence counter resumes after the highest persisted seq so a reconnect
// replay and the live feed never hand out the same seq twice.
func (b *Broker) Start(events EventRepository, queueSize int) error {
	if events == nil {
		return errors.New("hub: event repository is nil")
	}
	if queueSize <= 0 {
		queueSize = 1024
	}
	b.mu.Lock()
	if b.events != nil {
		b.mu.Unlock()
		return errors.New("hub: broker already started")
	}
	b.mu.Unlock()

	seq, err := events.MaxSeq(dbCtx())
	if err != nil {
		return fmt.Errorf("hub: read max event seq: %w", err)
	}

	b.mu.Lock()
	b.events = events
	if seq >= b.next {
		b.next = seq + 1
	}
	b.queue = make(chan Event, queueSize)
	b.done = make(chan struct{})
	b.mu.Unlock()

	b.wg.Add(1)
	// The writer owns its own reference to the repository: Close must be able to
	// detach the broker without racing an in-flight write.
	go b.writeLoop(events)
	slog.Info("hub: event persistence started", "resume_seq", b.next, "queue", queueSize)
	return nil
}

// SetNext moves the sequence counter, i.e. the value the next frame will get.
//
// It is only for a restore: the frames in the database are the truth afterwards,
// so the counter follows them even when that means moving backwards. Start only
// ever moves it forwards.
func (b *Broker) SetNext(next uint64) {
	b.mu.Lock()
	b.next = next
	b.mu.Unlock()
}

// Close stops the writer after draining whatever is still queued.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.events == nil {
		b.mu.Unlock()
		return
	}
	b.events = nil
	close(b.done)
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *Broker) writeLoop(events EventRepository) {
	defer b.wg.Done()
	for {
		select {
		case <-b.done:
			for {
				select {
				case e := <-b.queue:
					writeEvent(events, e)
				default:
					return
				}
			}
		case e := <-b.queue:
			writeEvent(events, e)
		}
	}
}

func writeEvent(events EventRepository, e Event) {
	ctx, cancel := context.WithTimeout(dbCtx(), eventWriteTimeout)
	defer cancel()
	if err := events.Append(ctx, e); err != nil {
		slog.Warn("hub: persist event failed", "task", e.TaskID, "seq", e.Seq, "kind", e.Kind, "error", err)
	}
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

// Publish delivers one event to every subscriber of the task and, when
// persistence is enabled, queues it for the asynchronous writer.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	e.Seq = b.next
	b.next++
	targets := make([]chan Event, 0, 4)
	for _, ch := range b.subs[e.TaskID] {
		targets = append(targets, ch)
	}
	queue := b.queue
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

	if queue == nil {
		return
	}
	select {
	case queue <- e:
	default:
		slog.Warn("hub: event queue full, frame dropped", "task", e.TaskID, "seq", e.Seq, "kind", e.Kind)
	}
}

// SubscriberCount reports how many consumers currently watch a task.
func (b *Broker) SubscriberCount(taskID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[taskID])
}
