package supervisor

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const (
	e2eUser     = "slotuser"
	e2ePass     = "slot-secret"
	e2eEchoPort = 8080
)

// socksAuthConnect performs a SOCKS5 user/pass (RFC 1929) handshake and a
// CONNECT to target via addr. It returns the auth status (0 = success), the
// CONNECT reply (0xff when auth failed), and the conn (closed at cleanup).
func socksAuthConnect(t *testing.T, addr, user, pass, target string) (authStatus, rep byte, c net.Conn) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial slot: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	var g [2]byte
	if _, err := c.Write([]byte{5, 1, 2}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	if _, err := io.ReadFull(c, g[:]); err != nil || g != [2]byte{5, 2} {
		t.Fatalf("greeting reply %v: %v (want user/pass method)", g, err)
	}
	sub := append([]byte{1, byte(len(user))}, user...)
	sub = append(append(sub, byte(len(pass))), pass...)
	if _, err := c.Write(sub); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if _, err := io.ReadFull(c, g[:]); err != nil {
		t.Fatalf("auth reply: %v", err)
	}
	if g[1] != 0 {
		return g[1], 0xff, c
	}
	host, portS, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portS)
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("request: %v", err)
	}
	var r [10]byte
	if _, err := io.ReadFull(c, r[:]); err != nil {
		t.Fatalf("connect reply: %v", err)
	}
	return 0, r[1], c
}

// e2eRig is route [wgE, httpE]: the HTTP CONNECT upstream (auth on) listens
// on wgE's netstack and maps the echo host to an echo on the same netstack
// that reports hop=httpE. wgE's own echo (port 80) reports hop=wgE, so the
// reported hop proves which hop was last. No host dial anywhere.
func e2eRig(t *testing.T) (*testrig.WGPeer, []config.Endpoint) {
	t.Helper()
	p := testrig.NewWGPeer(t, "wgE")
	wg := p.ClientConfig()
	wg.ID, wg.Name = "wgE", "wgE"
	echoLn, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), e2eEchoPort))
	if err != nil {
		t.Fatal(err)
	}
	done := testrig.ServeEcho(echoLn, "httpE")
	t.Cleanup(func() { _ = echoLn.Close(); <-done })
	ln, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), 3128))
	if err != nil {
		t.Fatal(err)
	}
	up := testrig.NewHTTPUpstream(ln, testrig.UpstreamConfig{
		Name: "httpE", Username: "ue", Password: "pe-secret", Dial: p.Net().DialContext,
		Hosts: map[string]string{testrig.EchoHost: netip.AddrPortFrom(p.TunnelIP(), e2eEchoPort).String()},
	})
	t.Cleanup(func() { _ = up.Close() })
	return p, []config.Endpoint{wg, {ID: "httpE", Name: "httpE", Kind: config.KindHTTP,
		Host: p.TunnelIP().String(), Port: 3128, Username: "ue", Password: "pe-secret"}}
}

// TestE2ECriterion1: acceptance criterion 1, route WG→HTTP with
// slot SOCKS auth, started via the Manager, fail closed when the WG hop dies.
func TestE2ECriterion1(t *testing.T) {
	sentinel := testrig.NewSentinel(t)
	peer, eps := e2eRig(t)
	sc := rigSlot(1, "wgE", "httpE")
	sc.SocksAuth = &config.SocksAuth{Username: e2eUser, Password: e2ePass}
	m, ev := newTestManager(t, config.Config{Endpoints: eps, Slots: []config.Slot{sc}},
		withBuilder(loopbackDirectBuilder), withInterval(ksInterval), withBackoff(200*time.Millisecond, time.Second))
	if err := m.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)
	addr := status(t, m, 1).Addr

	t.Run("echo exits at last hop", func(t *testing.T) {
		st, rep, c := socksAuthConnect(t, addr, e2eUser, e2ePass, testrig.EchoHost+":80")
		if st != 0 || rep != 0 {
			t.Fatalf("auth status %#x / CONNECT reply %#x, want 0/0", st, rep)
		}
		req, _ := http.NewRequest(http.MethodGet, "http://"+testrig.EchoHost+"/", nil)
		req.Close = true
		if err := req.Write(c); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), req)
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		defer resp.Body.Close()
		var got map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode echo: %v", err)
		}
		if got["hop"] != "httpE" {
			t.Fatalf("echo = %v, want hop=httpE (last hop), not wgE", got)
		}
	})

	t.Run("wrong slot credentials", func(t *testing.T) {
		st, rep, c := socksAuthConnect(t, addr, e2eUser, "wrong", testrig.EchoHost+":80")
		if st == 0 || rep != 0xff {
			t.Fatalf("auth status %#x / reply %#x, want auth failure", st, rep)
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		if n, _ := c.Read(make([]byte, 64)); n != 0 {
			t.Fatalf("got %d bytes after auth failure, want none", n)
		}
	})

	t.Run("WG hop down fails closed", func(t *testing.T) {
		peer.Pause()
		defer peer.Resume()
		waitSlots(t, ev, map[int]State{1: StateDegraded}, 10*time.Second)
		if _, rep, _ := socksAuthConnect(t, addr, e2eUser, e2ePass, sentinel.Addr()); rep == 0 {
			t.Fatal("CONNECT while DEGRADED succeeded, want non-zero reply")
		}
		if _, rep, _ := socksAuthConnect(t, addr, e2eUser, e2ePass, testrig.EchoHost+":80"); rep == 0 {
			t.Fatal("echo CONNECT while DEGRADED succeeded, want non-zero reply")
		}
	})

	if b, c := sentinel.Bytes(), sentinel.Conns(); b != 0 || c != 0 {
		t.Fatalf("direct sentinel saw %d bytes / %d conns, want 0", b, c)
	}
}
