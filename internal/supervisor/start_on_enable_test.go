package supervisor

import (
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// soeCfg is conflictCfg with settings.start_on_enable set to on.
func soeCfg(on bool, routes map[int][]string, enabled ...int) config.Config {
	cfg := conflictCfg(routes, enabled...)
	cfg.Settings.StartOnEnable = on
	return cfg
}

// TestApplyStartOnEnable: with start_on_enable=false, Apply never changes a
// slot's running state through enabled (start/stop endpoints are
// the only way); with true, enabled true→false stops and an added enabled
// slot starts (unchanged behaviour).
func TestApplyStartOnEnable(t *testing.T) {
	one := map[int][]string{1: {"wgA"}}
	two := map[int][]string{1: {"wgA"}, 2: {"wgB"}}
	for _, on := range []bool{false, true} {
		name := map[bool]string{false: "off", true: "on"}[on]

		t.Run(name+"/disable", func(t *testing.T) {
			f := newFakeNet()
			m, ev := newTestManager(t, soeCfg(on, one, 1), f.opts()...)
			if err := m.Run(); err != nil {
				t.Fatal(err)
			}
			waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
			if err := m.Apply(soeCfg(on, one)); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			want := StateUp
			if on {
				want = StateStopped
				waitSlots(t, ev, map[int]State{1: StateStopped}, 5*time.Second)
			} else if seen := drainStates(ev, 1, 200*time.Millisecond); len(seen) != 0 {
				t.Fatalf("disable with start_on_enable=false changed slot 1: %v", seen)
			}
			if got := status(t, m, 1); got.State != want || got.Enabled {
				t.Fatalf("slot 1 = %+v, want %s, disabled", got, want)
			}
		})

		t.Run(name+"/add enabled", func(t *testing.T) {
			f := newFakeNet()
			m, ev := newTestManager(t, soeCfg(on, one, 1), f.opts()...)
			if err := m.Run(); err != nil {
				t.Fatal(err)
			}
			waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
			if err := m.Apply(soeCfg(on, two, 1, 2)); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			want := StateStopped
			if on {
				want = StateUp
				waitSlots(t, ev, map[int]State{2: StateUp}, 5*time.Second)
			} else if seen := drainStates(ev, 2, 200*time.Millisecond); len(seen) != 0 {
				t.Fatalf("added slot 2 with start_on_enable=false changed: %v", seen)
			}
			if got := status(t, m, 2); got.State != want || !got.Enabled {
				t.Fatalf("slot 2 = %+v, want %s, enabled", got, want)
			}
			if got := status(t, m, 1); got.State != StateUp {
				t.Fatalf("untouched slot 1 = %+v, want UP", got)
			}
		})

		// Boot autostart follows enabled whatever the setting.
		t.Run(name+"/boot autostart", func(t *testing.T) {
			f := newFakeNet()
			m, ev := newTestManager(t, soeCfg(on, two, 1), f.opts()...)
			if err := m.Run(); err != nil {
				t.Fatal(err)
			}
			waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
			if got := status(t, m, 2); got.State != StateStopped {
				t.Fatalf("disabled slot 2 = %+v, want STOPPED", got)
			}
		})
	}
}

// TestApplyStartOnEnableOffRebuildKeepsRunning: with start_on_enable=false a
// config change to a running slot that is also disabled still rebuilds and
// restarts it (no hot mutation), and leaves a STOPPED one stopped.
func TestApplyStartOnEnableOffRebuildKeepsRunning(t *testing.T) {
	f := newFakeNet()
	m, ev := newTestManager(t, soeCfg(false, map[int][]string{1: {"wgA"}}, 1), f.opts()...)
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
	if err := m.Apply(soeCfg(false, map[int][]string{1: {"wgB"}})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitSlots(t, ev, map[int]State{1: StateUp}, 5*time.Second)
	if n := f.count("wgB"); n < 1 {
		t.Fatalf("wgB builds = %d, want a rebuild on wgB", n)
	}
	if got := status(t, m, 1); got.State != StateUp || got.Enabled {
		t.Fatalf("slot 1 = %+v, want UP, disabled", got)
	}
}
