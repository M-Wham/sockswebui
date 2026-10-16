package supervisor

import (
	"io"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// managerRig holds the endpoints for the four test routes: two WG peers
// (wgA, plus wgA2: a distinct endpoint with its own client key on wgA's
// relay, since one WG key may hold only one live session), socksC on wgA's netstack and socksD on loopback. Each proxy maps the echo
// host to its own loopback echo reporting the proxy's name as hop.
type managerRig struct {
	peerA *testrig.WGPeer
	eps   []config.Endpoint
}

// loopEcho serves a testrig echo named hop on loopback and returns its addr.
func loopEcho(t *testing.T, hop string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := testrig.ServeEcho(ln, hop)
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return ln.Addr().String()
}

func newManagerRig(t *testing.T) managerRig {
	t.Helper()
	pA, pB := testrig.NewWGPeer(t, "wgA"), testrig.NewWGPeer(t, "wgB")
	epA, epB := pA.ClientConfig(), pB.ClientConfig()
	epA.ID, epB.ID = "wgA", "wgB"
	// wgA2 reaches the same relay as wgA with its own device key: running
	// slots never share a WG endpoint (answer B).
	epA2 := pA.AddClient(t, netip.AddrFrom4([4]byte{10, 64, 0, 3}))
	epA2.ID, epA2.Name = "wgA2", "wgA2"

	ln, err := pA.Net().ListenTCPAddrPort(netip.AddrPortFrom(pA.TunnelIP(), 1080))
	if err != nil {
		t.Fatal(err)
	}
	var host net.Dialer // test-only onward dial from the fake upstream to the loopback echo
	upC := testrig.NewSOCKS5Upstream(ln, testrig.UpstreamConfig{
		Name: "socksC", Dial: host.DialContext,
		Hosts: map[string]string{testrig.EchoHost: loopEcho(t, "socksC")},
	})
	t.Cleanup(func() { _ = upC.Close() })
	upD := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{
		Name: "socksD", Username: "ud", Password: "pd-secret",
		Hosts: map[string]string{testrig.EchoHost: loopEcho(t, "socksD")},
	})
	dHost, dPort, _ := net.SplitHostPort(upD.Addr())
	port, _ := strconv.Atoi(dPort)
	return managerRig{peerA: pA, eps: []config.Endpoint{
		epA, epB, epA2,
		{ID: "socksC", Name: "socksC", Kind: config.KindSOCKS5, Host: pA.TunnelIP().String(), Port: 1080},
		{ID: "socksD", Name: "socksD", Kind: config.KindSOCKS5, Host: dHost, Port: port, Username: "ud", Password: "pd-secret"},
	}}
}

// newTestManager builds a manager for cfg with a large event subscription;
// it is closed at cleanup.
func newTestManager(t *testing.T, cfg config.Config, opts ...option) (*Manager, <-chan Event) {
	t.Helper()
	log, _ := newLogger()
	m := New(Options{Config: cfg, Logger: log, slotOpts: opts, tick: 100 * time.Millisecond})
	ch, cancel := m.bus.Subscribe(1024)
	t.Cleanup(func() { m.Close(); cancel() })
	return m, ch
}

// waitSlots reads state events until every slot in want reached its state,
// returning the states seen per slot.
func waitSlots(t *testing.T, ch <-chan Event, want map[int]State, timeout time.Duration) map[int][]State {
	t.Helper()
	seen := map[int][]State{}
	pending := len(want)
	done := map[int]bool{}
	deadline := time.After(timeout)
	for pending > 0 {
		select {
		case e := <-ch:
			if e.Type != EventState {
				continue
			}
			seen[e.Slot] = append(seen[e.Slot], e.State)
			if w, ok := want[e.Slot]; ok && e.State == w && !done[e.Slot] {
				done[e.Slot] = true
				pending--
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %v; seen %v", want, seen)
		}
	}
	return seen
}

func status(t *testing.T, m *Manager, id int) SlotStatus {
	t.Helper()
	for _, s := range m.Snapshot() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("slot %d not in snapshot", id)
	return SlotStatus{}
}

func TestManagerFourSlots(t *testing.T) {
	r := newManagerRig(t)
	routes := map[int][]string{1: {"wgA"}, 2: {"wgB"}, 3: {"wgA2", "socksC"}, 4: {"socksD"}}
	cfg := config.Config{Endpoints: r.eps}
	for id := 1; id <= 4; id++ {
		cfg.Slots = append(cfg.Slots, rigSlot(id, routes[id]...))
	}
	m, ev := newTestManager(t, cfg)
	if err := m.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp, 2: StateUp, 3: StateUp, 4: StateUp}, waitUp)
	want := map[int]string{1: "wgA", 2: "wgB", 3: "socksC", 4: "socksD"}
	for id := 1; id <= 4; id++ {
		if got := socksEcho(t, status(t, m, id).Addr); got["hop"] != want[id] {
			t.Fatalf("slot %d %v echo = %v, want hop=%s", id, routes[id], got, want[id])
		}
	}
	for _, s := range m.Snapshot() {
		if s.State != StateUp {
			t.Fatalf("slot %d state = %s, want UP (all concurrent)", s.ID, s.State)
		}
	}
}

