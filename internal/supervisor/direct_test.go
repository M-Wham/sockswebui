package supervisor

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// fallbackLines returns the slot log lines recording a fallback connection.
func fallbackLines(s *Slot) []LogLine {
	var out []LogLine
	for _, l := range s.Logs() {
		if l.Msg == "fallback" {
			out = append(out, l)
		}
	}
	return out
}

// TestAllowDirect: with killswitch "allow-direct", a DEGRADED slot dials the
// device route and logs one line per fallback connection; once
// UP again, new conns ride the tunnel and the fallback conns are closed.
func TestAllowDirect(t *testing.T) {
	r := wgRig(t)
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(1, r.route...)
	cfg.Killswitch = config.KillswitchAllowDirect
	cfg.Health.FailThreshold = ksThreshold
	s, ev, procLog := newTestSlot(t, cfg, r.eps, withBuilder(loopbackDirectBuilder),
		withInterval(ksInterval), withBackoff(200*time.Millisecond, time.Second))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)

	// While UP the sentinel target is dialed through the tunnel, never direct.
	trySend(s.Addr(), sentinel.Addr(), []byte("tunnel"))
	time.Sleep(100 * time.Millisecond)
	if b := sentinel.Bytes(); b != 0 {
		t.Fatalf("UP: sentinel saw %d bytes, want 0", b)
	}
	if n := len(fallbackLines(s)); n != 0 {
		t.Fatalf("UP: %d fallback lines, want 0", n)
	}

	r.outer.Pause()
	waitState(t, ev, StateDegraded, 10*time.Second)

	const n = 3
	var fb []io.Closer
	for i := 0; i < n; i++ {
		rep, c := socksConnect(t, s.Addr(), sentinel.Addr())
		if rep != 0 {
			t.Fatalf("fallback CONNECT %d reply = %#x, want 0x00", i, rep)
		}
		if _, err := c.Write([]byte("direct")); err != nil {
			t.Fatalf("write via fallback: %v", err)
		}
		fb = append(fb, c)
	}
	waitFor(t, "sentinel bytes", 5*time.Second, func() bool { return sentinel.Bytes() >= n*int64(len("direct")) })

	lines := fallbackLines(s)
	if len(lines) != n {
		t.Fatalf("fallback lines = %d, want %d: %v", len(lines), n, lines)
	}
	priv := r.eps[0].PrivateKey
	for _, l := range lines {
		if !strings.Contains(l.Text, "target="+sentinel.Addr()) {
			t.Fatalf("fallback line lacks target %s: %q", sentinel.Addr(), l.Text)
		}
		if strings.Contains(l.Text, priv) {
			t.Fatalf("fallback line leaks a secret: %q", l.Text)
		}
	}
	if got := strings.Count(procLog.String(), "msg=fallback"); got != n {
		t.Fatalf("process log fallback lines = %d, want %d", got, n)
	}

	r.outer.Resume()
	waitState(t, ev, StateUp, 15*time.Second)
	// Fallback conns are cut once the tunnel is back.
	for i, c := range fb {
		buf := make([]byte, 1)
		rc := c.(interface {
			Read([]byte) (int, error)
			SetReadDeadline(time.Time) error
		})
		_ = rc.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := rc.Read(buf); err == nil || isTimeout(err) {
			t.Fatalf("fallback conn %d still open after UP (err=%v)", i, err)
		}
	}

	before := sentinel.Bytes()
	if got := socksEcho(t, s.Addr()); got["hop"] != "wgA" {
		t.Fatalf("echo after recovery = %v, want hop=wgA", got)
	}
	trySend(s.Addr(), sentinel.Addr(), []byte("tunnel"))
	time.Sleep(200 * time.Millisecond)
	if b := sentinel.Bytes(); b != before {
		t.Fatalf("after recovery sentinel grew %d → %d bytes, want no direct traffic", before, b)
	}
	if got := len(fallbackLines(s)); got != n {
		t.Fatalf("fallback lines after recovery = %d, want %d", got, n)
	}
}

func isTimeout(err error) bool {
	te, ok := err.(interface{ Timeout() bool })
	return ok && te.Timeout()
}

