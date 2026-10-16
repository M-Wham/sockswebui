package supervisor

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const (
	ksInterval  = 400 * time.Millisecond
	ksThreshold = 2
	streamPort  = 7000
	ksGrace     = 300 * time.Millisecond // scheduling slack under -race
)

// ksRig is one kill-switch scenario: the slot's route and endpoints, how to
// kill and restore one hop, and the hop name the echo reports when healthy.
type ksRig struct {
	outer     *testrig.WGPeer
	route     []string
	eps       []config.Endpoint
	stream    string // host:port of a TCP echo reachable through the chain
	kill      func() // takes one hop down (default outer.Pause)
	restore   func() // undoes kill (default outer.Resume)
	hop       string // echo "hop" through the healthy chain (default "wgA")
	threshold int    // health fail_threshold (default ksThreshold)
}

// serveStream runs a TCP echo (io.Copy back) on the peer netstack.
func serveStream(t *testing.T, p *testrig.WGPeer) string {
	t.Helper()
	ln, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), streamPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return net.JoinHostPort(p.TunnelIP().String(), strconv.Itoa(streamPort))
}

func wgRig(t *testing.T) ksRig {
	p := testrig.NewWGPeer(t, "wgA")
	ep := p.ClientConfig()
	ep.ID = "wgA"
	return ksRig{outer: p, route: []string{"wgA"}, eps: []config.Endpoint{ep}, stream: serveStream(t, p)}
}

func wgSocksRig(t *testing.T) ksRig {
	r := wgRig(t)
	ln, err := r.outer.Net().ListenTCPAddrPort(netip.AddrPortFrom(r.outer.TunnelIP(), 1080))
	if err != nil {
		t.Fatal(err)
	}
	up := testrig.NewSOCKS5Upstream(ln, testrig.UpstreamConfig{
		Name: "socksB", Username: "ub", Password: "pb-secret", Dial: r.outer.Net().DialContext,
		Hosts: map[string]string{testrig.EchoHost: r.outer.TunnelIP().String()},
	})
	t.Cleanup(func() { _ = up.Close() })
	r.route = []string{"wgA", "socksB"}
	r.eps = append(r.eps, config.Endpoint{ID: "socksB", Name: "socksB", Kind: config.KindSOCKS5,
		Host: r.outer.TunnelIP().String(), Port: 1080, Username: "ub", Password: "pb-secret"})
	return r
}

func TestKillSwitch(t *testing.T) {
	t.Run("WG", func(t *testing.T) { testKillSwitch(t, wgRig(t)) })
	t.Run("WG->SOCKS5", func(t *testing.T) { testKillSwitch(t, wgSocksRig(t)) })
	// Fail closed on long routes: killing any hop of a three-hop route sends nothing to
	// the device route (rig: fail_threshold 1).
	t.Run("WG->WG->SOCKS5", func(t *testing.T) {
		for hop := 1; hop <= 3; hop++ {
			t.Run("kill-hop-"+strconv.Itoa(hop), func(t *testing.T) { testKillSwitch(t, threeHopRig(t, hop)) })
		}
	})
}

// threeHopRig is [wgA, wgB, socksB]: wgB's UDP socket lives on wgA's
// netstack (as in internal/egress/wgnested_test.go), socksB listens on wgB's
// netstack and dials onward from there, and the stream echo sits on wgB.
// kill takes down hop k (1-based): pause wgA, pause wgB, or make socksB
// refuse every connection.
func threeHopRig(t *testing.T, k int) ksRig {
	t.Helper()
	a := testrig.NewWGPeer(t, "wgA")
	b := testrig.NewWGPeer(t, "wgB",
		testrig.WithTunnelIPs(netip.MustParseAddr("10.65.0.1"), netip.MustParseAddr("10.65.0.2")),
		testrig.WithHostNet(a.Net(), a.TunnelIP()))
	epA, epB := a.ClientConfig(), b.ClientConfig()
	epA.ID, epB.ID = "wgA", "wgB"
	ln, err := b.Net().ListenTCPAddrPort(netip.AddrPortFrom(b.TunnelIP(), 1080))
	if err != nil {
		t.Fatal(err)
	}
	gate := &gateListener{Listener: ln}
	up := testrig.NewSOCKS5Upstream(gate, testrig.UpstreamConfig{
		Name: "socksB", Username: "ub", Password: "pb-secret", Dial: b.Net().DialContext,
		Hosts: map[string]string{testrig.EchoHost: b.TunnelIP().String()},
	})
	t.Cleanup(func() { _ = up.Close() })
	r := ksRig{
		outer: a, route: []string{"wgA", "wgB", "socksB"}, stream: serveStream(t, b), hop: "wgB", threshold: 1,
		eps: []config.Endpoint{epA, epB, {ID: "socksB", Name: "socksB", Kind: config.KindSOCKS5,
			Host: b.TunnelIP().String(), Port: 1080, Username: "ub", Password: "pb-secret"}},
	}
	switch k {
	case 1:
		r.kill, r.restore = a.Pause, a.Resume
	case 2:
		r.kill, r.restore = b.Pause, b.Resume
	case 3:
		r.kill, r.restore = func() { gate.down.Store(true) }, func() { gate.down.Store(false) }
	default:
		t.Fatalf("no hop %d in a three-hop route", k)
	}
	return r
}

