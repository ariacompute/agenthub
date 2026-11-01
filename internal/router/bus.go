package router

import (
	"sync"
)

// Bus fans domain events out to the console. It is the same shape as the hub
// broker and for the same reason: a slow subscriber loses the oldest frame rather
// than blocking the writer, because a stale dashboard must never stall a run.
//
// The bus is a live view only. Replay is served from the store, so a client that
// reconnects with since=<seq> gets exactly the frames it missed.
type Bus struct {
	mu     sync.Mutex
	next   int
	subs   map[int]*busSub
	closed bool
}

type busSub struct {
	subject string
	ch      chan Event
}

// NewBus returns an empty bus.
func NewBus() *Bus {
	return &Bus{subs: map[int]*busSub{}}
}

// Subscribe returns a channel of events for one subject ("" means every event)
// and the function that releases it. The buffer must be positive; the caller owes
// the bus a matching cancel, which is why it is returned rather than left to a
// context.
func (b *Bus) Subscribe(subject string, buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	id := b.next
	b.next++
	sub := &busSub{subject: subject, ch: make(chan Event, buffer)}
	b.subs[id] = sub
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(sub.ch)
			}
			b.mu.Unlock()
		})
	}
	return sub.ch, cancel
}

// Publish delivers one committed event. It never blocks: a full subscriber loses
// its oldest frame to make room.
func (b *Bus) Publish(e *Event) {
	if b == nil || e == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if sub.subject != "" && sub.subject != e.Subject {
			continue
		}
		select {
		case sub.ch <- *e:
		default:
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- *e:
			default:
			}
		}
	}
}

// SubscriberCount reports how many live streams there are, for the water level.
func (b *Bus) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Close releases every subscriber. A closed bus drops later publishes instead of
// panicking on a send to a closed channel.
func (b *Bus) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, sub := range b.subs {
		close(sub.ch)
		delete(b.subs, id)
	}
}
