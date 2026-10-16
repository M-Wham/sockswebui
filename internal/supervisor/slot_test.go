package supervisor

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const waitUp = 20 * time.Second

func TestSlotStartUp(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	s, ev, _ := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep})
	if st, _ := s.State(); st != StateStopped {
		t.Fatalf("initial state = %s, want STOPPED", st)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	seen, _ := waitState(t, ev, StateUp, waitUp)
	if !reflect.DeepEqual(seen, []State{StateStarting, StateUp}) {
		t.Fatalf("transitions = %v, want [STARTING UP]", seen)
	}
	if got := socksEcho(t, s.Addr()); got["hop"] != "wgA" {
		t.Fatalf("echo via slot = %v, want hop=wgA", got)
	}
	if err := s.Start(); err == nil {
		t.Fatal("second Start on a running slot succeeded, want error")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitState(t, ev, StateStopped, 5*time.Second)
}

// While STARTING a failed probe is retried after 2, 4, … s (capped at the
// interval), not the full interval, so a slow first probe does not delay UP.
func TestSlotStartUpRetries(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	var probes atomic.Int32
	prober := func(ctx context.Context, d Dialer, u string, to time.Duration) (Result, error) {
		if probes.Add(1) <= 2 {
			return Result{}, errors.New("stub: not yet")
		}
		return Probe(ctx, d, u, to)
	}
	waits := make(chan time.Duration, 8)
	after := func(d time.Duration) <-chan time.Time {
		waits <- d
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	cfg := rigSlot(1, "wgA")
	cfg.Health.IntervalS = 7
	s, ev, _ := newTestSlot(t, cfg, []config.Endpoint{ep}, withProber(prober), withAfter(after))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	seen, _ := waitState(t, ev, StateUp, waitUp)
	if !reflect.DeepEqual(seen, []State{StateStarting, StateUp}) {
		t.Fatalf("transitions = %v, want [STARTING UP]", seen)
	}
	if n := probes.Load(); n < 3 {
		t.Fatalf("probes = %d, want >= 3", n)
	}
	for _, want := range []time.Duration{2 * time.Second, 4 * time.Second} {
		if d := <-waits; d != want {
			t.Fatalf("retry wait = %v, want %v", d, want)
		}
	}
}

func TestSlotStartError(t *testing.T) {
	epB := peerEndpoint(t, "wgB")
	other, otherEv, _ := newTestSlot(t, rigSlot(2, "wgB"), []config.Endpoint{epB})
	if err := other.Start(); err != nil {
		t.Fatalf("Start other: %v", err)
	}
	waitState(t, otherEv, StateUp, waitUp)

	t.Run("port in use", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		ep := peerEndpoint(t, "wgA")
		cfg := rigSlot(1, "wgA")
		cfg.Port = busy.Addr().(*net.TCPAddr).Port
		s, ev, _ := newTestSlot(t, cfg, []config.Endpoint{ep})
		if err := s.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, e := waitState(t, ev, StateError, 5*time.Second)
		if !strings.Contains(e.Msg, "address already in use") {
			t.Fatalf("ERROR msg = %q, want port-in-use reason", e.Msg)
		}
		if st, msg := s.State(); st != StateError || msg != e.Msg {
			t.Fatalf("State() = %s %q, want ERROR %q", st, msg, e.Msg)
		}
	})

	t.Run("bad wg key", func(t *testing.T) {
		const badKey = "c2VjcmV0LWtleS1tYXRlcmlhbC10b28tc2hvcnQ=" // 29 bytes
		ep := peerEndpoint(t, "wgA")
		ep.PrivateKey = badKey
		s, ev, logs := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep})
		if err := s.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, e := waitState(t, ev, StateError, 5*time.Second)
		if e.Msg == "" {
			t.Fatal("ERROR without a message")
		}
		for _, secret := range []string{badKey, "secret-key-material", ep.PeerPublicKey} {
			if strings.Contains(e.Msg, secret) || strings.Contains(logs.String(), secret) {
				t.Fatalf("key material %q leaked: msg=%q logs=%q", secret, e.Msg, logs.String())
			}
		}
	})

	if st, _ := other.State(); st != StateUp {
		t.Fatalf("other slot state = %s, want UP", st)
	}
	if got := socksEcho(t, other.Addr()); got["hop"] != "wgB" {
		t.Fatalf("other slot echo = %v, want hop=wgB", got)
	}
}

