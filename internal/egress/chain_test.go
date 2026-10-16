package egress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const echoHost = "echo.rig.invalid"

// startEcho serves the testrig echo on 127.0.0.1 and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := testrig.ServeEcho(ln, "echo")
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return ln.Addr().String()
}

// proxyEP builds a proxy endpoint pointing at addr.
func proxyEP(t *testing.T, id, kind, addr, user, pass string) config.Endpoint {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return config.Endpoint{ID: id, Name: id, Kind: kind, Host: host, Port: p, Username: user, Password: pass}
}

func build(t *testing.T, route []string, eps []config.Endpoint) Chain {
	t.Helper()
	ch, err := Build(route, eps)
	if err != nil {
		t.Fatalf("Build(%v): %v", route, err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return ch
}

// getEcho fetches http://echo.rig.invalid/ through the chain.
func getEcho(t *testing.T, ch Chain) map[string]string {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{DialContext: ch.DialContext, DisableKeepAlives: true},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get("http://" + echoHost + "/")
	if err != nil {
		t.Fatalf("GET via chain: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	return got
}

// rig is a [socksA(auth), httpB(auth)] proxy chain whose last hop maps
// echo.rig.invalid:80 to the loopback echo server.
type rig struct {
	socksA *testrig.SOCKS5Upstream
	httpB  *testrig.HTTPUpstream
	eps    []config.Endpoint
}

func newProxyRig(t *testing.T, passA, passB string) rig {
	t.Helper()
	echo := startEcho(t)
	a := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{Name: "socksA", Username: "ua", Password: "pa-secret"})
	b := testrig.NewLoopbackHTTP(t, testrig.UpstreamConfig{
		Name: "httpB", Username: "ub", Password: "pb-secret",
		Hosts: map[string]string{echoHost: echo},
	})
	return rig{socksA: a, httpB: b, eps: []config.Endpoint{
		proxyEP(t, "socksA", config.KindSOCKS5, a.Addr(), "ua", passA),
		proxyEP(t, "httpB", config.KindHTTP, b.Addr(), "ub", passB),
	}}
}

// wgRig is a testrig WireGuard peer plus a route endpoint list that reaches it.
type wgRig struct {
	peer *testrig.WGPeer
	eps  []config.Endpoint
}

func newWGRig(t *testing.T, opts ...testrig.PeerOption) wgRig {
	t.Helper()
	p := testrig.NewWGPeer(t, "wgA", opts...)
	ep := p.ClientConfig()
	ep.ID = "wgA"
	return wgRig{peer: p, eps: []config.Endpoint{ep}}
}

// socksOnPeer serves a testrig SOCKS5 upstream (auth ub/pb) on the peer's
// netstack at TunnelIP:1080; it dials onward from that netstack.
func socksOnPeer(t *testing.T, p *testrig.WGPeer) *testrig.SOCKS5Upstream {
	t.Helper()
	ln, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), 1080))
	if err != nil {
		t.Fatal(err)
	}
	up := testrig.NewSOCKS5Upstream(ln, testrig.UpstreamConfig{
		Name: "socksB", Username: "ub", Password: "pb-secret", Dial: p.Net().DialContext,
		Hosts: map[string]string{echoHost: p.TunnelIP().String()},
	})
	t.Cleanup(func() { _ = up.Close() })
	return up
}

func TestChain(t *testing.T) {
	t.Run("WG", func(t *testing.T) {
		r := newWGRig(t)
		ch := build(t, []string{"wgA"}, r.eps)
		got := getEcho(t, ch)
		if got["hop"] != "wgA" || got["ip"] != r.peer.ClientIP().String() {
			t.Fatalf("echo = %v, want hop=wgA ip=%s (client tunnel addr)", got, r.peer.ClientIP())
		}
	})

	t.Run("WG->WG", testWGtoWG)

	t.Run("WG->SOCKS5", func(t *testing.T) {
		r := newWGRig(t)
		up := socksOnPeer(t, r.peer)
		eps := append(r.eps, proxyEP(t, "socksB", config.KindSOCKS5, up.Addr(), "ub", "pb-secret"))
		ch := build(t, []string{"wgA", "socksB"}, eps)
		got := getEcho(t, ch)
		// socksB dials from the peer netstack, so the echo sees the peer's own
		// tunnel IP rather than the client's: socksB was the last hop.
		if got["ip"] != r.peer.TunnelIP().String() {
			t.Fatalf("echo = %v, want ip=%s (socksB on the peer netstack)", got, r.peer.TunnelIP())
		}
		if tg := up.Targets(); len(tg) != 1 || tg[0] != echoHost+":80" {
			t.Fatalf("socksB targets = %v, want [%s:80] verbatim", tg, echoHost)
		}
	})

	t.Run("SOCKS5->HTTP", func(t *testing.T) {
		r := newProxyRig(t, "pa-secret", "pb-secret")
		ch := build(t, []string{"socksA", "httpB"}, r.eps)
		got := getEcho(t, ch)
		if got["ip"] != r.httpB.SourceIP().String() {
			t.Fatalf("echo ip = %q, want httpB source %s (last hop)", got["ip"], r.httpB.SourceIP())
		}
		if tg := r.socksA.Targets(); len(tg) != 1 || tg[0] != r.httpB.Addr() {
			t.Fatalf("socksA targets = %v, want [%s]", tg, r.httpB.Addr())
		}
		if tg := r.httpB.Targets(); len(tg) != 1 || tg[0] != echoHost+":80" {
			t.Fatalf("httpB targets = %v, want [%s:80] (hostname passed verbatim)", tg, echoHost)
		}
	})
}

