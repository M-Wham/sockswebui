package supervisor

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// TestAllowDirectThreeHop: an allow-direct slot on
// [wgA, wgB, socksB] falls back to the device route only while DEGRADED,
// whichever hop dies, and drops every fallback conn once the chain is back.
// loopbackDirectBuilder lets the fallback reach the loopback sentinel, so a
// gate that fell back at the wrong time would show up as sentinel traffic.
func TestAllowDirectThreeHop(t *testing.T) {
	for hop := 1; hop <= 3; hop++ {
		t.Run("kill-hop-"+strconv.Itoa(hop), func(t *testing.T) { testAllowDirectThreeHop(t, threeHopRig(t, hop)) })
	}
}

func testAllowDirectThreeHop(t *testing.T, r ksRig) {
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(1, r.route...)
	cfg.Killswitch = config.KillswitchAllowDirect
	cfg.Health.FailThreshold = r.threshold
	s, ev, _ := newTestSlot(t, cfg, r.eps, withBuilder(loopbackDirectBuilder),
		withInterval(ksInterval), withBackoff(200*time.Millisecond, time.Second))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)

	// (1) UP: the chain carries traffic, the sentinel target is dialed
	// through it (host loopback is unreachable from wgB's netstack), never
	// via the device route.
	if got := socksEcho(t, s.Addr()); got["hop"] != r.hop {
		t.Fatalf("echo while UP = %v, want hop=%s", got, r.hop)
	}
	trySend(s.Addr(), sentinel.Addr(), []byte("tunnel"))
	time.Sleep(100 * time.Millisecond)
	if b, c := sentinel.Bytes(), sentinel.Conns(); b != 0 || c != 0 {
		t.Fatalf("UP: sentinel saw %d bytes / %d conns, want 0", b, c)
	}
	if n := len(fallbackLines(s)); n != 0 {
		t.Fatalf("UP: %d fallback lines, want 0", n)
	}

	// (2) Kill hop k. Until DEGRADED, CONNECTs to the sentinel keep going
	// to the (dead) chain or get 0x01; the hammer stops itself on DEGRADED.
	r.kill()
	attempts := preDegradedHammer(s, sentinel.Addr())
	waitFor(t, "DEGRADED", 10*time.Second, func() bool { st, _ := s.State(); return st == StateDegraded })
	if n := attempts(); n == 0 {
		t.Fatal("no CONNECT attempted between the kill and DEGRADED (vacuous)")
	}
	assertFallbackOnlyInDegraded(t, s)

	f0, c0 := len(fallbackLines(s)), sentinel.Conns()
	rep, fb := socksConnect(t, s.Addr(), sentinel.Addr())
	if rep != 0 {
		t.Fatalf("CONNECT while DEGRADED reply = %#x, want 0x00 via fallback", rep)
	}
	if _, err := fb.Write([]byte("direct")); err != nil {
		t.Fatalf("write via fallback: %v", err)
	}
	waitFor(t, "fallback conn at sentinel", 5*time.Second, func() bool { return sentinel.Conns() == c0+1 })
	if d := len(fallbackLines(s)) - f0; d != 1 {
		t.Fatalf("fallback lines for one CONNECT = %d, want 1: %v", d, fallbackLines(s))
	}
	if !strings.Contains(fallbackLines(s)[f0].Text, "target="+sentinel.Addr()) {
		t.Fatalf("fallback line lacks target %s: %q", sentinel.Addr(), fallbackLines(s)[f0].Text)
	}

	// (3) Restore hop k: UP again, every fallback conn closed (polled),
	// and new CONNECTs ride the chain.
	r.restore()
	waitFor(t, "UP after restore", 15*time.Second, func() bool { st, _ := s.State(); return st == StateUp })
	waitFor(t, "fallback conns closed at sentinel", 2*time.Second, func() bool { return sentinel.Open() == 0 })
	if got := socksEcho(t, s.Addr()); got["hop"] != r.hop {
		t.Fatalf("echo after restore = %v, want hop=%s", got, r.hop)
	}
	fAfter, cAfter := len(fallbackLines(s)), sentinel.Conns()
	trySend(s.Addr(), sentinel.Addr(), []byte("tunnel"))
	time.Sleep(200 * time.Millisecond)
	if c := sentinel.Conns(); c != cAfter {
		t.Fatalf("after restore sentinel conns %d → %d, want no device-route dial", cAfter, c)
	}
	if n := len(fallbackLines(s)); n != fAfter {
		t.Fatalf("fallback lines after restore %d → %d, want none new", fAfter, n)
	}
	// Every device-route conn the sentinel ever saw was a logged fallback.
	if c, f := sentinel.Conns(), int64(len(fallbackLines(s))); c != f {
		t.Fatalf("sentinel conns = %d, fallback lines = %d, want equal", c, f)
	}
	assertFallbackOnlyInDegraded(t, s)
}

// preDegradedHammer CONNECTs to target through the slot until the slot is
// DEGRADED, and returns a func reporting how many attempts were made.
func preDegradedHammer(s *Slot, target string) func() int {
	var n atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if st, _ := s.State(); st != StateUp {
				return
			}
			n.Add(1)
			if rep, c, err := socksTry(s.Addr(), target, 300*time.Millisecond); err == nil && rep == 0 {
				_, _ = c.Write([]byte("leak"))
				_ = c.Close()
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	return func() int { <-done; return int(n.Load()) }
}

// assertFallbackOnlyInDegraded walks the slot log in order and fails if a
// "fallback" line appears while the last logged state is not DEGRADED.
func assertFallbackOnlyInDegraded(t *testing.T, s *Slot) {
	t.Helper()
	cur := ""
	for _, l := range s.Logs() {
		switch {
		case l.Msg == "slot state":
			_, after, _ := strings.Cut(l.Text, "to=")
			cur, _, _ = strings.Cut(after, " ")
		case l.Msg == "fallback" && cur != string(StateDegraded):
			t.Fatalf("fallback line while state %q: %q", cur, l.Text)
		}
	}
	if cur == "" {
		t.Fatal("no state transitions in the slot log")
	}
}