func TestManagerAutostart(t *testing.T) {
	r := newManagerRig(t)
	cfg := config.Config{Endpoints: r.eps}
	for id := 1; id <= 4; id++ {
		sc := rigSlot(id, "wgA")
		if id >= 3 {
			sc.Route = []string{"wgB"}
		}
		sc.Enabled = id%2 == 1 // 1 and 3
		cfg.Slots = append(cfg.Slots, sc)
	}
	m, ev := newTestManager(t, cfg)
	for _, s := range m.Snapshot() {
		if s.State != StateStopped {
			t.Fatalf("before Run: slot %d = %s, want STOPPED", s.ID, s.State)
		}
	}
	if err := m.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen := waitSlots(t, ev, map[int]State{1: StateUp, 3: StateUp}, waitUp)
	if len(seen[2]) != 0 || len(seen[4]) != 0 {
		t.Fatalf("disabled slots saw events: %v", seen)
	}
	for id, want := range map[int]State{1: StateUp, 2: StateStopped, 3: StateUp, 4: StateStopped} {
		if got := status(t, m, id).State; got != want {
			t.Fatalf("slot %d = %s, want %s", id, got, want)
		}
	}
}

func TestManagerApply(t *testing.T) {
	r := newManagerRig(t)
	cfg := config.Config{Endpoints: r.eps, Slots: []config.Slot{rigSlot(1, "wgA"), rigSlot(2, "wgB")}}
	m, ev := newTestManager(t, cfg)
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp, 2: StateUp}, waitUp)
	addr2 := status(t, m, 2).Addr

	next := config.Config{Endpoints: r.eps, Slots: []config.Slot{rigSlot(1, "socksD"), rigSlot(2, "wgB")}}
	next.Slots[1].Name = "renamed" // name-only change: no restart (disable stops: TestApplyDisableStops)
	if err := m.Apply(next); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	seen := waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)
	if want := []State{StateStopped, StateStarting, StateUp}; !reflect.DeepEqual(seen[1], want) {
		t.Fatalf("slot 1 transitions = %v, want %v", seen[1], want)
	}
	if len(seen[2]) != 0 {
		t.Fatalf("unchanged slot 2 restarted: %v", seen[2])
	}
	if got := status(t, m, 2); got.State != StateUp || got.Addr != addr2 {
		t.Fatalf("slot 2 = %+v, want UP on %s", got, addr2)
	}
	if got := socksEcho(t, status(t, m, 1).Addr); got["hop"] != "socksD" {
		t.Fatalf("slot 1 after apply echo = %v, want hop=socksD", got)
	}

	// A changed endpoint referenced by the route also restarts; removing a slot stops it.
	eps := append([]config.Endpoint(nil), r.eps...)
	eps[4].Password = "changed"
	if err := m.Apply(config.Config{Endpoints: eps, Slots: []config.Slot{rigSlot(1, "socksD")}}); err != nil {
		t.Fatal(err)
	}
	seen = waitSlots(t, ev, map[int]State{1: StateStarting, 2: StateStopped}, 5*time.Second)
	if seen[1][0] != StateStopped {
		t.Fatalf("slot 1 transitions = %v, want STOPPED first", seen[1])
	}
	if n := len(m.Snapshot()); n != 1 {
		t.Fatalf("snapshot has %d slots after removal, want 1", n)
	}
	assertClosed(t, addr2)
}

