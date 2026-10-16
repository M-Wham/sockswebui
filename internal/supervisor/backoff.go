package supervisor

import "time"

// Rebuild backoff defaults: 5 s doubling up to 5 min.
const (
	DefaultBackoffBase = 5 * time.Second
	DefaultBackoffCap  = 5 * time.Minute
)

// backoff yields exponentially growing waits: base, 2·base, … capped at max.
// Not safe for concurrent use; each DEGRADED episode owns one.
type backoff struct {
	base, max, next time.Duration
}

func newBackoff(base, max time.Duration) *backoff {
	if base <= 0 {
		base = DefaultBackoffBase
	}
	if max < base {
		max = base
	}
	return &backoff{base: base, max: max, next: base}
}

// Next returns the wait before the next attempt and doubles the one after.
func (b *backoff) Next() time.Duration {
	d := b.next
	b.next = min(2*b.next, b.max)
	return d
}

// Reset restarts the sequence at base.
func (b *backoff) Reset() { b.next = b.base }
