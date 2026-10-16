package socks

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testIdle = 200 * time.Millisecond

// noDeadlineConn rejects read deadlines, like a conn type without deadline
// support, to exercise the watcher-goroutine fallback.
type noDeadlineConn struct{ net.Conn }

var errNoDeadline = errors.New("deadline not supported")

func (noDeadlineConn) SetReadDeadline(time.Time) error { return errNoDeadline }
func (noDeadlineConn) SetDeadline(time.Time) error     { return errNoDeadline }

// noDeadlineDialer wraps every dialed conn in noDeadlineConn.
type noDeadlineDialer struct{ *fakeDialer }

func (d noDeadlineDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := d.fakeDialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return noDeadlineConn{c}, nil
}

// openRelay runs a CONNECT through the server and returns both relay ends.
func openRelay(t *testing.T, addr string, fd *fakeDialer) (client, remote net.Conn) {
	t.Helper()
	client = dialServer(t, addr)
	if m := greet(t, client, 0x00); m != 0x00 {
		t.Fatalf("method = %#x", m)
	}
	mustWrite(t, client, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
	if rep := readReply(t, client); rep != 0x00 {
		t.Fatalf("rep = %#x, want 0x00", rep)
	}
	remote = <-fd.remotes
	t.Cleanup(func() { _ = remote.Close() })
	return client, remote
}

// expectClosedWithin fails unless c reports closed (not a timeout) within d.
func expectClosedWithin(t *testing.T, c net.Conn, d time.Duration, side string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	b := make([]byte, 1)
	for {
		_, err := c.Read(b)
		if err == nil {
			continue // drain stray payload
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("%s still open after %v of silence", side, d)
		}
		return
	}
}

func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := runtime.NumGoroutine()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settledGoroutines returns runtime.NumGoroutine once it has held steady for
// 100 ms, so goroutines still exiting from an earlier test or iteration (e.g. a
// previous server's Serve wrapper) are not counted in the baseline.
func settledGoroutines(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	last, steady := runtime.NumGoroutine(), 0
	for steady < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count did not settle (last %d)", last)
		}
		time.Sleep(5 * time.Millisecond)
		if n := runtime.NumGoroutine(); n == last {
			steady++
		} else {
			last, steady = n, 0
		}
	}
	return last
}

func idleLogLines(out string) int {
	return strings.Count(out, "level=DEBUG msg=\"socks relay idle closed\"")
}

