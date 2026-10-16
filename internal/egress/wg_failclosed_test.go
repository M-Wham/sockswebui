package egress

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/testrig"
)

// blockDeviceDNS makes both the process resolver and egress's device-lookup
// seam fail and count. Not parallel-safe: it swaps process-wide state.
func blockDeviceDNS(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	origResolver, origLookup := net.DefaultResolver, deviceLookup
	t.Cleanup(func() { net.DefaultResolver, deviceLookup = origResolver, origLookup })
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		n.Add(1)
		return nil, errors.New("blocked")
	}}
	deviceLookup = func(context.Context, string) ([]netip.Addr, error) {
		n.Add(1)
		return nil, errors.New("blocked")
	}
	return &n
}

// TestWGNoDNS: a WG last hop with no dns servers cannot resolve a hostname;
// the dial fails closed without touching the device resolver.
func TestWGNoDNS(t *testing.T) {
	lookups := blockDeviceDNS(t)
	r := newWGRig(t)
	r.eps[0].DNS = nil
	ch := build(t, []string{"wgA"}, r.eps)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := ch.DialContext(ctx, "tcp", echoHost+":80"); err == nil {
		_ = c.Close()
		t.Fatal("hostname dial with no tunnel DNS succeeded")
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("device lookups = %d, want 0", n)
	}
}

// TestWGNoHandshake: a wrong peer key means no handshake ever completes; the
// dial errors by its deadline and nothing falls back to the device route.
func TestWGNoHandshake(t *testing.T) {
	lookups := blockDeviceDNS(t)
	sentinel := testrig.NewSentinel(t)
	r := newWGRig(t)
	var wrong [32]byte
	if _, err := rand.Read(wrong[:]); err != nil {
		t.Fatal(err)
	}
	r.eps[0].PeerPublicKey = base64.StdEncoding.EncodeToString(wrong[:])
	ch := build(t, []string{"wgA"}, r.eps)

	const deadline = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	start := time.Now()
	for _, addr := range []string{netip.AddrPortFrom(r.peer.TunnelIP(), 80).String(), sentinel.Addr()} {
		if c, err := ch.DialContext(ctx, "tcp", addr); err == nil {
			_ = c.Close()
			t.Fatalf("dial %s without a handshake succeeded", addr)
		}
	}
	if el := time.Since(start); el > deadline+2*time.Second {
		t.Fatalf("dials took %v, want errors by the %v deadline", el, deadline)
	}
	time.Sleep(50 * time.Millisecond)
	if sentinel.Conns() != 0 || sentinel.Bytes() != 0 || lookups.Load() != 0 {
		t.Fatalf("sentinel %d conns / %d bytes, device lookups %d; want 0/0/0",
			sentinel.Conns(), sentinel.Bytes(), lookups.Load())
	}
}
