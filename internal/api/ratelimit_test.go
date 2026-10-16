package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for the rate limiter.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func withRemote(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	e.srv.loginLimiter.now = clk.now
	e.setup("hunter22-long")

	ipA := withRemote("192.0.2.10:40000")
	for i := 0; i < loginLimit; i++ {
		body := `{"password":"wrong-password"}`
		want := http.StatusUnauthorized
		if i == 2 {
			body, want = `{"password":"hunter22-long"}`, http.StatusOK // successes count too
		}
		// Vary the source port: the key is the host part only.
		rec := e.do("POST", "/api/login", body, withRemote("192.0.2.10:"+string(rune('0'+i))+"1000"))
		if rec.Code != want {
			t.Fatalf("attempt %d: status %d, want %d", i+1, rec.Code, want)
		}
		clk.advance(5 * time.Second)
	}
	rec := e.do("POST", "/api/login", `{"password":"hunter22-long"}`, ipA, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.99")
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("cookie set on rate-limited login")
	}

	if rec := e.do("POST", "/api/login", `{"password":"wrong-password"}`, withRemote("198.51.100.7:1234")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("other IP: status %d, want 401", rec.Code)
	}

	// The first attempt was 25s ago; once it is a minute old a slot frees up.
	clk.advance(loginWindow - 25*time.Second)
	if rec := e.do("POST", "/api/login", `{"password":"hunter22-long"}`, ipA); rec.Code != http.StatusOK {
		t.Fatalf("after window: status %d, want 200", rec.Code)
	}
}

func TestLoginRateLimitIPv6Host(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	for i := 0; i < loginLimit; i++ {
		e.do("POST", "/api/login", `{"password":"wrong-password"}`, withRemote("[2001:db8::1]:5000"))
	}
	if rec := e.do("POST", "/api/login", `{"password":"wrong-password"}`, withRemote("[2001:db8::1]:6000")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
}

func TestRateLimiterPrunesExpired(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(loginLimit, loginWindow, 1000)
	l.now = clk.now
	for i := 0; i < 100; i++ {
		if ok, _ := l.allow("10.0.0." + string(rune('0'+i%10)) + string(rune('0'+i/10))); !ok {
			t.Fatalf("client %d denied", i)
		}
	}
	if n := l.size(); n != 100 {
		t.Fatalf("size %d, want 100", n)
	}
	clk.advance(loginWindow + time.Second)
	l.allow("10.9.9.9")
	if n := l.size(); n != 1 {
		t.Fatalf("after prune size %d, want 1", n)
	}
}

// floodLimiter returns a login-sized limiter on a fake clock.
func floodLimiter() (*rateLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(loginLimit, loginWindow, loginMaxClients)
	l.now = clk.now
	return l, clk
}

func floodKey(i int) string { return "flood-" + strconv.Itoa(i) }

// TestRateLimiterFlood: a full table evicts instead of locking out new clients.
func TestRateLimiterFlood(t *testing.T) {
	t.Run("distinct keys then fresh key admitted", func(t *testing.T) {
		l, clk := floodLimiter()
		for i := 0; i < 5000; i++ {
			l.allow(floodKey(i))
			clk.advance(time.Millisecond)
		}
		if ok, _ := l.allow("admin"); !ok {
			t.Fatal("fresh key denied after a 5000-key flood")
		}
		if n := l.size(); n > loginMaxClients {
			t.Fatalf("size %d > cap %d", n, loginMaxClients)
		}
	})

	t.Run("at-limit key survives flood", func(t *testing.T) {
		l, clk := floodLimiter()
		for i := 0; i < loginLimit; i++ {
			l.allow("attacker")
		}
		clk.advance(time.Millisecond)
		for i := 0; i < 5000; i++ {
			l.allow(floodKey(i))
			clk.advance(time.Millisecond)
		}
		if ok, _ := l.allow("attacker"); ok {
			t.Fatal("at-limit key reset its budget by flooding")
		}
	})

	t.Run("all at limit evicts soonest-expiring", func(t *testing.T) {
		l, clk := floodLimiter()
		for i := 0; i < loginMaxClients; i++ {
			for j := 0; j < loginLimit; j++ {
				l.allow(floodKey(i))
			}
			clk.advance(time.Millisecond)
		}
		if ok, _ := l.allow("admin"); !ok {
			t.Fatal("fresh key denied with every key at its limit")
		}
		if n := l.size(); n > loginMaxClients {
			t.Fatalf("size %d > cap %d", n, loginMaxClients)
		}
		evicted := 0
		for i := 0; i < loginMaxClients; i++ {
			if ok, _ := l.peek(floodKey(i)); ok {
				evicted++
				if i != 0 {
					t.Errorf("evicted %s, want the soonest-expiring %s", floodKey(i), floodKey(0))
				}
			}
		}
		if evicted != 1 {
			t.Fatalf("evicted %d keys, want 1", evicted)
		}
	})

	t.Run("record never exceeds cap", func(t *testing.T) {
		l, clk := floodLimiter()
		for i := 0; i < 5000; i++ {
			l.record(floodKey(i))
			if n := l.size(); n > loginMaxClients {
				t.Fatalf("after %d records size %d > cap %d", i+1, n, loginMaxClients)
			}
			clk.advance(time.Millisecond)
		}
	})
}

func BenchmarkAllowFullTable(b *testing.B) {
	l, clk := floodLimiter()
	for i := 0; i < loginMaxClients; i++ {
		for j := 0; j < loginLimit; j++ {
			l.allow(floodKey(i))
		}
		clk.advance(time.Millisecond)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.allow("bench-" + strconv.Itoa(i))
	}
}

func TestHashSlotRespectsContext(t *testing.T) {
	for i := 0; i < cap(hashSlots); i++ {
		hashSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(hashSlots); i++ {
			<-hashSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	enc := "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"
	if _, err := verifyPassword(ctx, "pw", enc); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("verify with full slots: err=%v, want deadline exceeded", err)
	}
	if _, err := hashPassword(ctx, "pw", testParams); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hash with full slots: err=%v, want deadline exceeded", err)
	}
}
