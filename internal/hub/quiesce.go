package hub

import (
	"context"
	"log/slog"
)

// The quiesce gate.
//
// Restoring a checkpoint truncates every application table and writes an
// earlier state back. A task that ran while that happened would append to a
// database that is about to be replaced, and its spans and events would
// describe rows that no longer exist. So a restore first closes the gate — new
// work is refused with restore_in_progress — and then waits for what is already
// running to finish on its own.
//
// The gate is deliberately small: a counter and a condition variable, and
// nothing in the request path changes while it is open.

// enterTask registers a running task. It returns false while the hub is
// draining, which is how "no new work during a restore" is enforced.
func (h *Hub) enterTask() bool {
	h.qMu.Lock()
	defer h.qMu.Unlock()
	if h.draining {
		return false
	}
	h.active++
	return true
}

// leaveTask releases a finished task and wakes a drain that is waiting for it.
func (h *Hub) leaveTask() {
	h.qMu.Lock()
	h.active--
	if h.active == 0 {
		h.qCond.Broadcast()
	}
	h.qMu.Unlock()
}

// Drain closes the gate and waits for the tasks that are still running.
//
// A failure leaves the gate the way it was found: a drain that could not finish
// must not strand the hub, because the caller only lifts the gate when this
// returned nil.
func (h *Hub) Drain(ctx context.Context) error {
	h.qMu.Lock()
	h.draining = true
	waited := h.active

	// sync.Cond cannot wait on a context, so a watcher wakes the loop when the
	// caller gives up.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			h.qMu.Lock()
			h.qCond.Broadcast()
			h.qMu.Unlock()
		case <-stop:
		}
	}()

	for h.active > 0 {
		if ctx.Err() != nil {
			h.draining = false
			h.qMu.Unlock()
			return ctx.Err()
		}
		h.qCond.Wait()
	}
	h.qMu.Unlock()
	slog.Info("hub: drained before restore", "waitedFor", waited)
	return nil
}

// Allow reopens the gate. It is safe to call when the gate was never closed,
// which is what lets a restore lift it from a deferred statement.
func (h *Hub) Allow() {
	h.qMu.Lock()
	wasDraining := h.draining
	h.draining = false
	h.qCond.Broadcast()
	h.qMu.Unlock()
	if wasDraining {
		slog.Info("hub: accepting work again")
	}
}

// ActiveTaskCount is how much work a drain is waiting for.
func (h *Hub) ActiveTaskCount() int {
	h.qMu.Lock()
	defer h.qMu.Unlock()
	return h.active
}

// ResetEventSeq re-reads the highest persisted event sequence.
//
// A restore replaces the events table: the frames in the database are the truth
// now, so the broker's counter follows them — including backwards, which is the
// only case where it is allowed to move that way. Without it the first event
// after a restore would leave a gap or, worse, collide with a replayed frame.
func (h *Hub) ResetEventSeq() error {
	if h.eventRepo == nil {
		return nil
	}
	seq, err := h.eventRepo.MaxSeq(dbCtx())
	if err != nil {
		return err
	}
	h.broker.SetNext(seq + 1)
	slog.Info("hub: event sequence reset", "next", seq+1)
	return nil
}

// Reset clears the in-memory push callback log, which would otherwise describe
// tasks a restore has just replaced.
func (n *MockNotifier) Reset() {
	n.mu.Lock()
	n.records = nil
	n.mu.Unlock()
}
