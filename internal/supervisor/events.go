package supervisor

import (
	"sync"
	"time"
)

// Event types pushed to the UI over SSE.
const (
	EventState      = "state"      // slot state transition (State, Msg)
	EventThroughput = "throughput" // per-slot byte counters and rates
	EventHealth     = "health"     // one health probe outcome
)

// Health is one probe outcome. It never carries the probe URL or error text,
// which could hold credentials; failures are detailed in the slot log.
type Health struct {
	OK        bool   `json:"ok"`
	ExitIP    string `json:"exit_ip,omitempty"`
	Country   string `json:"country,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

// Event is one slot event: a state transition, a health probe
// outcome or a throughput snapshot. Msg is log-safe: it never carries secrets.
type Event struct {
	Type       string      `json:"type"`
	Slot       int         `json:"slot"`
	State      State       `json:"state,omitempty"`
	Msg        string      `json:"msg,omitempty"`
	Throughput *Throughput `json:"throughput,omitempty"`
	Health     *Health     `json:"health,omitempty"`
	TS         time.Time   `json:"ts"`
}

// DefaultEventBuffer is the per-subscriber buffer used when Subscribe gets <= 0.
const DefaultEventBuffer = 64

// Bus fans events out to subscribers without ever blocking the publisher:
// a subscriber whose buffer is full misses the event.
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBus returns an empty Bus.
func NewBus() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

// Subscribe returns a channel of future events and a cancel func that
// unsubscribes and closes the channel. Cancel is idempotent.
func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	if buf <= 0 {
		buf = DefaultEventBuffer
	}
	ch := make(chan Event, buf)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
}

// Subscribers is the number of live subscriptions.
func (b *Bus) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Publish delivers e to every subscriber that has room; slow ones drop it.
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
