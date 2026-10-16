package supervisor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
)

// TestThroughputSkipsStopped: 1 Hz throughput emissions carry entries only
// for running slots, never for idle STOPPED ones.
func TestThroughputSkipsStopped(t *testing.T) {
	r := newManagerRig(t)
	cfg := config.Config{Endpoints: r.eps}
	for id, route := range map[int]string{1: "wgA", 2: "wgB", 3: "socksD"} {
		sc := rigSlot(id, route)
		sc.Enabled = id == 1
		cfg.Slots = append(cfg.Slots, sc)
	}
	m, ev := newTestManager(t, cfg)
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)
	up := 0
	deadline := time.After(800 * time.Millisecond) // ~8 ticks at 100 ms
	for {
		select {
		case e := <-ev:
			if e.Type != EventThroughput {
				continue
			}
			if e.Slot != 1 {
				t.Fatalf("throughput event for STOPPED slot %d: %+v", e.Slot, e.Throughput)
			}
			up++
		case <-deadline:
			if up == 0 {
				t.Fatal("no throughput events for UP slot 1")
			}
			return
		}
	}
}

// closeErrChain wraps a real chain; Close reports an error carrying secret.
type closeErrChain struct {
	egress.Chain
	secret string
}

func (c *closeErrChain) Close() error {
	_ = c.Chain.Close()
	return errors.New("stub close failure, auth " + c.secret)
}

// TestManagerApplyStopError: a failing old.Stop() during Apply is logged,
// redacted, to the slot's log ring, and the rebuild still proceeds.
func TestManagerApplyStopError(t *testing.T) {
	r := newManagerRig(t)
	const secret = "pd-secret" // socksD's password
	build := func(route []string, eps []config.Endpoint, _ bool) (egress.Chain, error) {
		ch, err := egress.Build(route, eps)
		if err != nil {
			return nil, err
		}
		return &closeErrChain{Chain: ch, secret: secret}, nil
	}
	log, procLog := newLogger()
	m := New(Options{Config: config.Config{Endpoints: r.eps, Slots: []config.Slot{rigSlot(1, "socksD")}},
		Logger: log, slotOpts: []option{withBuilder(build)}, tick: 100 * time.Millisecond})
	ev, cancel := m.bus.Subscribe(1024)
	t.Cleanup(func() { m.Close(); cancel() })
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)

	if err := m.Apply(config.Config{Endpoints: r.eps, Slots: []config.Slot{rigSlot(1, "wgB")}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, waitUp)
	if got := socksEcho(t, status(t, m, 1).Addr); got["hop"] != "wgB" {
		t.Fatalf("slot 1 after apply echo = %v, want hop=wgB", got)
	}
	var found bool
	all := procLog.String()
	for _, l := range m.Logs(1) {
		all += "\n" + l.Text
		if strings.Contains(l.Text, "stub close failure") && strings.Contains(l.Text, redactedMark) {
			found = true
		}
	}
	if !found {
		t.Fatalf("stop error not in slot 1 log ring (redacted): %s", all)
	}
	if strings.Contains(all, secret) {
		t.Fatalf("secret leaked in logs: %s", all)
	}
}
