package socks

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Log aggregation constants: a flood of rejected clients must not push
// important lines out of the bounded slot log.
const (
	logAggWindow  = time.Minute // per (event, sourceKey) window
	logAggMaxKeys = 1024        // table bound per Server
)

// aggregated lists the only events whose lines are aggregated, with the level of
// their summary line: every handshake reject, request reject and failed CONNECT
// dial (a client, unauthenticated on a no-auth slot, can cause them at will) and
// max_conns. io_error and handshake_timeout share one window whether they hit
// the handshake or the request phase.
// Anything else (fallback, kill switch, state lines) always logs.
var aggregated = map[string]slog.Level{
	"auth_failed":          slog.LevelInfo,
	"auth_throttled":       slog.LevelInfo,
	"bad_version":          slog.LevelInfo,
	"io_error":             slog.LevelInfo,
	"no_acceptable_method": slog.LevelInfo,
	"bad_auth_version":     slog.LevelInfo,
	"handshake_timeout":    slog.LevelInfo,
	"cmd_not_supported":    slog.LevelInfo,
	"atyp_not_supported":   slog.LevelInfo,
	"bad_address":          slog.LevelInfo,
	"dial_failed":          slog.LevelInfo,
	"max_conns":            slog.LevelWarn,
}

type aggKey struct{ event, key string }

type aggEntry struct {
	start time.Time // window start: the event that was logged
	last  time.Time // most recent event, for least-recently-seen eviction
	count int       // events in this window, the logged one included
	err   string    // latest redacted error text, carried into the summary
}

// logAgg suppresses repeats of noisy per-source log lines: per (event, sourceKey)
// and window, the first event logs and the rest only count; when the window ends
// (seen lazily on the next event, by a ticker, or at close) one summary line
// carries the count. The table holds at most logAggMaxKeys entries (on a
// full table: prune ended windows, else evict the least recently seen; never drop a count).
type logAgg struct {
	log     *slog.Logger
	now     func() time.Time
	window  time.Duration
	maxKeys int

	mu      sync.Mutex
	ents    map[aggKey]*aggEntry
	running bool // ticker goroutine started
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

type aggSummary struct {
	k   aggKey
	n   int
	err string
}

func newLogAgg(log *slog.Logger) *logAgg {
	return &logAgg{
		log: log, now: time.Now, window: logAggWindow, maxKeys: logAggMaxKeys,
		ents: map[aggKey]*aggEntry{},
	}
}

// allow records one event for key and reports whether its line should be logged.
func (a *logAgg) allow(event, key string) bool { return a.allowErr(event, key, "") }

// allowErr is allow with an already-redacted error text; the summary carries
// the latest one seen in the window.
func (a *logAgg) allowErr(event, key, errText string) bool {
	if _, ok := aggregated[event]; !ok {
		return true
	}
	var out []aggSummary
	defer func() { a.emit(out) }()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return true // no ticker after close: log rather than hold a count nobody flushes
	}
	now := a.now()
	k := aggKey{event, key}
	if e, ok := a.ents[k]; ok {
		if now.Sub(e.start) < a.window {
			e.count++
			e.last = now
			if errText != "" {
				e.err = errText
			}
			return false
		}
		out = appendSummary(out, k, e)
		delete(a.ents, k)
	} else if len(a.ents) >= a.maxKeys {
		out = a.makeRoom(now, out)
	}
	a.ents[k] = &aggEntry{start: now, last: now, count: 1, err: errText}
	a.startTicker()
	return true
}

// makeRoom prunes ended windows; if none ended it evicts the least recently
// seen entry. Either way the dropped counts become summaries.
func (a *logAgg) makeRoom(now time.Time, out []aggSummary) []aggSummary {
	var victim aggKey
	var victimLast time.Time
	have, pruned := false, false
	for k, e := range a.ents {
		if now.Sub(e.start) >= a.window {
			out = appendSummary(out, k, e)
			delete(a.ents, k)
			pruned = true
			continue
		}
		if !have || e.last.Before(victimLast) {
			victim, victimLast, have = k, e.last, true
		}
	}
	if !pruned && have {
		out = appendSummary(out, victim, a.ents[victim])
		delete(a.ents, victim)
	}
	return out
}

func appendSummary(out []aggSummary, k aggKey, e *aggEntry) []aggSummary {
	if e.count > 1 {
		out = append(out, aggSummary{k, e.count, e.err})
	}
	return out
}

// flush ends every window that is over (all of them when force) and logs their
// summaries.
func (a *logAgg) flush(force bool) {
	var out []aggSummary
	a.mu.Lock()
	now := a.now()
	for k, e := range a.ents {
		if force || now.Sub(e.start) >= a.window {
			out = appendSummary(out, k, e)
			delete(a.ents, k)
		}
	}
	a.mu.Unlock()
	a.emit(out)
}

// emit logs summaries outside the lock. peer_key is the sourceKey: never a
// username or password, so client-supplied bytes never reach the log.
func (a *logAgg) emit(out []aggSummary) {
	for _, s := range out {
		args := []any{"peer_key", s.k.key, "count", s.n}
		if s.err != "" {
			args = append(args, "err", s.err)
		}
		a.log.Log(context.Background(), aggregated[s.k.event], "socks "+s.k.event+" summary", args...)
	}
}

// startTicker starts the flush goroutine on first use. Callers hold a.mu.
func (a *logAgg) startTicker() {
	if a.running {
		return
	}
	a.running = true
	a.stop, a.done = make(chan struct{}), make(chan struct{})
	go a.tick(a.window, a.stop, a.done)
}

func (a *logAgg) tick(every time.Duration, stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			a.flush(false)
		case <-stop:
			return
		}
	}
}

// close stops the ticker, waits for it, and logs every pending summary. It is
// idempotent; after close every event logs (nothing is held back).
func (a *logAgg) close() {
	a.mu.Lock()
	a.closed = true
	stop, done, running := a.stop, a.done, a.running
	a.running = false
	a.mu.Unlock()
	if running {
		close(stop)
		<-done
	}
	a.flush(true)
}

func (a *logAgg) size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.ents)
}
