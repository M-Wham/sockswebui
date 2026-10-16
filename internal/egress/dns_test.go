package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// TestNoDeviceResolver proves hostname targets are resolved at the exit:
// both the process resolver and egress's device-lookup seam fail the test.
// Not parallel: it swaps process-wide state.
func TestNoDeviceResolver(t *testing.T) {
	origResolver, origLookup := net.DefaultResolver, deviceLookup
	t.Cleanup(func() { net.DefaultResolver, deviceLookup = origResolver, origLookup })
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Error("device resolver used")
		return nil, errors.New("blocked")
	}}
	deviceLookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		t.Errorf("egress device lookup used for %q", host)
		return nil, errors.New("blocked")
	}

	t.Run("SOCKS5", func(t *testing.T) {
		echo := startEcho(t)
		up := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{
			Name: "socksX", Hosts: map[string]string{echoHost: echo},
		})
		ch := build(t, []string{"socksX"}, []config.Endpoint{
			proxyEP(t, "socksX", config.KindSOCKS5, up.Addr(), "", ""),
		})
		if got := getEcho(t, ch); got["ip"] != up.SourceIP().String() {
			t.Fatalf("echo = %v, want ip %s", got, up.SourceIP())
		}
		if tg := up.Targets(); len(tg) != 1 || tg[0] != echoHost+":80" {
			t.Fatalf("socks targets = %v, want hostname passed as ATYP domain", tg)
		}
	})

	t.Run("SOCKS5->HTTP", func(t *testing.T) {
		r := newProxyRig(t, "pa-secret", "pb-secret")
		ch := build(t, []string{"socksA", "httpB"}, r.eps)
		if got := getEcho(t, ch); got["ip"] != r.httpB.SourceIP().String() {
			t.Fatalf("echo = %v", got)
		}
	})

	t.Run("WG", func(t *testing.T) {
		r := newWGRig(t)
		ch := build(t, []string{"wgA"}, r.eps)
		if got := getEcho(t, ch); got["hop"] != "wgA" || got["ip"] != r.peer.ClientIP().String() {
			t.Fatalf("echo = %v", got)
		}
	})

	// A proxy hop after a WG hop is reached through the tunnel: its hostname
	// is resolved by the tunnel's DNS and dialed over the netstack.
	t.Run("WG->SOCKS5 proxy by name", func(t *testing.T) {
		r := newWGRig(t, testrig.WithDNSRecord("socks.rig.invalid", netip.AddrFrom4([4]byte{10, 64, 0, 1})))
		up := socksOnPeer(t, r.peer)
		ep := proxyEP(t, "socksB", config.KindSOCKS5, up.Addr(), "ub", "pb-secret")
		ep.Host = "socks.rig.invalid"
		ch := build(t, []string{"wgA", "socksB"}, append(r.eps, ep))
		if got := getEcho(t, ch); got["ip"] != r.peer.TunnelIP().String() {
			t.Fatalf("echo = %v, want ip=%s", got, r.peer.TunnelIP())
		}
	})
}
