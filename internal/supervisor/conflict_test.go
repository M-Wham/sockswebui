package supervisor

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
)

// conflictEps: two WG endpoints and one shared proxy. The fake builder and
// prober never touch them, so only Kind and ID matter.
var conflictEps = []config.Endpoint{
	{ID: "wgA", Name: "wgA", Kind: config.KindWireGuard},
	{ID: "wgB", Name: "wgB", Kind: config.KindWireGuard},
	{ID: "socksC", Name: "socksC", Kind: config.KindSOCKS5, Host: "127.0.0.1", Port: 1},
}

// fakeChain records its route; it never dials.
type fakeChain struct{ route []string }

func (fakeChain) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("fake chain: no dial")
}
func (fakeChain) Close() error { return nil }

// fakeNet counts builds per route key, can hold builds (STARTING) and fail
// probes (DEGRADED) for routes whose first hop is listed.
type fakeNet struct {
	mu     sync.Mutex
	builds map[string]int
	hold   chan struct{} // non-nil: builds whose first hop is hop block until closed
	hop    string
	fail   atomic.Bool // every probe fails
}

func newFakeNet() *fakeNet { return &fakeNet{builds: map[string]int{}} }

func (f *fakeNet) build(route []string, _ []config.Endpoint, _ bool) (egress.Chain, error) {
	f.mu.Lock()
	f.builds[strings.Join(route, ",")]++
	hold, hop := f.hold, f.hop
	f.mu.Unlock()
	if hold != nil && len(route) > 0 && route[0] == hop {
		<-hold
	}
	return fakeChain{route: route}, nil
}

func (f *fakeNet) probe(_ context.Context, _ Dialer, _ string, _ time.Duration) (Result, error) {
	if f.fail.Load() {
		return Result{}, errors.New("stub: probe failed")
	}
	return Result{ExitIP: "10.0.0.1"}, nil
}

// release unblocks held builds; later builds no longer hold.
func (f *fakeNet) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hold != nil {
		close(f.hold)
		f.hold = nil
	}
}

func (f *fakeNet) count(route ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds[strings.Join(route, ",")]
}

func (f *fakeNet) opts() []option {
	return []option{withBuilder(f.build), withProber(f.probe), withInterval(20 * time.Millisecond),
		withBackoff(10*time.Millisecond, 20*time.Millisecond)}
}

func conflictCfg(routes map[int][]string, enabled ...int) config.Config {
	// start_on_enable is the product default; soeCfg overrides it.
	cfg := config.Config{Endpoints: conflictEps, Settings: config.Settings{StartOnEnable: true}}
	for id := 1; id <= config.MaxSlots; id++ {
		if r, ok := routes[id]; ok {
			sc := rigSlot(id, r...)
			sc.Enabled = false
			for _, e := range enabled {
				sc.Enabled = sc.Enabled || e == id
			}
			cfg.Slots = append(cfg.Slots, sc)
		}
	}
	return cfg
}

func assertInUse(t *testing.T, err error, slot int, ep string) {
	t.Helper()
	if !errors.Is(err, ErrEndpointInUse) {
		t.Fatalf("err = %v, want ErrEndpointInUse", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "slot "+strconv.Itoa(slot)) || !strings.Contains(msg, ep) {
		t.Fatalf("err %q does not name slot %d and endpoint %s", msg, slot, ep)
	}
	var e *EndpointInUseError
	if !errors.As(err, &e) || e.Slot != slot || e.Endpoint != ep {
		t.Fatalf("err %v: want *EndpointInUseError{Slot:%d, Endpoint:%s}", err, slot, ep)
	}
}

func TestStartRejectsWGEndpointInUse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		route1 []string
		route2 []string
	}{
		{"first hop", []string{"wgA"}, []string{"wgA", "socksC"}},
		{"second hop", []string{"wgB", "wgA"}, []string{"wgA"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNet()
			m, ev := newTestManager(t, conflictCfg(map[int][]string{1: tc.route1, 2: tc.route2}), f.opts()...)
			if err := m.Start(1); err != nil {
				t.Fatal(err)
			}
			waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
			assertInUse(t, m.Start(2), 1, "wgA")
			time.Sleep(100 * time.Millisecond)
			if n := f.count(tc.route2...); n != 0 {
				t.Fatalf("builder called %d times for slot 2, want 0", n)
			}
			if got := status(t, m, 2).State; got != StateStopped {
				t.Fatalf("slot 2 = %s, want STOPPED", got)
			}
			if got := status(t, m, 1).State; got != StateUp {
				t.Fatalf("slot 1 = %s, want UP", got)
			}
		})
	}
}

// TestCheckStart: the read-only pre-check reports a WG conflict with another
// running slot and starts nothing.
func TestCheckStart(t *testing.T) {
	f := newFakeNet()
	cfg := conflictCfg(map[int][]string{1: {"wgB", "wgA", "socksC"}, 2: {"wgA"}, 3: {"socksC"}, 4: {"wgB"}})
	m, ev := newTestManager(t, cfg, f.opts()...)
	sc := func(id int, route ...string) config.Slot { return rigSlot(id, route...) }

	if err := m.CheckStart(sc(2, "wgA"), conflictEps); err != nil {
		t.Fatalf("nothing running: %v", err)
	}
	if err := m.Start(1); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)

	assertInUse(t, m.CheckStart(sc(2, "wgA"), conflictEps), 1, "wgA")
	assertInUse(t, m.CheckStart(sc(4, "wgB"), conflictEps), 1, "wgB")
	if err := m.CheckStart(sc(3, "socksC"), conflictEps); err != nil {
		t.Fatalf("proxy-only overlap: %v", err)
	}
	if err := m.CheckStart(sc(1, "wgA", "wgB"), conflictEps); err != nil {
		t.Fatalf("own running instance: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	for _, id := range []int{2, 3, 4} {
		if got := status(t, m, id).State; got != StateStopped {
			t.Fatalf("CheckStart started slot %d (%s)", id, got)
		}
	}
	if n := f.count("wgA") + f.count("wgB") + f.count("socksC"); n != 0 {
		t.Fatalf("CheckStart built %d chains, want 0", n)
	}
	if err := m.Stop(1); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckStart(sc(2, "wgA"), conflictEps); err != nil {
		t.Fatalf("after slot 1 stopped: %v", err)
	}
}

