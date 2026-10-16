package supervisor

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const (
	floodUser = "flood-client"
	floodPass = "flood-pass-secret"
)

// socksAuthFrom opens a SOCKS5 user/pass session from loopback source src and,
// when the auth succeeds and target is set, CONNECTs to target. It returns the
// auth status, the CONNECT reply (0xff if not sent) and the open conn on 0x00.
func socksAuthFrom(t *testing.T, src, addr, user, pass, target string) (byte, byte, net.Conn) {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial slot from %s: %v", src, err)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	fail := func(what string, err error) {
		_ = c.Close()
		t.Fatalf("%s: %v", what, err)
	}
	if _, err := c.Write([]byte{5, 1, 2}); err != nil {
		fail("greeting", err)
	}
	var g [2]byte
	if _, err := io.ReadFull(c, g[:]); err != nil || g != [2]byte{5, 2} {
		fail("greeting reply", err)
	}
	msg := append([]byte{1, byte(len(user))}, user...)
	msg = append(append(msg, byte(len(pass))), pass...)
	if _, err := c.Write(msg); err != nil {
		fail("auth", err)
	}
	var a [2]byte
	if _, err := io.ReadFull(c, a[:]); err != nil {
		fail("auth reply", err)
	}
	if a[1] != 0 || target == "" {
		_ = c.Close()
		return a[1], 0xff, nil
	}
	host, portS, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portS)
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		fail("request", err)
	}
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		fail("connect reply", err)
	}
	if rep[1] != 0 {
		_ = c.Close()
		return 0, rep[1], nil
	}
	t.Cleanup(func() { _ = c.Close() })
	return 0, 0, c
}

// junkConnect sends raw bytes from loopback source src and waits for the
// server to close the conn.
func junkConnect(t *testing.T, src, addr string, b []byte) {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial slot from %s: %v", src, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(b); err != nil {
		t.Fatalf("junk write: %v", err)
	}
	// EOF or a reset (unread junk) both mean the server closed it.
	var ne net.Error
	if _, err := io.Copy(io.Discard, c); errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("junk conn from %s not closed by the server: %v", src, err)
	}
}

func ringHas(s *Slot, msg, sub string) bool {
	for _, l := range s.Logs() {
		if l.Msg == msg && strings.Contains(l.Text, sub) {
			return true
		}
	}
	return false
}

// TestRingSurvivesAuthFlood: an auth-failure flood larger
// than the slot ring does not push out a kill-switch line written before it,
// and fallback lines are never aggregated: every fallback connection, before
// and after the flood, keeps its own line. On a slot without SOCKS auth,
// request rejects and dial failures are aggregated the same way.
func TestRingSurvivesAuthFlood(t *testing.T) {
	t.Run("auth", testRingSurvivesAuthFlood)
	t.Run("no_auth", testRingSurvivesRequestFlood)
}

