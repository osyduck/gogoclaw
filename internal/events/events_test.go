package events

import (
	"testing"
	"time"
)

func TestPublishReachesSubscriber(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	defer unsub()
	b.Publish(Event{Type: "login:ok", Email: "a@x.com"})
	select {
	case ev := <-ch:
		if ev.Type != "login:ok" || ev.Email != "a@x.com" {
			t.Errorf("ev = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	unsub()
	b.Publish(Event{Type: "refresh:ok"})
	select {
	case _, open := <-ch:
		if open {
			t.Error("received event after unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		// no delivery is also acceptable
	}
}

func TestPublishDoesNotBlockOnFullSubscriber(t *testing.T) {
	b := New()
	_, unsub := b.Subscribe() // never drained
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish(Event{Type: "refresh:ok"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
}
