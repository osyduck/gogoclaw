package events

import "sync"

// Event is a realtime notification pushed to dashboard subscribers.
type Event struct {
	Type   string `json:"type"`
	Email  string `json:"email,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Bus is an in-process fan-out pub/sub. Publish never blocks: a subscriber
// whose buffer is full drops the event.
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func New() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

// Subscribe returns a receive channel and an unsubscribe func that closes it.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, unsub
}

// Publish fans an event out to all current subscribers, skipping any whose
// buffer is full.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
