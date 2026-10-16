package supervisor

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const (
	rotUserPrefix = "acct-sid-"
	rotIDOld      = "AAAAAAAAAA"
	rotIDNew      = "BBBBBBBBBB"
)

// rotRig is a loopback SOCKS5 upstream that accepts any username with the
// account prefix and records the credentials it receives, plus the endpoint
// for it ("sx", session template in user or pass) and an unrelated plain
// upstream ("sy").
type rotRig struct {
	up  *testrig.SOCKS5Upstream
	cfg config.Config
}

// newRotRig builds slot 1 [sx] and slot 2 [sy]. user/pass are sx's stored
// fields; wantPass is what the upstream requires ("" = any).
func newRotRig(t *testing.T, user, pass, wantPass string) rotRig {
	t.Helper()
	up := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{
		Name: "sx", Password: wantPass,
		AcceptUser: func(u string) bool { return strings.HasPrefix(u, rotUserPrefix) },
		Hosts:      map[string]string{testrig.EchoHost: loopEcho(t, "sx")},
	})
	other := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{
		Name: "sy", Hosts: map[string]string{testrig.EchoHost: loopEcho(t, "sy")},
	})
	ep := func(id, addr string) config.Endpoint {
		h, p, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(p)
		return config.Endpoint{ID: id, Name: id + "-name", Kind: config.KindSOCKS5, Host: h, Port: port}
	}
	sx := ep("sx", up.Addr())
	sx.Username, sx.Password = user, pass
	sx.Session = config.Session{Charset: config.SessionAlnum, Length: 10, CurrentID: rotIDOld}
	return rotRig{up: up, cfg: config.Config{
		Endpoints: []config.Endpoint{sx, ep("sy", other.Addr())},
		Slots:     []config.Slot{rigSlot(1, "sx"), rigSlot(2, "sy")},
	}}
}

// withID returns cfg with endpoint sx's current_id set to id (deep copy of
// the endpoint list so the manager's view is not aliased).
func withID(cfg config.Config, id string) config.Config {
	cfg.Endpoints = slices.Clone(cfg.Endpoints)
	cfg.Endpoints[0].Session.CurrentID = id
	return cfg
}

func lastUser(t *testing.T, up *testrig.SOCKS5Upstream) string {
	t.Helper()
	cs := up.Credentials()
	if len(cs) == 0 {
		t.Fatal("upstream recorded no credentials")
	}
	return cs[len(cs)-1].User
}

func rotateLines(m *Manager, id int) []string {
	var out []string
	for _, l := range m.Logs(id) {
		if strings.HasPrefix(l.Msg, "session rotated") {
			out = append(out, l.Msg)
		}
	}
	return out
}

func TestRotateRebuildsAndNewIDReachesUpstream(t *testing.T) {
	r := newRotRig(t, rotUserPrefix+config.SessionToken, "pw-fixed", "")
	m, ch := newTestManager(t, r.cfg)
	if err := m.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitSlots(t, ch, map[int]State{1: StateUp, 2: StateUp}, 10*time.Second)
	if got := socksEcho(t, status(t, m, 1).Addr)["hop"]; got != "sx" {
		t.Fatalf("hop = %q, want sx", got)
	}
	if u := lastUser(t, r.up); u != rotUserPrefix+rotIDOld {
		t.Fatalf("upstream user = %q, want old id", u)
	}
	rep, open := socksConnect(t, status(t, m, 1).Addr, testrig.EchoHost+":80")
	if rep != 0 {
		t.Fatalf("CONNECT = %#x", rep)
	}
	m.mu.RLock()
	other := m.slots[2]
	m.mu.RUnlock()

	if err := m.Apply(withID(r.cfg, rotIDNew)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	_ = open.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := open.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("open connection on rotated slot not closed: %v", err)
	}
	waitSlots(t, ch, map[int]State{1: StateUp}, 10*time.Second)
	if got := socksEcho(t, status(t, m, 1).Addr)["hop"]; got != "sx" {
		t.Fatalf("hop after rotate = %q, want sx", got)
	}
	if u := lastUser(t, r.up); u != rotUserPrefix+rotIDNew {
		t.Fatalf("upstream user after rotate = %q, want new id", u)
	}
	m.mu.RLock()
	same := m.slots[2] == other
	m.mu.RUnlock()
	if !same {
		t.Fatal("slot 2 (not using the endpoint) was rebuilt")
	}
}

func TestRotateBackoffRebuildKeepsID(t *testing.T) {
	r := newRotRig(t, rotUserPrefix+config.SessionToken, "pw-fixed", "")
	var down atomic.Bool
	prober := func(ctx context.Context, d Dialer, url string, to time.Duration) (Result, error) {
		if down.Load() {
			return failProber(ctx, d, url, to)
		}
		return Probe(ctx, d, url, to)
	}
	var rebuilds atomic.Int32
	hook := func(st string) {
		if st == stageRebuild {
			rebuilds.Add(1)
		}
	}
	clk := newFakeClock()
	cfg := r.cfg
	cfg.Slots = cfg.Slots[:1]
	m, ch := newTestManager(t, cfg, withProber(prober), withInterval(200*time.Millisecond),
		withBackoffAfter(clk.after), withHook(hook))
	if err := m.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitSlots(t, ch, map[int]State{1: StateUp}, 10*time.Second)
	down.Store(true)
	waitSlots(t, ch, map[int]State{1: StateDegraded}, 10*time.Second)
	down.Store(false)
	clk.next(t).fire <- time.Now()
	waitSlots(t, ch, map[int]State{1: StateUp}, 10*time.Second)
	if rebuilds.Load() != 1 {
		t.Fatalf("rebuilds = %d, want 1", rebuilds.Load())
	}
	socksEcho(t, status(t, m, 1).Addr)
	for _, c := range r.up.Credentials() {
		if c.User != rotUserPrefix+rotIDOld {
			t.Fatalf("upstream saw %q, want unchanged id %s", c.User, rotIDOld)
		}
	}
	if got := rotateLines(m, 1); len(got) != 0 {
		t.Fatalf("backoff rebuild logged rotation: %q", got)
	}
}