func TestThroughput(t *testing.T) {
	r := wgRig(t)
	cfg := config.Config{Endpoints: r.eps, Slots: []config.Slot{rigSlot(1, r.route...)}}
	// Ticks are driven explicitly: with a free-running ticker the upstream
	// write can land wholly in one interval and the echo in later ones, so no
	// single event would carry both rates.
	log, _ := newLogger()
	ticks := make(chan time.Time)
	m := New(Options{Config: cfg, Logger: log, ticks: ticks})
	ev, cancel := m.bus.Subscribe(1024)
	t.Cleanup(func() { m.Close(); cancel() })
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)

	const n = 256 << 10
	rep, c := socksConnect(t, status(t, m, 1).Addr, r.stream)
	if rep != 0 {
		t.Fatalf("CONNECT reply = %#x", rep)
	}
	go func() { _, _ = c.Write(make([]byte, n)) }()
	if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
		t.Fatalf("read back: %v", err)
	}
	waitFor(t, "byte counters", 5*time.Second, func() bool {
		s := status(t, m, 1)
		return s.BytesIn >= n && s.BytesOut >= n
	})
	select { // one tick after the whole transfer: its interval holds both directions
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("throughput loop did not take the tick")
	}
	totals, rated := false, false
	deadline := time.After(5 * time.Second)
	for !totals || !rated {
		select {
		case e := <-ev:
			if e.Type != EventThroughput || e.Slot != 1 || e.Throughput == nil {
				continue
			}
			tp := e.Throughput
			totals = totals || (tp.BytesIn >= n && tp.BytesOut >= n)
			rated = rated || (tp.RateIn > 0 && tp.RateOut > 0)
		case <-deadline:
			t.Fatalf("throughput events: totals=%v rated=%v, want both", totals, rated)
		}
	}
}

// drainStates returns the state events for slot id that arrive within d.
func drainStates(ch <-chan Event, id int, d time.Duration) []State {
	var seen []State
	deadline := time.After(d)
	for {
		select {
		case e := <-ch:
			if e.Type == EventState && e.Slot == id {
				seen = append(seen, e.State)
			}
		case <-deadline:
			return seen
		}
	}
}

// TestApplyDisableStops: enabled true→false stops a running slot
// (enabled = desired running); false→true alone does not start it.
func TestApplyDisableStops(t *testing.T) {
	f := newFakeNet()
	cfg := conflictCfg(map[int][]string{1: {"wgA"}}, 1)
	m, ev := newTestManager(t, cfg, f.opts()...)
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
	addr := status(t, m, 1).Addr

	off := conflictCfg(map[int][]string{1: {"wgA"}})
	if err := m.Apply(off); err != nil {
		t.Fatalf("Apply disable: %v", err)
	}
	waitSlots(t, ev, map[int]State{1: StateStopped}, 5*time.Second)
	if got := status(t, m, 1); got.State != StateStopped || got.Enabled {
		t.Fatalf("slot 1 = %+v, want STOPPED, disabled", got)
	}
	assertClosed(t, addr)

	if err := m.Apply(cfg); err != nil {
		t.Fatalf("Apply re-enable: %v", err)
	}
	if seen := drainStates(ev, 1, 200*time.Millisecond); len(seen) != 0 {
		t.Fatalf("re-enable alone changed slot 1: %v", seen)
	}
	if got := status(t, m, 1); got.State != StateStopped || !got.Enabled {
		t.Fatalf("slot 1 = %+v, want STOPPED, enabled", got)
	}
	// A manual start followed by another disable stops it again.
	if err := m.Start(1); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
	if err := m.Apply(off); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateStopped}, 5*time.Second)
}

// TestApplyNameOnlyNoRestart guards sameSlot: a rename does not restart.
func TestApplyNameOnlyNoRestart(t *testing.T) {
	f := newFakeNet()
	cfg := conflictCfg(map[int][]string{1: {"wgA"}}, 1)
	m, ev := newTestManager(t, cfg, f.opts()...)
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
	addr := status(t, m, 1).Addr
	cfg.Slots[0].Name = "renamed"
	if err := m.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if seen := drainStates(ev, 1, 200*time.Millisecond); len(seen) != 0 {
		t.Fatalf("rename restarted slot 1: %v", seen)
	}
	if got := status(t, m, 1); got.State != StateUp || got.Addr != addr || got.Name != "renamed" {
		t.Fatalf("slot 1 = %+v, want UP on %s named renamed", got, addr)
	}
}