// TestAllowDirectNotInBlock is the regression guard: the identical scenario
// under "block" never reaches the sentinel and logs no fallback.
func TestAllowDirectNotInBlock(t *testing.T) {
	r := wgRig(t)
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(1, r.route...)
	cfg.Killswitch = config.KillswitchBlock
	cfg.Health.FailThreshold = ksThreshold
	s, ev, _ := newTestSlot(t, cfg, r.eps, withBuilder(loopbackDirectBuilder),
		withInterval(ksInterval), withBackoff(200*time.Millisecond, time.Second))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)
	r.outer.Pause()
	waitState(t, ev, StateDegraded, 10*time.Second)
	for i := 0; i < 3; i++ {
		if rep, _ := socksConnect(t, s.Addr(), sentinel.Addr()); rep != 0x01 {
			t.Fatalf("CONNECT %d under block reply = %#x, want 0x01", i, rep)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if b, c := sentinel.Bytes(), sentinel.Conns(); b != 0 || c != 0 {
		t.Fatalf("block: sentinel saw %d bytes / %d conns, want 0", b, c)
	}
	if n := len(fallbackLines(s)); n != 0 {
		t.Fatalf("block: %d fallback lines, want 0", n)
	}
}

// TestFallbackOnlyWhenDegraded: the allow-direct gate never dials direct in
// STARTING, ERROR or STOPPED (rule 2: fallback ONLY when DEGRADED).
func TestFallbackOnlyWhenDegraded(t *testing.T) {
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(1, "wgA")
	cfg.Killswitch = config.KillswitchAllowDirect
	s := NewSlot(cfg, nil, nil, nil, withBuilder(loopbackDirectBuilder))
	for _, st := range []State{StateStopped, StateStarting, StateError} {
		s.mu.Lock()
		s.state = st
		s.mu.Unlock()
		if c, err := (gate{s}).DialContext(context.Background(), "tcp", sentinel.Addr()); err == nil {
			_ = c.Close()
			t.Fatalf("%s: gate dialed, want ErrNotUp", st)
		}
	}
	if c := sentinel.Conns(); c != 0 {
		t.Fatalf("sentinel saw %d conns, want 0", c)
	}
}

// TestRestoreKeepsError: a rebuild finishing after the slot went ERROR
// (e.g. SOCKS serve panic) must not overwrite ERROR with UP.
func TestRestoreKeepsError(t *testing.T) {
	s := NewSlot(rigSlot(1, "wgA"), nil, nil, nil)
	s.mu.Lock()
	s.state = StateError
	s.mu.Unlock()
	ch, err := egress.Build(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if s.restore(context.Background(), ch, Result{}) {
		t.Fatal("restore succeeded from ERROR")
	}
	if st, _ := s.State(); st != StateError {
		t.Fatalf("state = %s, want ERROR", st)
	}
	s.mu.Lock()
	installed := s.chain != nil
	s.mu.Unlock()
	if installed {
		t.Fatal("restore installed a chain from ERROR")
	}
}

// TestAllowDirectDeniedLoopback: on the production path (real egress.Build,
// no withBuilder) an allow-direct slot in DEGRADED cannot reach a loopback
// target: SOCKS reply 0x01, zero sentinel conns, one "fallback dial failed"
// line that carries no secret: the fallback's direct chain still denies loopback.
func TestAllowDirectDeniedLoopback(t *testing.T) {
	r := wgRig(t)
	sentinel := testrig.NewSentinel(t)
	cfg := rigSlot(1, r.route...)
	cfg.Killswitch = config.KillswitchAllowDirect
	cfg.Health.FailThreshold = ksThreshold
	s, ev, procLog := newTestSlot(t, cfg, r.eps,
		withInterval(ksInterval), withBackoff(200*time.Millisecond, time.Second))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, waitUp)
	r.outer.Pause()
	waitState(t, ev, StateDegraded, 10*time.Second)

	if rep, _ := socksConnect(t, s.Addr(), sentinel.Addr()); rep != 0x01 {
		t.Fatalf("CONNECT to loopback reply = %#x, want 0x01", rep)
	}
	time.Sleep(100 * time.Millisecond)
	if b, c := sentinel.Bytes(), sentinel.Conns(); b != 0 || c != 0 {
		t.Fatalf("sentinel saw %d bytes / %d conns, want 0", b, c)
	}
	var failed []LogLine
	for _, l := range s.Logs() {
		if l.Msg == "fallback dial failed" {
			failed = append(failed, l)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("fallback dial failed lines = %d, want 1: %v", len(failed), s.Logs())
	}
	if n := len(fallbackLines(s)); n != 0 {
		t.Fatalf("%d fallback (success) lines, want 0", n)
	}
	priv := r.eps[0].PrivateKey
	if strings.Contains(failed[0].Text, priv) || strings.Contains(procLog.String(), priv) {
		t.Fatalf("fallback dial failed line leaks a secret: %q", failed[0].Text)
	}
	if !strings.Contains(failed[0].Text, "target="+sentinel.Addr()) {
		t.Fatalf("line lacks target %s: %q", sentinel.Addr(), failed[0].Text)
	}
}
