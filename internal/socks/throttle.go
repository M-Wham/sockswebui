package socks

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// SOCKS auth failure throttle constants: they bound password guessing per source.
const (
	authFailLimit   = 10          // failures per source key per window
	authFailWindow  = time.Minute // sliding window
	authFailMaxKeys = 1024        // table bound per Server
)

// sourceKey is the throttle key for a client remote address (a client is one
// IPv4 address or one IPv6 /64, since one host usually owns a whole /64). The host part is parsed as an IP and unmapped first, so
// IPv4-mapped IPv6 keys by its full IPv4 address; any other IPv6 keys by its /64 prefix
// with the zone dropped. An unparsable address is used verbatim so it never shares a
// budget with other clients. This copies the rule of internal/api clientKey so both
// limiters key clients the same way; the shared test table guards drift.
func sourceKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return remote
	}
	a = a.Unmap() // must precede the Is4 check: a mapped IPv4 is not an IPv6 client
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a.WithZone(""), 64).Masked().String()
}

// throttle is a per-Server sliding-window log of auth failures keyed by source.
// Only failures consume budget: reserve counts an attempt up front (so concurrent
// handshakes cannot exceed the limit) and refund removes it on success.
type throttle struct {
	mu        sync.Mutex
	now       func() time.Time
	fails     map[string][]time.Time
	lastPrune time.Time
}

func newThrottle() *throttle {
	return &throttle{now: time.Now, fails: map[string][]time.Time{}}
}

// reserve counts one failure for key unless key is at its limit. It returns the
// timestamp to pass to refund and whether the credentials may be compared.
func (t *throttle) reserve(key string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if now.Sub(t.lastPrune) >= authFailWindow {
		t.prune(now)
	}
	recent := t.live(t.fails[key], now)
	if len(recent) >= authFailLimit {
		t.fails[key] = recent
		return time.Time{}, false
	}
	t.add(key, recent, now)
	return now, true
}

// fail records one failure for key without checking the limit.
func (t *throttle) fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.add(key, t.live(t.fails[key], now), now)
}

// refund removes the attempt reserved at `at` after a successful auth.
func (t *throttle) refund(key string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts := t.fails[key]
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].Equal(at) {
			ts = append(ts[:i:i], ts[i+1:]...)
			break
		}
	}
	if len(ts) == 0 {
		delete(t.fails, key)
		return
	}
	t.fails[key] = ts
}

// add stores an attempt for key, first making room for a new key on a full table:
// prune expired keys, else evict one key (see pruneOrPick).
func (t *throttle) add(key string, recent []time.Time, now time.Time) {
	if len(recent) == 0 {
		delete(t.fails, key)
		if len(t.fails) >= authFailMaxKeys {
			if victim, ok := t.pruneOrPick(now); ok {
				delete(t.fails, victim)
			}
		}
	}
	t.fails[key] = append(recent, now)
}

// pruneOrPick prunes expired keys. If none expired it returns the key to evict: the
// under-limit key with the oldest most-recent failure; only if every key is at
// its limit, the at-limit key whose block expires soonest, so a blocked source
// cannot clear its block by flooding the table with new keys.
func (t *throttle) pruneOrPick(now time.Time) (string, bool) {
	var under, blocked string
	var underAt, blockedUntil time.Time
	var haveUnder, haveBlocked, pruned bool
	for k, ts := range t.fails {
		r := t.live(ts, now)
		switch {
		case len(r) == 0:
			delete(t.fails, k)
			pruned = true
			continue
		case len(r) < len(ts):
			t.fails[k] = r
		}
		if len(r) < authFailLimit {
			if last := r[len(r)-1]; !haveUnder || last.Before(underAt) {
				under, underAt, haveUnder = k, last, true
			}
			continue
		}
		if until := r[len(r)-authFailLimit].Add(authFailWindow); !haveBlocked || until.Before(blockedUntil) {
			blocked, blockedUntil, haveBlocked = k, until, true
		}
	}
	t.lastPrune = now
	switch {
	case pruned:
		return "", false
	case haveUnder:
		return under, true
	}
	return blocked, haveBlocked
}

// live returns the attempts in ts that are still inside the window.
func (t *throttle) live(ts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-authFailWindow)
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// prune drops every key whose failures have all expired and trims the rest.
func (t *throttle) prune(now time.Time) {
	for k, ts := range t.fails {
		if r := t.live(ts, now); len(r) == 0 {
			delete(t.fails, k)
		} else {
			t.fails[k] = r
		}
	}
	t.lastPrune = now
}

func (t *throttle) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.fails)
}