func TestRotateLogLine(t *testing.T) {
	r := newRotRig(t, rotUserPrefix+config.SessionToken, "pw-fixed", "")
	m, ch := newTestManager(t, r.cfg)
	if err := m.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitSlots(t, ch, map[int]State{1: StateUp, 2: StateUp}, 10*time.Second)
	for _, id := range []int{1, 2} {
		if got := rotateLines(m, id); len(got) != 0 {
			t.Fatalf("slot %d: first start logged rotation: %q", id, got)
		}
	}
	if err := m.Apply(withID(r.cfg, rotIDNew)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{"session rotated endpoint=sx-name id=" + rotIDNew}
	if got := rotateLines(m, 1); !slices.Equal(got, want) {
		t.Fatalf("slot 1 rotate lines = %q, want %q", got, want)
	}
	if got := rotateLines(m, 2); len(got) != 0 {
		t.Fatalf("slot 2 (unaffected) logged rotation: %q", got)
	}
	// Re-applying the same config is not a rotation.
	if err := m.Apply(withID(r.cfg, rotIDNew)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := rotateLines(m, 1); len(got) != 1 {
		t.Fatalf("unchanged apply logged rotation again: %q", got)
	}
}

func TestRotateRedactsRenderedPassword(t *testing.T) {
	stored := "pw-" + config.SessionToken + "-tail"
	rendered := strings.ReplaceAll(stored, config.SessionToken, rotIDOld)
	// The upstream wants a different password: every auth fails.
	r := newRotRig(t, rotUserPrefix+"fixed", stored, "wrong-pass")
	m, ch := newTestManager(t, r.cfg)
	_ = m.Run()
	waitSlots(t, ch, map[int]State{2: StateUp}, 10*time.Second)
	// Let slot 1 probe (and fail auth) a few times.
	time.Sleep(1500 * time.Millisecond)
	if rep, c, err := socksTry(status(t, m, 1).Addr, testrig.EchoHost+":80", 2*time.Second); err == nil && rep == 0 {
		_ = c.Close()
	}
	m.mu.RLock()
	s := m.slots[1]
	m.mu.RUnlock()
	// Defence in depth: even a log line that carries the values is scrubbed,
	// while the (loggable) bare session id survives.
	s.log.Info("leak check " + rendered + " " + stored + " id=" + rotIDOld)

	check := func(where, text string) {
		t.Helper()
		for _, secret := range []string{rendered, stored} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s contains password %q: %s", where, secret, text)
			}
		}
	}
	var sawID bool
	for _, id := range []int{1, 2} {
		for _, l := range m.Logs(id) {
			check("Logs", l.Msg+"\n"+l.Text)
			sawID = sawID || strings.Contains(l.Msg, "id="+rotIDOld)
		}
	}
	if !sawID {
		t.Fatal("bare session id was redacted")
	}
	for _, st := range m.Snapshot() {
		check("Snapshot.Msg", st.Msg)
	}
	if len(r.up.Credentials()) != 0 {
		t.Fatal("upstream accepted credentials it should have rejected")
	}
}

func TestRotateRebuiltSlots(t *testing.T) {
	cfg := config.Config{Slots: []config.Slot{
		{ID: 1, Route: []string{"wg", "sx"}, Enabled: true},
		{ID: 2, Route: []string{"sx"}, Enabled: true},
		{ID: 3, Route: []string{"sx"}},
		{ID: 4, Route: []string{"sy"}, Enabled: true},
	}}
	cases := []struct {
		name   string
		states map[int]State
		want   []int
	}{
		{"running and error", map[int]State{1: StateUp, 2: StateError, 3: StateStopped, 4: StateUp}, []int{1, 2}},
		{"starting and degraded", map[int]State{1: StateStarting, 2: StateDegraded, 3: StateStopped, 4: StateDegraded}, []int{1, 2}},
		{"stopped", map[int]State{1: StateStopped, 2: StateStopped, 3: StateStopped, 4: StateUp}, nil},
		{"disabled but running", map[int]State{1: StateStopped, 2: StateStopped, 3: StateUp, 4: StateUp}, []int{3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var snap []SlotStatus
			for id := 1; id <= 4; id++ {
				snap = append(snap, SlotStatus{ID: id, State: c.states[id]})
			}
			if got := rotatedSlots(cfg, "sx", snap); !slices.Equal(got, c.want) {
				t.Fatalf("rotatedSlots = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("manager", func(t *testing.T) {
		r := newRotRig(t, rotUserPrefix+config.SessionToken, "pw-fixed", "")
		r.cfg.Slots = append(r.cfg.Slots, rigSlot(3, "sx"))
		r.cfg.Slots[2].Enabled = false
		m, ch := newTestManager(t, r.cfg)
		if err := m.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		waitSlots(t, ch, map[int]State{1: StateUp, 2: StateUp}, 10*time.Second)
		if got := m.RotatedSlots(r.cfg, "sx"); !slices.Equal(got, []int{1}) {
			t.Fatalf("RotatedSlots = %v, want [1]", got)
		}
	})
}
