package supervisor

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// policyRecorder wraps loopbackDirectBuilder and keeps every direct chain
// it hands out, so a test can read the blockLAN policy each was built with.
type policyRecorder struct {
	mu     sync.Mutex
	direct []*testrig.DirectChain
}

func (p *policyRecorder) build(route []string, eps []config.Endpoint, blockLAN bool) (egress.Chain, error) {
	ch, err := loopbackDirectBuilder(route, eps, blockLAN)
	if dc, ok := ch.(*testrig.DirectChain); ok && err == nil {
		p.mu.Lock()
		p.direct = append(p.direct, dc)
		p.mu.Unlock()
	}
	return ch, err
}

// policies returns the blockLAN policy of every direct chain built so far.
func (p *policyRecorder) policies() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]bool, 0, len(p.direct))
	for _, dc := range p.direct {
		out = append(out, dc.BlockLAN())
	}
	return out
}

// directSlot is an UP-able empty-route slot probing a loopback echo.
func directSlot(t *testing.T, id int, blockLAN bool) config.Slot {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := testrig.ServeEcho(ln, "direct")
	t.Cleanup(func() { _ = ln.Close(); <-done })
	cfg := rigSlot(id)
	cfg.Route = []string{}
	cfg.BlockLANTargets = blockLAN
	cfg.Health.URL = "http://" + ln.Addr().String() + "/"
	return cfg
}

// TestBlockLANBothDirectPaths: both device-route paths (an empty-route slot
// and the allow-direct DEGRADED fallback) build their direct chain with the
// slot's block_lan_targets (the LAN-class deny step), and a config
// change rebuilds with the new value (a running slot is never mutated in place).
func TestBlockLANBothDirectPaths(t *testing.T) {
	for _, flag := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty route blockLAN=%v", flag), func(t *testing.T) {
			rec := &policyRecorder{}
			s, ev, _ := newTestSlot(t, directSlot(t, 1, flag), nil, withBuilder(rec.build))
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			waitState(t, ev, StateUp, waitUp)
			got := rec.policies()
			if len(got) != 1 || got[0] != flag {
				t.Fatalf("direct chain policies = %v, want [%v]", got, flag)
			}
		})
		t.Run(fmt.Sprintf("allow-direct fallback blockLAN=%v", flag), func(t *testing.T) {
			sentinel := testrig.NewSentinel(t)
			rec := &policyRecorder{}
			cfg := rigSlot(1, "wgA")
			cfg.Killswitch = config.KillswitchAllowDirect
			cfg.BlockLANTargets = flag
			s := NewSlot(cfg, nil, nil, nil, withBuilder(rec.build))
			s.mu.Lock()
			s.state = StateDegraded
			s.mu.Unlock()
			c, err := (gate{s}).DialContext(context.Background(), "tcp", sentinel.Addr())
			if err != nil {
				t.Fatalf("fallback dial: %v", err)
			}
			_ = c.Close()
			got := rec.policies()
			if len(got) != 1 || got[0] != flag {
				t.Fatalf("fallback chain policies = %v, want [%v]", got, flag)
			}
		})
	}

	t.Run("apply rebuilds with new value", func(t *testing.T) {
		rec := &policyRecorder{}
		cfg := config.Config{Slots: []config.Slot{directSlot(t, 1, false)}}
		m, ev := newTestManager(t, cfg, withBuilder(rec.build))
		if err := m.Run(); err != nil {
			t.Fatal(err)
		}
		waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)
		next := config.Config{Slots: []config.Slot{cfg.Slots[0]}}
		next.Slots[0].BlockLANTargets = true
		if err := m.Apply(next); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)
		got := rec.policies()
		if len(got) != 2 || got[0] || !got[1] {
			t.Fatalf("direct chain policies = %v, want [false true]", got)
		}
	})

	// Real builder (no seam): the always-deny set is unaffected by the flag;
	// a loopback target via the allow-direct fallback still gets 0x01.
	t.Run("real builder allow-direct loopback denied", func(t *testing.T) {
		r := wgRig(t)
		sentinel := testrig.NewSentinel(t)
		cfg := rigSlot(1, r.route...)
		cfg.Killswitch = config.KillswitchAllowDirect
		cfg.BlockLANTargets = true
		cfg.Health.FailThreshold = ksThreshold
		s, ev, _ := newTestSlot(t, cfg, r.eps,
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
	})
}