func testRingSurvivesAuthFlood(t *testing.T) {
	r := wgRig(t)
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(1, r.route...)
	cfg.Killswitch = config.KillswitchAllowDirect
	cfg.Health.FailThreshold = ksThreshold
	cfg.SocksAuth = &config.SocksAuth{Username: floodUser, Password: floodPass}
	// A long backoff keeps the slot DEGRADED with no rebuild probe in flight, so
	// Stop never closes a netstack mid-DNS (the known Close vs WriteNotify race;
	// this test is about logs, not recovery).
	s, ev, _ := newTestSlot(t, cfg, r.eps, withBuilder(loopbackDirectBuilder),
		withInterval(ksInterval), withBackoff(time.Minute, time.Minute))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)
	r.outer.Pause()
	waitState(t, ev, StateDegraded, 10*time.Second)
	waitFor(t, "kill-switch line", 5*time.Second, func() bool {
		return ringHas(s, "kill switch closed proxied connections", "reason=degraded")
	})

	const n = 3
	fallbackRound := func(src string) {
		t.Helper()
		for i := range n {
			st, rep, _ := socksAuthFrom(t, src, s.Addr(), floodUser, floodPass, sentinel.Addr())
			if st != 0 || rep != 0 {
				t.Fatalf("fallback CONNECT %d from %s: auth %#x reply %#x", i, src, st, rep)
			}
		}
	}
	fallbackRound("127.0.0.1")
	waitFor(t, "fallback lines", 5*time.Second, func() bool { return len(fallbackLines(s)) == n })

	// Flood from another source: more failures than the ring holds lines.
	const flood = LogRingSize + 200
	for i := range flood {
		if st, _, _ := socksAuthFrom(t, "127.0.0.2", s.Addr(), floodUser, "wrong-"+strconv.Itoa(i), ""); st != 0x01 {
			t.Fatalf("flood attempt %d status = %#x, want 0x01", i, st)
		}
	}
	waitFor(t, "throttled line", 5*time.Second, func() bool {
		return ringHas(s, "socks handshake rejected", "reason=auth_throttled")
	})

	// Unauthenticated junk: a bad_version flood larger than the ring from a
	// third source; handshake rejects are aggregated too.
	for range flood {
		junkConnect(t, "127.0.0.4", s.Addr(), []byte{4, 1, 0})
	}
	waitFor(t, "bad_version line", 5*time.Second, func() bool {
		return ringHas(s, "socks handshake rejected", "reason=bad_version")
	})

	if !ringHas(s, "kill switch closed proxied connections", "reason=degraded") {
		t.Fatalf("kill-switch line pushed out of the ring by the auth/bad_version flood")
	}
	if !ringHas(s, "slot state", "to=DEGRADED") {
		t.Fatalf("DEGRADED state line pushed out of the ring by the auth flood")
	}
	if got := len(fallbackLines(s)); got != n {
		t.Fatalf("fallback lines after flood = %d, want %d", got, n)
	}

	// Fallback lines are never aggregated: each new one is logged.
	fallbackRound("127.0.0.3")
	fallbackRound("127.0.0.3")
	waitFor(t, "fallback lines after flood", 5*time.Second, func() bool { return len(fallbackLines(s)) >= 3*n })
	time.Sleep(50 * time.Millisecond)
	if got := len(fallbackLines(s)); got != 3*n {
		t.Fatalf("fallback lines = %d, want %d (one per connection)", got, 3*n)
	}

	// Stopping the slot closes the SOCKS server, which flushes the summaries
	// into the ring; no flood line carries a password.
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !ringHas(s, "socks auth_throttled summary", "peer_key=127.0.0.2") {
		t.Fatalf("no throttled summary in the ring after Stop: %v", s.Logs())
	}
	if !ringHas(s, "socks bad_version summary", "peer_key=127.0.0.4") {
		t.Fatalf("no bad_version summary in the ring after Stop: %v", s.Logs())
	}
	for _, l := range s.Logs() {
		if strings.Contains(l.Text, "wrong-") || strings.Contains(l.Text, floodPass) {
			t.Fatalf("ring line carries a password: %q", l.Text)
		}
	}
}

// testRingSurvivesRequestFlood: on a no-auth DEGRADED slot (kill switch
// "block"), a cmd_not_supported (BIND) flood and a dial_failed flood, each larger than the
// ring, leave the kill-switch and DEGRADED lines in place and end in summaries.
func testRingSurvivesRequestFlood(t *testing.T) {
	r := wgRig(t)
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(2, r.route...)
	cfg.Health.FailThreshold = ksThreshold
	s, ev, _ := newTestSlot(t, cfg, r.eps,
		withInterval(ksInterval), withBackoff(time.Minute, time.Minute))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)
	r.outer.Pause()
	waitState(t, ev, StateDegraded, 10*time.Second)
	waitFor(t, "kill-switch line", 5*time.Second, func() bool {
		return ringHas(s, "kill switch closed proxied connections", "reason=degraded")
	})

	host, portS, _ := net.SplitHostPort(sentinel.Addr())
	ip := net.ParseIP(host).To4()
	port, _ := strconv.Atoi(portS)
	request := func(cmd byte) []byte {
		b := append([]byte{5, 1, 0, 5, cmd, 0, 1}, ip...)
		return binary.BigEndian.AppendUint16(b, uint16(port))
	}

	const flood = LogRingSize + 200
	for range flood {
		junkConnect(t, "127.0.0.5", s.Addr(), request(0x02)) // BIND: cmd_not_supported
	}
	waitFor(t, "cmd_not_supported line", 5*time.Second, func() bool {
		return ringHas(s, "socks request rejected", "reason=cmd_not_supported")
	})
	for range flood {
		junkConnect(t, "127.0.0.6", s.Addr(), request(0x01)) // refused by the kill switch
	}
	waitFor(t, "dial_failed line", 5*time.Second, func() bool {
		return ringHas(s, "socks connect failed", "reason=dial_failed")
	})

	if !ringHas(s, "kill switch closed proxied connections", "reason=degraded") {
		t.Fatalf("kill-switch line pushed out of the ring by the request/dial flood")
	}
	if !ringHas(s, "slot state", "to=DEGRADED") {
		t.Fatalf("DEGRADED state line pushed out of the ring by the request/dial flood")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !ringHas(s, "socks cmd_not_supported summary", "peer_key=127.0.0.5") {
		t.Fatalf("no cmd_not_supported summary in the ring after Stop: %v", s.Logs())
	}
	if !ringHas(s, "socks dial_failed summary", "peer_key=127.0.0.6") {
		t.Fatalf("no dial_failed summary in the ring after Stop: %v", s.Logs())
	}
}
