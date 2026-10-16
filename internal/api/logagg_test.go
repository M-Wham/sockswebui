package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type aggClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *aggClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *aggClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// lockedBuf is a goroutine-safe log sink (the ticker logs from its own goroutine).
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func newTestAgg(t *testing.T) (*logAgg, *aggClock, *lockedBuf) {
	t.Helper()
	buf := &lockedBuf{}
	clk := &aggClock{t: time.Unix(1_700_000_000, 0)}
	a := newLogAgg(slog.New(slog.NewTextHandler(buf, nil)))
	a.now = clk.now
	t.Cleanup(a.close)
	return a, clk, buf
}

// TestAuthFailLogAggregatedAPI: per (event, clientKey) and 1-minute window
// the first failure logs and the rest only count; one summary carries the count.
func TestAuthFailLogAggregatedAPI(t *testing.T) {
	if logAggWindow != time.Minute || logAggMaxKeys != 4096 {
		t.Fatalf("constants = %v / %d, want 1m / 4096", logAggWindow, logAggMaxKeys)
	}

	t.Run("handler_flood_two_lines", func(t *testing.T) {
		e := newTestEnv(t)
		e.do("POST", "/api/setup", `{"password":"hunter22-long"}`)
		e.srv.loginLimiter = newRateLimiter(1_000_000, loginWindow, loginMaxClients)
		clk := &aggClock{t: time.Unix(1_700_000_000, 0)}
		e.srv.agg.now = clk.now
		e.logs.Reset()
		remote := func(ip string) reqOpt { return func(r *http.Request) { r.RemoteAddr = ip + ":1" } }
		for range 1000 {
			e.do("POST", "/api/login", `{"password":"wrong-WRONGCANARY"}`, remote("198.51.100.7"))
		}
		if n := strings.Count(e.logs.String(), "login failed"); n != 1 {
			t.Fatalf("lines after 1000 failures = %d, want 1", n)
		}
		e.do("POST", "/api/login", `{"password":"wrong"}`, remote("198.51.100.8"))
		if n := strings.Count(e.logs.String(), "remote=198.51.100.8"); n != 1 {
			t.Fatalf("second key first failure lines = %d, want 1", n)
		}
		clk.add(logAggWindow)
		e.do("POST", "/api/login", `{"password":"wrong"}`, remote("198.51.100.7"))
		out := e.logs.String()
		if !strings.Contains(out, `msg="login failed summary" remote=198.51.100.7 count=1000`) {
			t.Fatalf("no summary with count=1000:\n%s", out)
		}
		if strings.Contains(out, "WRONGCANARY") {
			t.Fatal("password in log")
		}
	})

	t.Run("flood_one_key_two_lines", func(t *testing.T) {
		a, clk, buf := newTestAgg(t)
		logged := 0
		for range 1000 {
			if a.allow("login_failed", "198.51.100.7") {
				logged++
			}
		}
		clk.add(logAggWindow)
		a.flush(false)
		if logged != 1 || strings.Count(buf.String(), "count=1000") != 1 {
			t.Fatalf("logged=%d summaries=%q", logged, buf.String())
		}
	})

	t.Run("second_key_and_event_log_immediately", func(t *testing.T) {
		a, _, _ := newTestAgg(t)
		for range 500 {
			a.allow("login_failed", "k1")
		}
		if !a.allow("login_failed", "k2") || !a.allow("login_rate_limited", "k1") {
			t.Fatal("different key or event suppressed")
		}
	})

	t.Run("other_events_never_aggregated", func(t *testing.T) {
		a, _, _ := newTestAgg(t)
		for _, ev := range []string{"password_changed", "config_imported", "logout"} {
			for i := range 100 {
				if !a.allow(ev, "k") {
					t.Fatalf("%s event %d suppressed", ev, i)
				}
			}
		}
		if n := a.size(); n != 0 {
			t.Fatalf("entries = %d, want 0", n)
		}
	})

	t.Run("lazy_rollover", func(t *testing.T) {
		a, clk, buf := newTestAgg(t)
		for range 3 {
			a.allow("login_failed", "k")
		}
		clk.add(logAggWindow)
		if !a.allow("login_failed", "k") {
			t.Fatal("first event of new window suppressed")
		}
		if !strings.Contains(buf.String(), `msg="login failed summary" remote=k count=3`) {
			t.Fatalf("rollover summary missing: %q", buf.String())
		}
	})

	t.Run("bounded_eviction_keeps_counts", func(t *testing.T) {
		a, clk, buf := newTestAgg(t)
		a.allow("login_failed", "old")
		a.allow("login_failed", "old")
		clk.add(time.Second)
		for i := range 2 * logAggMaxKeys {
			if !a.allow("login_failed", fmt.Sprintf("10.%d.%d.1", i/256, i%256)) {
				t.Fatalf("new key %d suppressed", i)
			}
			if n := a.size(); n > logAggMaxKeys {
				t.Fatalf("entries = %d > %d", n, logAggMaxKeys)
			}
		}
		if strings.Count(buf.String(), "remote=old count=2") != 1 {
			t.Fatalf("evicted count lost")
		}
	})

	t.Run("ticker_flushes_and_stops_on_close", func(t *testing.T) {
		buf := &lockedBuf{}
		a := newLogAgg(slog.New(slog.NewTextHandler(buf, nil)))
		a.window = 30 * time.Millisecond
		a.allow("login_failed", "k")
		a.allow("login_failed", "k")
		deadline := time.Now().Add(3 * time.Second)
		for !strings.Contains(buf.String(), "remote=k count=2") {
			if time.Now().After(deadline) {
				t.Fatal("ticker never flushed")
			}
			time.Sleep(5 * time.Millisecond)
		}
		done := a.done
		a.close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ticker running after close")
		}
		a.close()
	})

	t.Run("server_close_stops_ticker", func(t *testing.T) {
		e := newTestEnv(t)
		e.do("POST", "/api/setup", `{"password":"hunter22-long"}`)
		e.do("POST", "/api/login", `{"password":"bad"}`)
		done := e.srv.agg.done
		if done == nil {
			t.Fatal("ticker not started")
		}
		_ = e.srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ticker running after Server.Close")
		}
	})
}
