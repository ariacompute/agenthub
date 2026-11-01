package sandbox

import (
	"context"
	"sync"
	"time"
)

// Bus is the live event fan-out, shaped like router.Bus on purpose: publish
// assigns a sequence number through the store, then hands the frame to every
// subscriber of that subject.
//
// Replay is not the bus's job. A reconnecting console asks the store for
// `since=N`, because a bus that remembered history would have to be bounded and
// would then lie about being complete.
type Bus struct {
	mu     sync.Mutex
	subs   map[string]map[int]chan Event
	nextID int
	closed bool
	store  Store
	now    Clock
}

// NewBus builds a bus over the store that owns the sequence numbers.
func NewBus(store Store, clock Clock) *Bus {
	if clock == nil {
		clock = time.Now
	}
	if store == nil {
		store = NopStore()
	}
	return &Bus{subs: map[string]map[int]chan Event{}, store: store, now: clock}
}

// Subscribe opens a channel for one subject ("" means every frame). The returned
// function must be called to release it, which is what keeps a closed console
// tab from holding a channel forever.
func (b *Bus) Subscribe(subject string, buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 32
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	if b.subs[subject] == nil {
		b.subs[subject] = map[int]chan Event{}
	}
	id := b.nextID
	b.nextID++
	b.subs[subject][id] = ch
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if subs, ok := b.subs[subject]; ok {
				if c, ok := subs[id]; ok {
					delete(subs, id)
					close(c)
				}
				if len(subs) == 0 {
					delete(b.subs, subject)
				}
			}
		})
	}
}

// Publish persists the frame and fans it out. A delivery that a subscriber is
// too slow to take is dropped rather than blocking the caller: the console can
// always replay from the store, and one wedged tab must not stall the Manager.
func (b *Bus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = b.now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.store.AppendEvent(ctx, &e); err != nil {
		// The store is the audit trail; a failed append must not stop the frame
		// from reaching a live console.
		e.Seq = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, key := range []string{e.Subject, ""} {
		for id, ch := range b.subs[key] {
			select {
			case ch <- e:
			default:
				_ = id
			}
		}
	}
}

// Close releases every subscriber. It is safe to call twice.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for subject, subs := range b.subs {
		for id, ch := range subs {
			close(ch)
			delete(subs, id)
		}
		delete(b.subs, subject)
	}
}