// TestRelayIdleTimeout: relays silent in both directions close, so
// abandoned relays cannot pile up; payload in one direction keeps them open; no extra goroutine.
func TestRelayIdleTimeout(t *testing.T) {
	t.Run("silent relay closes both sides", func(t *testing.T) {
		fd := newFakeDialer()
		lc := &logCapture{}
		_, addr := startServer(t, Options{Dialer: fd, IdleTimeout: testIdle, Logger: lc.logger()})
		client, remote := openRelay(t, addr, fd)
		expectClosedWithin(t, client, time.Second, "client")
		expectClosedWithin(t, remote, time.Second, "remote")
		out := lc.waitFor(t, "socks relay idle closed")
		if n := idleLogLines(out); n != 1 {
			t.Fatalf("idle close logged %d times at DEBUG, want 1: %s", n, out)
		}
		if strings.Contains(out, "level=WARN") {
			t.Fatalf("idle close logged at WARN: %s", out)
		}
	})

	t.Run("one direction active keeps it open", func(t *testing.T) {
		for _, dir := range []string{"remote-to-client", "client-to-remote"} {
			t.Run(dir, func(t *testing.T) {
				fd := newFakeDialer()
				_, addr := startServer(t, Options{Dialer: fd, IdleTimeout: testIdle})
				client, remote := openRelay(t, addr, fd)
				src, dst := remote, client
				if dir == "client-to-remote" {
					src, dst = client, remote
				}
				_ = src.SetDeadline(time.Now().Add(10 * time.Second))
				_ = dst.SetDeadline(time.Now().Add(10 * time.Second))
				for end := time.Now().Add(2 * time.Second); time.Now().Before(end); {
					mustWrite(t, src, []byte{'.'})
					mustRead(t, dst, 1)
					time.Sleep(100 * time.Millisecond)
				}
				// The silent direction still carries data after 2 s.
				mustWrite(t, dst, []byte("ok"))
				if got := mustRead(t, src, 2); string(got) != "ok" {
					t.Fatalf("got %q", got)
				}
				expectClosedWithin(t, client, time.Second, "client")
				expectClosedWithin(t, remote, time.Second, "remote")
			})
		}
	})

	// A peer that stops reading must not pin the relay (and its per-source
	// slot) while the other direction stays active: the blocked write has a
	// deadline.
	t.Run("stalled write ends the relay", func(t *testing.T) {
		fd := newFakeDialer()
		s, addr := startServer(t, Options{Dialer: fd, IdleTimeout: testIdle})
		client, remote := openRelay(t, addr, fd)
		_ = client.SetDeadline(time.Now().Add(10 * time.Second))
		// remote never reads: the relay's write of this byte blocks (net.Pipe).
		mustWrite(t, client, []byte{'x'})
		// remote -> client stays active, so the relay is never idle.
		ended := false
		for end := time.Now().Add(10 * testIdle); time.Now().Before(end); {
			_ = remote.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := remote.Write([]byte{'.'}); err != nil {
				ended = true
				break
			}
			if _, err := client.Read(make([]byte, 1)); err != nil {
				ended = true
				break
			}
			time.Sleep(testIdle / 4)
		}
		if !ended {
			t.Fatalf("relay still open after %v with a stalled write", 10*testIdle)
		}
		expectClosedWithin(t, client, time.Second, "client")
		deadline := time.Now().Add(3 * time.Second)
		for {
			active, n := s.counts("127.0.0.1")
			if active == 0 && n == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("after stalled write: active=%d perSource=%d, want 0/0", active, n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	t.Run("two goroutines per relay, back to baseline", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd, IdleTimeout: time.Second})
		base := settledGoroutines(t)
		client, remote := openRelay(t, addr, fd)
		waitGoroutines(t, base+2) // handler + one pipe
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			if got := runtime.NumGoroutine(); got != base+2 {
				t.Fatalf("goroutines during relay = %d, want %d", got, base+2)
			}
		}
		expectClosedWithin(t, client, 2*time.Second, "client")
		expectClosedWithin(t, remote, 2*time.Second, "remote")
		waitGoroutines(t, base)
	})
}

// TestRelayIdleDeadlineFallback: a conn without read deadlines still closes on
// idle via a watcher for that relay only.
func TestRelayIdleDeadlineFallback(t *testing.T) {
	fd := newFakeDialer()
	lc := &logCapture{}
	_, addr := startServer(t, Options{Dialer: noDeadlineDialer{fd}, IdleTimeout: testIdle, Logger: lc.logger()})
	base := settledGoroutines(t)
	client, remote := openRelay(t, addr, fd)
	_ = remote.SetDeadline(time.Now().Add(10 * time.Second))
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	// Activity still keeps it open in fallback mode.
	for range 6 {
		mustWrite(t, remote, []byte{'.'})
		mustRead(t, client, 1)
		time.Sleep(100 * time.Millisecond)
	}
	mustWrite(t, client, []byte("ok"))
	if got := mustRead(t, remote, 2); string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
	expectClosedWithin(t, client, time.Second, "client")
	expectClosedWithin(t, remote, time.Second, "remote")
	waitGoroutines(t, base)
	if n := idleLogLines(lc.waitFor(t, "socks relay idle closed")); n != 1 {
		t.Fatalf("idle close logged %d times, want 1", n)
	}
}

func TestRelayIdleTimeoutDefault(t *testing.T) {
	if defaultIdleTimeout != 15*time.Minute {
		t.Fatalf("defaultIdleTimeout = %v, want 15m", defaultIdleTimeout)
	}
	s, err := New(Options{Dialer: newFakeDialer()})
	if err != nil {
		t.Fatal(err)
	}
	if s.idleTimeout != 15*time.Minute {
		t.Fatalf("idleTimeout = %v, want 15m", s.idleTimeout)
	}
	s, err = New(Options{Dialer: newFakeDialer(), IdleTimeout: testIdle})
	if err != nil {
		t.Fatal(err)
	}
	if s.idleTimeout != testIdle {
		t.Fatalf("idleTimeout = %v, want %v", s.idleTimeout, testIdle)
	}
}
