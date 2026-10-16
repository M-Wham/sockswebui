package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Login rate limit: 5 attempts per minute per client IP.
const (
	loginLimit      = 5
	loginWindow     = time.Minute
	loginMaxClients = 4096 // bounds memory under an address-spraying flood
)

// rateLimiter is a sliding-window-log limiter keyed by client IP. Every attempt counts.
type rateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	maxKeys   int
	now       func() time.Time
	hits      map[string][]time.Time
	lastPrune time.Time
}

func newRateLimiter(limit int, window time.Duration, maxKeys int) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, maxKeys: maxKeys, now: time.Now, hits: map[string][]time.Time{}}
}

// allow records an attempt for key and reports whether it may proceed. When denied, it
// also returns how long until the oldest attempt in the window expires.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastPrune) >= l.window {
		l.prune(now)
	}
	recent := l.recent(key, now)
	if len(recent) >= l.limit {
		l.hits[key] = recent
		return false, recent[0].Add(l.window).Sub(now)
	}
	l.admit(key, recent, now)
	l.hits[key] = append(recent, now)
	return true, 0
}

// peek reports whether key may proceed without recording an attempt. When denied, it
// also returns how long until the oldest attempt in the window expires.
func (l *rateLimiter) peek(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.recent(key, now)
	if len(recent) >= l.limit {
		return false, recent[0].Add(l.window).Sub(now)
	}
	return true, 0
}

// record counts one attempt for key, without checking the limit.
func (l *rateLimiter) record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.recent(key, now)
	l.admit(key, recent, now)
	l.hits[key] = append(recent, now)
}

// admit makes room for key before an attempt is stored. A key with live attempts is
// already in the table. For a new key on a full table, one pass prunes expired keys
// and, if none expired, evicts exactly one key, so memory stays bounded
// and a flood never locks out a new client.
func (l *rateLimiter) admit(key string, recent []time.Time, now time.Time) {
	if len(recent) > 0 {
		return
	}
	delete(l.hits, key)
	if len(l.hits) < l.maxKeys {
		return
	}
	if victim, ok := l.pruneOrPick(now); ok {
		delete(l.hits, victim)
	}
}

// pruneOrPick prunes like prune. If nothing expired it returns the key to evict: the
// under-limit key with the oldest most-recent attempt (E1(b)); only if every key is at
// its limit, the at-limit key whose block expires soonest (E1(c)).
func (l *rateLimiter) pruneOrPick(now time.Time) (string, bool) {
	var under, blocked string
	var underAt, blockedUntil time.Time
	var haveUnder, haveBlocked, pruned bool
	for k, ts := range l.hits {
		r := l.live(ts, now)
		switch {
		case len(r) == 0:
			delete(l.hits, k)
			pruned = true
			continue
		case len(r) < len(ts):
			l.hits[k] = r
		}
		if len(r) < l.limit {
			if last := r[len(r)-1]; !haveUnder || last.Before(underAt) {
				under, underAt, haveUnder = k, last, true
			}
			continue
		}
		if until := r[len(r)-l.limit].Add(l.window); !haveBlocked || until.Before(blockedUntil) {
			blocked, blockedUntil, haveBlocked = k, until, true
		}
	}
	l.lastPrune = now
	switch {
	case pruned:
		return "", false
	case haveUnder:
		return under, true
	}
	return blocked, haveBlocked
}

// recent returns key's attempts that are still inside the window.
func (l *rateLimiter) recent(key string, now time.Time) []time.Time {
	return l.live(l.hits[key], now)
}

// live returns the attempts in ts that are still inside the window.
func (l *rateLimiter) live(ts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// prune drops every key whose attempts have all expired and trims the rest.
func (l *rateLimiter) prune(now time.Time) {
	for k := range l.hits {
		if r := l.recent(k, now); len(r) == 0 {
			delete(l.hits, k)
		} else {
			l.hits[k] = r
		}
	}
	l.lastPrune = now
}

func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}

// setRetryAfter writes Retry-After as whole seconds, rounded up, at least 1.
func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
}