func TestStartEndpointInUseStates(t *testing.T) {
	routes := map[int][]string{1: {"wgA"}, 2: {"wgA", "socksC"}}
	for _, tc := range []struct {
		name  string
		state State
		setup func(f *fakeNet)
	}{
		{"STARTING", StateStarting, func(f *fakeNet) { f.hold, f.hop = make(chan struct{}), "wgA" }},
		{"UP", StateUp, func(*fakeNet) {}},
		{"DEGRADED", StateDegraded, func(*fakeNet) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNet()
			tc.setup(f)
			m, ev := newTestManager(t, conflictCfg(routes), f.opts()...)
			if err := m.Start(1); err != nil {
				t.Fatal(err)
			}
			if tc.state == StateDegraded {
				waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
				f.fail.Store(true)
			}
			waitFor(t, "slot 1 "+string(tc.state), 5*time.Second, func() bool {
				return status(t, m, 1).State == tc.state
			})
			assertInUse(t, m.Start(2), 1, "wgA")
			if got := status(t, m, 2).State; got != StateStopped {
				t.Fatalf("slot 2 = %s, want STOPPED", got)
			}
			f.release() // a held build would block Stop's teardown
			if err := m.Stop(1); err != nil {
				t.Fatal(err)
			}
			if err := m.Start(2); err != nil {
				t.Fatalf("Start(2) after slot 1 stopped: %v", err)
			}
		})
	}
	t.Run("ERROR", func(t *testing.T) {
		f := newFakeNet()
		m, _ := newTestManager(t, conflictCfg(routes), append(f.opts(), withHook(func(st string) {
			if st == stageBuild {
				panic("stub: boom")
			}
		}))...)
		if err := m.Start(1); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "slot 1 ERROR", 5*time.Second, func() bool { return status(t, m, 1).State == StateError })
		if err := m.Start(2); err != nil && errors.Is(err, ErrEndpointInUse) {
			t.Fatalf("Start(2) with slot 1 in ERROR: %v", err)
		}
	})
}

func TestStartSharedProxyAllowed(t *testing.T) {
	f := newFakeNet()
	m, ev := newTestManager(t, conflictCfg(map[int][]string{1: {"wgA", "socksC"}, 2: {"wgB", "socksC"}}), f.opts()...)
	for _, id := range []int{1, 2} {
		if err := m.Start(id); err != nil {
			t.Fatalf("Start(%d): %v", id, err)
		}
	}
	waitSlots(t, ev, map[int]State{1: StateUp, 2: StateUp}, 5*time.Second)
}

func TestAutostartEndpointInUse(t *testing.T) {
	routes := map[int][]string{1: {"wgA"}, 2: {"wgB"}, 3: {"wgA", "socksC"}}
	check := func(t *testing.T, m *Manager, err error, ev <-chan Event) {
		t.Helper()
		assertInUse(t, err, 1, "wgA")
		waitSlots(t, ev, map[int]State{3: StateError}, 5*time.Second)
		waitFor(t, "slot 1 UP", 5*time.Second, func() bool { return status(t, m, 1).State == StateUp })
		s3 := status(t, m, 3)
		if s3.State != StateError || !strings.Contains(s3.Msg, "slot 1") || !strings.Contains(s3.Msg, "wgA") {
			t.Fatalf("slot 3 = %+v, want ERROR naming slot 1 and wgA", s3)
		}
	}
	t.Run("Run", func(t *testing.T) {
		f := newFakeNet()
		m, ev := newTestManager(t, conflictCfg(routes, 1, 3), f.opts()...)
		check(t, m, m.Run(), ev)
		if n := f.count("wgA", "socksC"); n != 0 {
			t.Fatalf("slot 3 builder calls = %d, want 0", n)
		}
	})
	t.Run("Apply", func(t *testing.T) {
		f := newFakeNet()
		m, ev := newTestManager(t, conflictCfg(map[int][]string{1: routes[1]}, 1), f.opts()...)
		if err := m.Run(); err != nil {
			t.Fatal(err)
		}
		waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
		check(t, m, m.Apply(conflictCfg(routes, 1, 3)), ev)
		if n := f.count("wgA", "socksC"); n != 0 {
			t.Fatalf("slot 3 builder calls = %d, want 0", n)
		}
	})
}

// TestStartConflictConcurrent: two concurrent Starts on conflicting slots →
// exactly one wins.
func TestStartConflictConcurrent(t *testing.T) {
	for i := 0; i < 20; i++ {
		f := newFakeNet()
		m, _ := newTestManager(t, conflictCfg(map[int][]string{1: {"wgA"}, 2: {"wgA", "socksC"}}), f.opts()...)
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for _, id := range []int{1, 2} {
			wg.Add(1)
			go func() { defer wg.Done(); errs[id] = m.Start(id) }()
		}
		wg.Wait()
		if (errs[1] == nil) == (errs[2] == nil) {
			t.Fatalf("run %d: errs = %v / %v, want exactly one success", i, errs[1], errs[2])
		}
	}
}
