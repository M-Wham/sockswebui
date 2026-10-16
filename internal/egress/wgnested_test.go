package egress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// nestedRig is a [wgA, wgB] route: peer wgB's UDP socket lives on peer wgA's
// netstack (at wgA's tunnel IP), so wgB is reachable only through wgA.
type nestedRig struct {
	a, b *testrig.WGPeer
	eps  []config.Endpoint
}

func newNestedRig(t *testing.T) nestedRig {
	t.Helper()
	a := testrig.NewWGPeer(t, "wgA")
	b := testrig.NewWGPeer(t, "wgB",
		testrig.WithTunnelIPs(netip.MustParseAddr("10.65.0.1"), netip.MustParseAddr("10.65.0.2")),
		testrig.WithHostNet(a.Net(), a.TunnelIP()))
	epA := a.ClientConfig()
	epA.ID = "wgA"
	epB := b.ClientConfig()
	epB.ID = "wgB"
	// Importer default: the chain, not the config, must clamp to outer − 80.
	epB.MTU = defaultWGMTU
	return nestedRig{a: a, b: b, eps: []config.Endpoint{epA, epB}}
}

// testWGtoWG is TestChain/WG->WG.
func testWGtoWG(t *testing.T) {
	binds := recordBinds(t)
	sentinel := testrig.NewSentinel(t)
	r := newNestedRig(t)
	if strings.HasPrefix(r.b.Endpoint(), "127.") {
		t.Fatalf("wgB endpoint %s is on loopback, want an address on wgA's netstack", r.b.Endpoint())
	}
	ch := build(t, []string{"wgA", "wgB"}, r.eps)

	got := getEcho(t, ch)
	if got["hop"] != "wgB" || got["ip"] != r.b.ClientIP().String() {
		t.Fatalf("echo = %v, want hop=wgB ip=%s (resolved via wgB's tunnel DNS)", got, r.b.ClientIP())
	}

	tun := ch.(*chain).tunnels
	if len(tun) != 2 || tun[0].mtu != defaultWGMTU || tun[1].mtu != defaultWGMTU-wgEncapOverhead {
		var mtus []int
		for _, x := range tun {
			mtus = append(mtus, x.mtu)
		}
		t.Fatalf("tunnel MTUs = %v, want [%d %d] (inner = outer − 80)", mtus, defaultWGMTU, defaultWGMTU-wgEncapOverhead)
	}

	if len(*binds) != 1 {
		t.Fatalf("OS binds = %d, want exactly 1 (wgA's); wgB must ride wgA's netstack", len(*binds))
	}
	if r.b.HostNetPackets() == 0 {
		t.Fatal("wgB peer saw 0 datagrams on wgA's netstack; handshake did not arrive via wgA")
	}

	// A loopback target is tunnelled to wgB's exit, never the device route.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, err := ch.DialContext(ctx, "tcp", sentinel.Addr()); err == nil {
		_ = c.Close()
		t.Fatal("dial to the loopback sentinel through [wgA, wgB] succeeded")
	}
	time.Sleep(50 * time.Millisecond)
	if sentinel.Conns() != 0 || sentinel.Bytes() != 0 {
		t.Fatalf("sentinel saw %d conns / %d bytes, want 0", sentinel.Conns(), sentinel.Bytes())
	}
}

// TestWGNestedFamilyMismatch: wgB's endpoint is IPv6 but wgA has only an
// IPv4 tunnel address, so no netstack bind can carry it — rejected before
// any socket opens.
func TestWGNestedFamilyMismatch(t *testing.T) {
	binds := recordBinds(t)
	r := newNestedRig(t)
	eps := append([]config.Endpoint(nil), r.eps...)
	eps[1].Endpoint = "[fd00::1]:51820"
	if ch, err := Build([]string{"wgA", "wgB"}, eps); err == nil {
		_ = ch.Close()
		t.Fatal("Build succeeded with an address-family mismatch between hops")
	}
	if len(*binds) != 0 {
		t.Fatalf("OS binds = %d after rejected build, want 0", len(*binds))
	}
}

// redirectBind is an OS bind that sends any datagram addressed to target to
// the host UDP sentinel instead, through the same real OS socket. wgB's
// advertised address lives on wgA's netstack and cannot be bound on the host,
// so this keeps the guard hermetic: an OS-socket send toward wgB lands on
// loopback, where the sentinel counts it, rather than on the device route.
type redirectBind struct {
	conn.Bind
	target string
	to     conn.Endpoint
}

func (b *redirectBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	if ep.DstToString() == b.target {
		ep = b.to
	}
	return b.Bind.Send(bufs, ep)
}

// TestWGNestedNoHostUDP (inner WireGuard UDP rides the outer tunnel): during a [wgA, wgB] echo round
// trip, no WireGuard datagram for the inner peer leaves through an OS socket;
// wgB's UDP rides wgA's netstack only. The echo must succeed so the zero count
// is not vacuous.
func TestWGNestedNoHostUDP(t *testing.T) {
	r := newNestedRig(t)
	sentinel := testrig.NewUDPSentinel(t)
	orig := newOSBind
	newOSBind = func() conn.Bind {
		b := orig()
		to, err := b.ParseEndpoint(sentinel.Addr())
		if err != nil {
			t.Errorf("parse sentinel endpoint: %v", err)
		}
		return &redirectBind{Bind: b, target: r.b.Endpoint(), to: to}
	}
	t.Cleanup(func() { newOSBind = orig })

	ch := build(t, []string{"wgA", "wgB"}, r.eps)
	client := &http.Client{
		Transport: &http.Transport{DialContext: ch.DialContext, DisableKeepAlives: true},
		Timeout:   5 * time.Second,
	}
	resp, echoErr := client.Get("http://" + echoHost + "/")
	var got map[string]string
	if echoErr == nil {
		echoErr = json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
	}

	time.Sleep(50 * time.Millisecond) // let any in-flight datagram land
	if n := sentinel.Datagrams(); n != 0 {
		t.Fatalf("host UDP sentinel for wgB's endpoint %s saw %d datagrams via an OS socket, want 0",
			r.b.Endpoint(), n)
	}
	if echoErr != nil {
		t.Fatalf("echo via [wgA, wgB]: %v", echoErr)
	}
	if got["hop"] != "wgB" {
		t.Fatalf("echo = %v, want hop=wgB", got)
	}
	if r.b.HostNetPackets() == 0 {
		t.Fatal("wgB saw 0 datagrams on wgA's netstack; the round trip did not ride wgA")
	}
}