func TestChainProxyAuthFail(t *testing.T) {
	for _, tc := range []struct{ name, passA, passB string }{
		{"socks hop", "wrong-pass-A", "pb-secret"},
		{"http hop", "pa-secret", "wrong-pass-B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := testrig.NewSentinel(t)
			r := newProxyRig(t, tc.passA, tc.passB)
			ch := build(t, []string{"socksA", "httpB"}, r.eps)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := ch.DialContext(ctx, "tcp", sentinel.Addr())
			if err == nil {
				_ = c.Close()
				t.Fatal("DialContext succeeded with a wrong password")
			}
			for _, p := range []string{tc.passA, tc.passB, "pa-secret", "pb-secret"} {
				if strings.Contains(err.Error(), p) {
					t.Fatalf("error leaks a password: %v", err)
				}
			}
			time.Sleep(50 * time.Millisecond)
			if sentinel.Conns() != 0 || sentinel.Bytes() != 0 {
				t.Fatalf("sentinel saw %d conns / %d bytes, want 0", sentinel.Conns(), sentinel.Bytes())
			}
		})
	}
}

func TestBuildValidate(t *testing.T) {
	sentinel := testrig.NewSentinel(t)

	t.Run("empty route is direct", func(t *testing.T) {
		// The loopback sentinel is on the deny-list, so reach it through the
		// internal permit-all hook; TestDeviceDialCheckedLink covers Build.
		ch, err := buildWith(nil, nil, permitAll)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ch.Close() })
		c, err := ch.DialContext(context.Background(), "tcp", sentinel.Addr())
		if err != nil {
			t.Fatalf("direct dial: %v", err)
		}
		if _, err := io.WriteString(c, "ping"); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		deadline := time.Now().Add(2 * time.Second)
		for sentinel.Bytes() < 4 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if sentinel.Bytes() != 4 {
			t.Fatalf("sentinel bytes = %d, want 4", sentinel.Bytes())
		}
	})

	t.Run("invalid routes fail before any socket", func(t *testing.T) {
		s2 := testrig.NewSentinel(t)
		p := func(id string) config.Endpoint { return proxyEP(t, id, config.KindSOCKS5, s2.Addr(), "", "") }
		wg := config.Endpoint{ID: "wg", Kind: config.KindWireGuard, Endpoint: s2.Addr(),
			PeerPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
		eps := []config.Endpoint{p("p1"), p("p2"), p("p3"), p("p4"), p("p5"), wg}
		for name, route := range map[string][]string{
			"unknown id":      {"nope"},
			"proxy before wg": {"p1", "wg"},
			"five hops":       {"p1", "p2", "p3", "p4", "p5"},
		} {
			ch, err := Build(route, eps)
			if err == nil {
				_ = ch.Close()
				t.Errorf("%s: Build(%v) succeeded, want error", name, route)
			}
		}
		time.Sleep(50 * time.Millisecond)
		if s2.Conns() != 0 {
			t.Fatalf("sentinel conns = %d, want 0", s2.Conns())
		}
	})

	t.Run("nested WG", testBuildValidateNested)

	t.Run("closed chain refuses dials", func(t *testing.T) {
		ch, err := Build(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = ch.Close()
		// Assert the closed-chain error itself: the sentinel is on loopback, so
		// a bare "err != nil" would also pass because of the deny-list.
		if c, err := ch.DialContext(context.Background(), "tcp", sentinel.Addr()); err == nil {
			_ = c.Close()
			t.Fatal("DialContext after Close succeeded")
		} else if !errors.Is(err, ErrClosed) {
			t.Fatalf("DialContext after Close: %v, want ErrClosed", err)
		}
	})

	t.Run("non-tcp network refused", func(t *testing.T) {
		r := newProxyRig(t, "pa-secret", "pb-secret")
		ch := build(t, []string{"socksA"}, r.eps)
		if c, err := ch.DialContext(context.Background(), "udp", sentinel.Addr()); err == nil {
			_ = c.Close()
			t.Fatal("udp dial through proxy chain succeeded")
		}
	})
}