func TestSlotPanic(t *testing.T) {
	epA, epB := peerEndpoint(t, "wgA"), peerEndpoint(t, "wgB")
	boom := func(stage string) {
		if stage == stageProbe {
			panic("injected test panic")
		}
	}
	a, aEv, logs := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{epA}, withHook(boom))
	b, bEv, _ := newTestSlot(t, rigSlot(2, "wgB"), []config.Endpoint{epB})
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	_, e := waitState(t, aEv, StateError, 5*time.Second)
	if !strings.Contains(e.Msg, "panic") {
		t.Fatalf("ERROR msg = %q, want it to mention the panic", e.Msg)
	}
	if !strings.Contains(logs.String(), "injected test panic") {
		t.Fatalf("panic not logged: %s", logs.String())
	}
	if rep, _ := socksConnect(t, a.Addr(), testrig.EchoHost+":80"); rep != 0x01 {
		t.Fatalf("CONNECT on ERROR slot reply = %#x, want 0x01", rep)
	}
	waitState(t, bEv, StateUp, waitUp)
	if got := socksEcho(t, b.Addr()); got["hop"] != "wgB" {
		t.Fatalf("slot B echo = %v, want hop=wgB", got)
	}
}

func TestSlotNoImplicitDirect(t *testing.T) {
	cases := []struct {
		name   string
		badKey bool
		fail   error
	}{
		{"invalid endpoint", true, nil},
		{"build error", false, errors.New("stub: build failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := testrig.NewSentinel(t)
			ep := peerEndpoint(t, "wgA")
			if tc.badKey {
				ep.PrivateKey = "bm90LWEta2V5"
			}
			spy := &builderSpy{fail: tc.fail, inner: loopbackDirectBuilder}
			s, ev, _ := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep}, withBuilder(spy.build))
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			waitState(t, ev, StateError, 5*time.Second)
			if rep, _ := socksConnect(t, s.Addr(), sentinel.Addr()); rep != 0x01 {
				t.Fatalf("CONNECT reply = %#x, want 0x01", rep)
			}
			time.Sleep(200 * time.Millisecond) // let any stray connection land
			if got := spy.calls(); !reflect.DeepEqual(got, [][]string{{"wgA"}}) {
				t.Fatalf("Build calls = %q, want exactly [[wgA]] (never an empty route)", got)
			}
			if n, c := sentinel.Bytes(), sentinel.Conns(); n != 0 || c != 0 {
				t.Fatalf("sentinel saw %d bytes / %d conns, want 0", n, c)
			}
		})
	}
}

func TestSlotStop(t *testing.T) {
	boom := func(stage string) {
		if stage == stageProbe {
			panic("injected test panic")
		}
	}
	cases := []struct {
		name string
		from State
		opts []option
	}{
		{"from STARTING", StateStarting, []option{withProber(failProber)}},
		{"from UP", StateUp, nil},
		{"from ERROR", StateError, []option{withHook(boom)}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := peerEndpoint(t, "wgA")
			spy := &builderSpy{}
			opts := append([]option{withBuilder(spy.build)}, tc.opts...)
			s, ev, _ := newTestSlot(t, rigSlot(i+1, "wgA"), []config.Endpoint{ep}, opts...)
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			waitState(t, ev, tc.from, waitUp)
			if tc.from == StateStarting {
				waitBuilt(t, spy)
			}
			addr := s.Addr()
			if addr == "" {
				t.Fatal("no listener address")
			}
			if err := s.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			waitState(t, ev, StateStopped, 5*time.Second)
			if st, _ := s.State(); st != StateStopped {
				t.Fatalf("state = %s, want STOPPED", st)
			}
			assertClosed(t, addr)
			select {
			case <-spy.chain(t, 0).closed:
			default:
				t.Fatal("chain not closed")
			}
			if err := s.Stop(); err != nil {
				t.Fatalf("second Stop: %v", err)
			}
		})
	}
}

