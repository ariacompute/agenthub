package router

import (
	"testing"
	"time"
)

func drain(ch <-chan Event, want int) []Event {
	got := make([]Event, 0, want)
	timeout := time.After(time.Second)
	for len(got) < want {
		select {
		case e, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, e)
		case <-timeout:
			return got
		}
	}
	return got
}

func TestBusDeliversOnlyToMatchingSubjects(t *testing.T) {
	b := NewBus()
	defer b.Close()

	all, cancelAll := b.Subscribe("", 4)
	defer cancelAll()
	tickets, cancelTickets := b.Subscribe("tkt-1", 4)
	defer cancelTickets()

	b.Publish(&Event{Subject: "tkt-1", Kind: EventTicketCreated})
	b.Publish(&Event{Subject: "tkt-2", Kind: EventTicketCreated})

	if got := drain(all, 2); len(got) != 2 {
		t.Fatalf("the unfiltered stream wants both frames, got %d", len(got))
	}
	if got := drain(tickets, 2); len(got) != 1 || got[0].Subject != "tkt-1" {
		t.Fatalf("a filtered stream must only see its own subject, got %+v", got)
	}
	if b.SubscriberCount() != 2 {
		t.Fatalf("subscriber count: want 2, got %d", b.SubscriberCount())
	}
}

func TestBusDropsTheOldestFrameForASlowSubscriber(t *testing.T) {
	b := NewBus()
	defer b.Close()

	ch, cancel := b.Subscribe("", 1)
	defer cancel()

	for i := 0; i < 3; i++ {
		b.Publish(&Event{Subject: "tkt-1", Seq: uint64(i + 1)})
	}

	// The buffer holds one frame and the bus keeps the newest: a dashboard that
	// cannot keep up misses history, it does not stall the writer.
	got := drain(ch, 1)
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("want the newest frame (seq 3), got %+v", got)
	}
}

func TestBusCloseReleasesSubscribers(t *testing.T) {
	b := NewBus()
	ch, _ := b.Subscribe("", 2)
	b.Close()

	if _, ok := <-ch; ok {
		t.Fatal("a closed bus must close its subscriber channels")
	}
	if b.SubscriberCount() != 0 {
		t.Fatalf("no subscriber can survive Close, got %d", b.SubscriberCount())
	}

	// Publishing after Close is a no-op, not a panic on a closed channel.
	b.Publish(&Event{Subject: "tkt-1"})
	b.Close()

	late, cancel := b.Subscribe("", 1)
	defer cancel()
	if _, ok := <-late; ok {
		t.Fatal("a subscription opened after Close must be already closed")
	}
}

func TestBusCancelIsIdempotent(t *testing.T) {
	b := NewBus()
	defer b.Close()

	ch, cancel := b.Subscribe("tkt-1", 1)
	cancel()
	cancel() // a second cancel must not double-close the channel or panic

	if _, ok := <-ch; ok {
		t.Fatal("cancelling must close the stream")
	}
	if b.SubscriberCount() != 0 {
		t.Fatalf("cancelled streams must not be counted, got %d", b.SubscriberCount())
	}
}

func TestBusDefaultsAndNilSafety(t *testing.T) {
	b := NewBus()
	defer b.Close()

	// A zero buffer would block the writer forever; the bus substitutes a default.
	ch, cancel := b.Subscribe("", 0)
	defer cancel()
	b.Publish(&Event{Subject: "tkt-1"})
	if got := drain(ch, 1); len(got) != 1 {
		t.Fatalf("a zero buffer must still deliver, got %d frames", len(got))
	}

	var nilBus *Bus
	nilBus.Publish(&Event{Subject: "tkt-1"})
	nilBus.Close()
	if nilBus.SubscriberCount() != 0 {
		t.Fatal("a nil bus has no subscribers")
	}
}
