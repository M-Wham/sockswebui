package socks

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock for the aggregator.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newTestAgg returns an aggregator on a fake clock logging into lc.
func newTestAgg(t *testing.T) (*logAgg, *fakeClock, *logCapture) {
	t.Helper()
	lc := &logCapture{}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	a := newLogAgg(lc.logger())
	a.now = clk.now
	t.Cleanup(a.close)
	return a, clk, lc
}

// recs returns the raw records captured so far.
func (lc *logCapture) records() []string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return append([]string(nil), lc.recs...)
}

func countRecs(recs []string, sub string) int {
	n := 0
	for _, r := range recs {
		if strings.Contains(r, sub) {
			n++
		}
	}
	return n
}

// waitRec polls until a raw record contains sub (the text sink may be ahead).
func waitRec(t *testing.T, lc *logCapture, sub string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for countRecs(lc.records(), sub) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no record contains %q: %v", sub, lc.records())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitAggCount polls until the open window for (event, key) has counted want events.
func waitAggCount(t *testing.T, a *logAgg, event, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.mu.Lock()
		got := 0
		if e, ok := a.ents[aggKey{event, key}]; ok {
			got = e.count
		}
		a.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s count for %s = %d, want %d", event, key, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAuthFailLogAggregated: a reject flood cannot flood the slot log. Per (event, sourceKey)
// and 1-minute window the first event logs, the rest only count, and one summary
// line carries the count; aggregation never touches other events.
func TestAuthFailLogAggregated(t *testing.T) {
	if logAggWindow != time.Minute || logAggMaxKeys != 1024 {
		t.Fatalf("constants = %v / %d, want 1m / 1024", logAggWindow, logAggMaxKeys)
	}

	t.Run("flood_one_key_two_lines", func(t *testing.T) {
		a, clk, lc := newTestAgg(t)
		logged := 0
		for range 10_000 {
			if a.allow("auth_failed", "198.51.100.7") {
				logged++
			}
		}
		if logged != 1 {
			t.Fatalf("first-line allows = %d, want 1", logged)
		}
		clk.add(logAggWindow)
		a.flush(false)
		recs := lc.records()
		if len(recs)+logged > 2 {
			t.Fatalf("10000 failures produced %d lines, want <= 2: %v", len(recs)+logged, recs)
		}
		want := "socks auth_failed summary peer_key=198.51.100.7 count=10000"
		if len(recs) != 1 || !strings.Contains(recs[0], want) {
			t.Fatalf("summary = %v, want %q", recs, want)
		}
	})

	t.Run("second_key_logs_immediately", func(t *testing.T) {
		a, _, _ := newTestAgg(t)
		for range 500 {
			a.allow("auth_failed", "198.51.100.7")
		}
		if !a.allow("auth_failed", "198.51.100.8") {
			t.Fatal("a second key's first failure was suppressed")
		}
		if !a.allow("auth_throttled", "198.51.100.7") {
			t.Fatal("a new event for a flooding key was suppressed")
		}
	})

	t.Run("only_listed_events", func(t *testing.T) {
		a, _, lc := newTestAgg(t)
		// Every handshake reject, request reject and dial failure is aggregated: an
		// unauthenticated flood must not push kill-switch or fallback lines out of the slot ring.
		agg := []string{"auth_failed", "auth_throttled", "max_conns",
			"bad_version", "io_error", "no_acceptable_method", "bad_auth_version", "handshake_timeout",
			"cmd_not_supported", "atyp_not_supported", "bad_address", "dial_failed"}
		for _, ev := range agg {
			a.allow(ev, "k")
			if a.allow(ev, "k") {
				t.Fatalf("%s not aggregated", ev)
			}
		}
		for _, ev := range []string{"fallback", "killswitch", "slot_state"} {
			for i := range 100 {
				if !a.allow(ev, "k") {
					t.Fatalf("%s event %d suppressed; only SOCKS rejects and max_conns aggregate", ev, i)
				}
			}
		}
		if n := a.size(); n != len(agg) {
			t.Fatalf("entries = %d, want %d (non-aggregated events keep no state)", n, len(agg))
		}
		a.close()
		if got := countRecs(lc.records(), "summary"); got != len(agg) {
			t.Fatalf("summaries at close = %d, want %d: %v", got, len(agg), lc.records())
		}
	})

	t.Run("summary_carries_latest_err", func(t *testing.T) {
		a, _, lc := newTestAgg(t)
		a.allowErr("dial_failed", "k", "egress: hop wgA: [REDACTED] first")
		a.allowErr("dial_failed", "k", "egress: hop wgA: [REDACTED] last")
		a.close()
		recs := lc.records()
		want := "socks dial_failed summary peer_key=k count=2 err=egress: hop wgA: [REDACTED] last"
		if len(recs) != 1 || !strings.Contains(recs[0], want) {
			t.Fatalf("summary = %v, want %q", recs, want)
		}
	})

	t.Run("lazy_rollover", func(t *testing.T) {
		a, clk, lc := newTestAgg(t)
		a.allow("auth_failed", "k")
		a.allow("auth_failed", "k")
		a.allow("auth_failed", "k")
		clk.add(logAggWindow)
		if !a.allow("auth_failed", "k") {
			t.Fatal("first event of a new window suppressed")
		}
		recs := lc.records()
		if len(recs) != 1 || !strings.Contains(recs[0], "socks auth_failed summary peer_key=k count=3") {
			t.Fatalf("rollover summary = %v", recs)
		}
	})

	t.Run("single_event_no_summary", func(t *testing.T) {
		a, clk, lc := newTestAgg(t)
		a.allow("max_conns", "k")
		clk.add(logAggWindow)
		a.flush(false)
		if recs := lc.records(); len(recs) != 0 {
			t.Fatalf("summary for a lone event: %v", recs)
		}
		if n := a.size(); n != 0 {
			t.Fatalf("expired entries kept: %d", n)
		}
	})

	t.Run("bounded_keys_eviction_keeps_counts", func(t *testing.T) {
		a, clk, lc := newTestAgg(t)
		a.allow("auth_failed", "old")
		a.allow("auth_failed", "old") // count 2, least recently seen
		clk.add(time.Second)
		for i := range 2 * logAggMaxKeys {
			if !a.allow("auth_failed", fmt.Sprintf("10.0.%d.%d", i/256, i%256)) {
				t.Fatalf("new key %d suppressed on a full table (eviction admits it)", i)
			}
			if n := a.size(); n > logAggMaxKeys {
				t.Fatalf("entries = %d, want <= %d", n, logAggMaxKeys)
			}
		}
		if got := countRecs(lc.records(), "summary peer_key=old count=2"); got != 1 {
			t.Fatalf("evicted entry's count lost: %v", lc.records())
		}
	})

	t.Run("ticker_flushes_and_stops_on_close", func(t *testing.T) {
		lc := &logCapture{}
		a := newLogAgg(lc.logger())
		a.window = 30 * time.Millisecond
		a.allow("auth_failed", "k")
		a.allow("auth_failed", "k")
		lc.waitFor(t, "socks auth_failed summary peer_key=k count=2")
		done := a.done
		a.close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ticker goroutine still running after close")
		}
		a.close() // idempotent
		if a.allow("auth_failed", "j"); a.running {
			t.Fatal("ticker restarted after close")
		}
	})

	t.Run("server", func(t *testing.T) {
		userC := canary("UCANARYz", 255)
		passC := canary("PCANARYz", 255)
		var lc logCapture
		s, addr := startServer(t, Options{Dialer: newFakeDialer(), Username: authLogUser, Password: authLogPass, Logger: lc.logger()})
		const n = 60
		for range n {
			_ = authFrom(t, addr, "127.0.0.1", userC, passC)
		}
		waitRec(t, &lc, "reason=auth_throttled")
		if st := authFrom(t, addr, "127.0.0.2", userC, passC); st != 0x01 {
			t.Fatalf("second source status = %#x", st)
		}
		waitRec(t, &lc, "peer=127.0.0.2")
		recs := lc.records()
		if got := countRecs(recs, "reason=auth_failed"); got != 2 {
			t.Fatalf("auth_failed lines = %d, want 2 (one per source): %v", got, recs)
		}
		if got := countRecs(recs, "reason=auth_throttled"); got != 1 {
			t.Fatalf("auth_throttled lines = %d, want 1: %v", got, recs)
		}
		_ = s.Close()
		out := lc.all()
		for _, want := range []string{
			fmt.Sprintf("socks auth_failed summary peer_key=127.0.0.1 count=%d", authFailLimit),
			fmt.Sprintf("socks auth_throttled summary peer_key=127.0.0.1 count=%d", n-authFailLimit),
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q at Close: %s", want, out)
			}
		}
		assertNoCanary(t, out, "username", userC)
		assertNoCanary(t, out, "password", passC)
		if s.agg.running {
			t.Fatal("aggregator ticker running after Server.Close")
		}
	})

	t.Run("server_max_conns", func(t *testing.T) {
		var lc logCapture
		s, addr := startServer(t, Options{Dialer: newFakeDialer(), MaxConns: 2, Logger: lc.logger()})
		held := dialFrom(t, "127.0.0.1", addr)
		defer held.Close()
		waitSourceCount(t, s, "127.0.0.1", 1)
		const n = 30
		for range n {
			c := dialFrom(t, "127.0.0.1", addr)
			expectClosed(t, c)
		}
		// The accept loop closes a refused conn before counting it, so wait for
		// the last refusal to be counted before Close flushes the summary.
		waitAggCount(t, s.agg, "max_conns", "127.0.0.1", n)
		if got := countRecs(lc.records(), "reason=max_conns"); got != 1 {
			t.Fatalf("max_conns lines = %d, want 1: %v", got, lc.records())
		}
		_ = s.Close()
		lc.waitFor(t, fmt.Sprintf("socks max_conns summary peer_key=127.0.0.1 count=%d", n))
	})
}