func TestSlotRestartAfterStop(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	s, ev, _ := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep})
	for i := 0; i < 3; i++ {
		if err := s.Start(); err != nil {
			t.Fatalf("Start #%d: %v", i, err)
		}
		waitState(t, ev, StateUp, waitUp)
		if got := socksEcho(t, s.Addr()); got["hop"] != "wgA" {
			t.Fatalf("echo #%d = %v", i, got)
		}
		if err := s.Stop(); err != nil {
			t.Fatal(err)
		}
		waitState(t, ev, StateStopped, 5*time.Second)
	}
}

func waitBuilt(t *testing.T, spy *builderSpy) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		spy.mu.Lock()
		n := len(spy.chains)
		spy.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("chain never built")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Start racing Stop must never leave a listener bound after the final Stop.
func TestSlotStartStopRace(t *testing.T) {
	builder := func([]string, []config.Endpoint, bool) (egress.Chain, error) { return nopChain{}, nil }
	s, _, _ := newTestSlot(t, rigSlot(1, "wgA"), nil, withBuilder(builder), withProber(failProber))
	for i := 0; i < 50; i++ {
		done := make(chan struct{})
		go func() { _ = s.Start(); close(done) }()
		_ = s.Stop()
		<-done
		_ = s.Stop()
		if st, _ := s.State(); st != StateStopped || s.Addr() != "" {
			t.Fatalf("iteration %d: state %s addr %q after Stop, want STOPPED with no listener", i, st, s.Addr())
		}
	}
}

// TestServeFailDuringStarting: an ERROR while the build is still running
// cancels the start: the built chain is closed, no probe loop runs and the
// slot stays ERROR, holding no WG session.
func TestServeFailDuringStarting(t *testing.T) {
	var probes, closes atomic.Int32
	hold, built := make(chan struct{}), make(chan struct{}, 1)
	builder := func(route []string, _ []config.Endpoint, _ bool) (egress.Chain, error) {
		if route[0] == "wgB" {
			built <- struct{}{}
			<-hold
		}
		return &countChain{closes: &closes}, nil
	}
	prober := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		probes.Add(1)
		return Result{ExitIP: "10.0.0.1"}, nil
	}
	const interval = 50 * time.Millisecond
	cfg := conflictCfg(map[int][]string{1: {"wgB", "wgA"}, 2: {"wgA"}})
	m, ev := newTestManager(t, cfg, withBuilder(builder), withProber(prober), withInterval(interval))
	if err := m.Start(1); err != nil {
		t.Fatal(err)
	}
	<-built // listener attached, build blocked: STARTING
	m.mu.RLock()
	s := m.slots[1]
	m.mu.RUnlock()
	s.mu.Lock()
	_ = s.ln.Close() // SOCKS serve fails while STARTING
	s.mu.Unlock()
	waitSlots(t, ev, map[int]State{1: StateError}, 5*time.Second)

	close(hold)
	waitFor(t, "built chain closed", 5*time.Second, func() bool { return closes.Load() == 1 })
	time.Sleep(2 * interval)
	if n := probes.Load(); n != 0 {
		t.Fatalf("probe calls = %d, want 0", n)
	}
	if got := status(t, m, 1).State; got != StateError {
		t.Fatalf("slot 1 = %s, want ERROR", got)
	}
	if err := m.Start(2); err != nil {
		t.Fatalf("Start(2) [wgA] with slot 1 in ERROR: %v", err)
	}
	waitSlots(t, ev, map[int]State{2: StateUp}, 5*time.Second)
	if n := closes.Load(); n != 1 {
		t.Fatalf("chain closes = %d, want 1", n)
	}
	if err := m.Stop(1); err != nil {
		t.Fatalf("Stop from ERROR: %v", err)
	}
}

// countChain counts Close calls.
type countChain struct {
	fakeChain
	closes *atomic.Int32
}

func (c *countChain) Close() error { c.closes.Add(1); return nil }