func testKillSwitch(t *testing.T, r ksRig) {
	if r.kill == nil {
		r.kill, r.restore = r.outer.Pause, r.outer.Resume
	}
	if r.hop == "" {
		r.hop = "wgA"
	}
	if r.threshold == 0 {
		r.threshold = ksThreshold
	}
	sentinel := testrig.NewSentinel(t)
	noLeak := func(when string) {
		t.Helper()
		if b, c := sentinel.Bytes(), sentinel.Conns(); b != 0 || c != 0 {
			t.Fatalf("%s: direct sentinel saw %d bytes / %d conns, want 0", when, b, c)
		}
	}
	var probes atomic.Int32
	prober := func(ctx context.Context, d Dialer, u string, to time.Duration) (Result, error) {
		probes.Add(1)
		return Probe(ctx, d, u, to)
	}
	cfg := rigSlot(1, r.route...)
	cfg.Health.FailThreshold = r.threshold
	s, ev, _ := newTestSlot(t, cfg, r.eps, withProber(prober), withBuilder(loopbackDirectBuilder),
		withInterval(ksInterval), withBackoff(200*time.Millisecond, time.Second))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)
	// Anti-vacuity: the full chain carries traffic before any hop dies.
	if got := socksEcho(t, s.Addr()); got["hop"] != r.hop {
		t.Fatalf("echo while UP = %v, want hop=%s", got, r.hop)
	}

	// A long-lived connection streaming through the slot.
	rep, c := socksConnect(t, s.Addr(), r.stream)
	if rep != 0 {
		t.Fatalf("stream CONNECT reply = %#x, want 0x00", rep)
	}
	_ = c.SetDeadline(time.Time{})
	streamEnded := make(chan struct{})
	var rounds atomic.Int32
	go func() {
		defer close(streamEnded)
		buf := make([]byte, 4)
		for {
			if _, err := c.Write([]byte("ping")); err != nil {
				return
			}
			if _, err := io.ReadFull(c, buf); err != nil {
				return
			}
			rounds.Add(1)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	waitFor(t, "stream round trips", 5*time.Second, func() bool { return rounds.Load() >= 3 })
	noLeak("while UP")

	// Keep CONNECTing to the direct sentinel from before the hop dies until
	// after recovery: under "block" not one byte may reach it.
	stopHammer := sentinelHammer(s.Addr(), sentinel.Addr())
	defer func() {
		if stopHammer != nil {
			stopHammer()
		}
	}()

	// DEGRADED within fail_threshold × interval + probe timeout of the kill
	// (polled rather than slept). The probe timeout is ksInterval
	// (withInterval). probeLoop waits the interval after each probe, so every
	// failure costs interval + timeout: threshold × (interval + timeout), which
	// for the three-hop rig (threshold 1) is exactly the documented bound. ksGrace
	// covers a probe in flight at the kill and scheduling under -race.
	probes.Store(0)
	killed := time.Now()
	r.kill()
	bound := time.Duration(r.threshold)*(ksInterval+ksInterval) + ksGrace
	waitFor(t, "DEGRADED", bound, func() bool { st, _ := s.State(); return st == StateDegraded })
	if el := time.Since(killed); el > bound {
		t.Fatalf("DEGRADED %v after the kill, want within %v", el, bound)
	}
	if n := probes.Load(); n > int32(r.threshold)+1 {
		t.Fatalf("DEGRADED after %d probes, want within fail_threshold=%d (+1 in flight)", n, r.threshold)
	}
	select {
	case <-streamEnded:
	case <-time.After(3 * time.Second):
		t.Fatal("existing proxied connection not closed on DEGRADED")
	}
	if rep, _ := socksConnect(t, s.Addr(), sentinel.Addr()); rep != 0x01 {
		t.Fatalf("CONNECT while DEGRADED reply = %#x, want 0x01", rep)
	}
	if st, _ := s.State(); st != StateDegraded {
		t.Fatalf("state = %s, want DEGRADED while hop is down", st)
	}
	noLeak("while DEGRADED")

	r.restore()
	waitFor(t, "UP after restore", 15*time.Second, func() bool { st, _ := s.State(); return st == StateUp })
	if got := socksEcho(t, s.Addr()); got["hop"] != r.hop {
		t.Fatalf("echo after recovery = %v, want hop=%s", got, r.hop)
	}
	time.Sleep(3 * ksInterval) // let the hammer run against the recovered slot
	stopHammer()
	stopHammer = nil
	noLeak("after recovery")
}

// waitFor polls cond until it holds or timeout.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
