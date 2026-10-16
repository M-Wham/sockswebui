package supervisor

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// TestSlotExplicitEmptyRoute: an explicit empty route is the direct chain,
// built once through the slot builder; it reaches loopback only via the
// test-only loopback direct chain injected through the builder seam.
func TestSlotExplicitEmptyRoute(t *testing.T) {
	t.Run("explicit empty route is direct", func(t *testing.T) {
		sentinel := testrig.NewSentinel(t)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := testrig.ServeEcho(ln, "direct")
		t.Cleanup(func() { _ = ln.Close(); <-done })
		spy := &builderSpy{inner: loopbackDirectBuilder}
		cfg := rigSlot(1)
		cfg.Route = []string{}
		cfg.Health.URL = "http://" + ln.Addr().String() + "/"
		s, ev, _ := newTestSlot(t, cfg, nil, withBuilder(spy.build))
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		waitState(t, ev, StateUp, waitUp)
		if got := spy.calls(); len(got) != 1 || len(got[0]) != 0 {
			t.Fatalf("Build calls = %q, want one call with the empty route", got)
		}
		if rep, c := socksConnect(t, s.Addr(), sentinel.Addr()); rep != 0 {
			t.Fatalf("CONNECT reply = %#x, want 0x00", rep)
		} else {
			_, _ = c.Write([]byte("hi"))
		}
		waitFor(t, "sentinel bytes", 5*time.Second, func() bool { return sentinel.Bytes() > 0 })
	})
}

// TestFallbackUsesSlotBuilder: the allow-direct fallback gets its direct
// chain from the slot's builder (one call, empty route), never from
// egress.Build directly, so the test seam covers it.
func TestFallbackUsesSlotBuilder(t *testing.T) {
	sentinel := testrig.NewSentinel(t)
	spy := &builderSpy{inner: loopbackDirectBuilder}
	cfg := rigSlot(1, "wgA")
	cfg.Killswitch = config.KillswitchAllowDirect
	s := NewSlot(cfg, nil, nil, nil, withBuilder(spy.build))
	s.mu.Lock()
	s.state = StateDegraded
	s.mu.Unlock()

	c, err := (gate{s}).DialContext(context.Background(), "tcp", sentinel.Addr())
	if err != nil {
		t.Fatalf("fallback dial: %v", err)
	}
	defer c.Close()
	if got := spy.calls(); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("Build calls = %q, want exactly one call with the empty route", got)
	}
	waitFor(t, "sentinel conn", 5*time.Second, func() bool { return sentinel.Conns() == 1 })
}

// TestEmptyRouteDeniedLoopback: on the production path (real egress.Build,
// no withBuilder) an UP empty-route slot refuses a loopback target with
// SOCKS reply 0x01 and the sentinel sees nothing: the device route never reaches loopback.
func TestEmptyRouteDeniedLoopback(t *testing.T) {
	sentinel := testrig.NewSentinel(t)
	prober := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		return Result{ExitIP: "198.51.100.7"}, nil
	}
	cfg := rigSlot(1)
	cfg.Route = []string{}
	s, ev, _ := newTestSlot(t, cfg, nil, withProber(prober))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, ev, StateUp, waitUp)
	if rep, _ := socksConnect(t, s.Addr(), sentinel.Addr()); rep != 0x01 {
		t.Fatalf("CONNECT to loopback reply = %#x, want 0x01", rep)
	}
	time.Sleep(100 * time.Millisecond)
	if b, c := sentinel.Bytes(), sentinel.Conns(); b != 0 || c != 0 {
		t.Fatalf("sentinel saw %d bytes / %d conns, want 0", b, c)
	}
}
