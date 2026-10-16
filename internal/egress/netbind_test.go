package egress

import (
	"errors"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/M-Wham/sockswebui/internal/testrig"
)

type recvResult struct {
	n    int
	size int
	data []byte
	ep   conn.Endpoint
	err  error
}

func recvAsync(fn conn.ReceiveFunc) <-chan recvResult {
	ch := make(chan recvResult, 1)
	go func() {
		bufs := [][]byte{make([]byte, 2048)}
		sizes := make([]int, 1)
		eps := make([]conn.Endpoint, 1)
		n, err := fn(bufs, sizes, eps)
		ch <- recvResult{n: n, size: sizes[0], data: bufs[0][:sizes[0]], ep: eps[0], err: err}
	}()
	return ch
}

// TestNetstackBind drives a netBind over a real single-hop tunnel's netstack:
// datagrams cross the tunnel to a UDP echo on the WG peer's netstack.
func TestNetstackBind(t *testing.T) {
	p := testrig.NewWGPeer(t, "wgA")
	pc, err := p.Net().ListenUDPAddrPort(netip.AddrPortFrom(p.TunnelIP(), 7))
	if err != nil {
		t.Fatal(err)
	}
	echoDone := testrig.ServeUDPEcho(pc)
	t.Cleanup(func() { _ = pc.Close(); <-echoDone })

	s, err := parseWG("route[0]", p.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	tun, err := startWG(s, newOSBind())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tun.close)

	before := runtime.NumGoroutine()
	var b conn.Bind = newNetBind(tun.tnet, p.ClientIP())
	if b.BatchSize() != 1 {
		t.Fatalf("BatchSize = %d, want 1", b.BatchSize())
	}
	if err := b.SetMark(42); err != nil {
		t.Fatalf("SetMark: %v", err)
	}
	if _, err := b.ParseEndpoint("wg.example:51820"); err == nil {
		t.Fatal("ParseEndpoint accepted a hostname (must never resolve)")
	}
	fns, port, err := b.Open(0)
	if err != nil || len(fns) != 1 || port == 0 {
		t.Fatalf("Open = %d fns, port %d, err %v", len(fns), port, err)
	}
	if _, _, err := b.Open(0); !errors.Is(err, conn.ErrBindAlreadyOpen) {
		t.Fatalf("second Open err = %v, want ErrBindAlreadyOpen", err)
	}

	dst := netip.AddrPortFrom(p.TunnelIP(), 7).String()
	ep, err := b.ParseEndpoint(dst)
	if err != nil {
		t.Fatal(err)
	}
	if ep.DstToString() != dst || ep.DstIP() != p.TunnelIP() {
		t.Fatalf("endpoint = %s/%s, want %s", ep.DstToString(), ep.DstIP(), dst)
	}

	got := recvAsync(fns[0])
	payload := []byte("ping-through-tunnel")
	var res recvResult
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	if err := b.Send([][]byte{payload}, ep); err != nil {
		t.Fatalf("Send: %v", err)
	}
wait:
	for {
		select {
		case res = <-got:
			break wait
		case <-tick.C: // first datagrams may race the handshake; resend
			if err := b.Send([][]byte{payload}, ep); err != nil {
				t.Fatalf("Send: %v", err)
			}
		case <-deadline:
			t.Fatal("no echo through the tunnel within 10s")
		}
	}
	if res.err != nil || res.n != 1 || string(res.data) != string(payload) {
		t.Fatalf("recv = n %d, %q, err %v; want 1, %q", res.n, res.data, res.err, payload)
	}
	if res.ep == nil || res.ep.DstToString() != dst {
		t.Fatalf("recv endpoint = %v, want %s", res.ep, dst)
	}

	// Close must unblock a pending receive with net.ErrClosed.
	pending := recvAsync(fns[0])
	time.Sleep(50 * time.Millisecond)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case r := <-pending:
		if !errors.Is(r.err, net.ErrClosed) {
			t.Fatalf("pending receive after Close: err %v, want net.ErrClosed", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the pending receive")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := b.Send([][]byte{payload}, ep); err == nil {
		t.Fatal("Send after Close succeeded")
	}
	if _, err := fns[0]([][]byte{make([]byte, 64)}, make([]int, 1), make([]conn.Endpoint, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("receive after Close: %v, want net.ErrClosed", err)
	}

	// wireguard-go re-opens binds on BindUpdate.
	if _, _, err := b.Open(port); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	waitGoroutines(t, before)
}

func TestNetstackBindWrongEndpoint(t *testing.T) {
	p := testrig.NewWGPeer(t, "wgA")
	s, err := parseWG("route[0]", p.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	tun, err := startWG(s, newOSBind())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tun.close)
	b := newNetBind(tun.tnet, p.ClientIP())
	if _, _, err := b.Open(0); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	v6, err := b.ParseEndpoint("[fd00::1]:51820")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send([][]byte{[]byte("x")}, v6); err == nil {
		t.Fatal("Send to an endpoint of the other address family succeeded")
	}
}
