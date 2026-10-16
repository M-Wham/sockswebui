package api

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// fakeCtrl is an in-memory SlotController that records calls.
type fakeCtrl struct {
	bus *supervisor.Bus

	mu       sync.Mutex
	starts   []int
	stops    []int
	applies  []config.Config
	startErr error
	applyErr error
	checkErr error
	checks   []config.Slot
	states   map[int]supervisor.State
	// msgs, probes and since feed Snapshot (status object).
	msgs   map[int]string
	probes map[int]*supervisor.ProbeStatus
	since  map[int]time.Time
	logs   map[int][]supervisor.LogLine
	// seq records Start/Stop/Apply in call order ("start 1", "stop 2", "apply").
	seq []string
}

func newFakeCtrl() *fakeCtrl {
	return &fakeCtrl{bus: supervisor.NewBus(), states: map[int]supervisor.State{}, msgs: map[int]string{},
		probes: map[int]*supervisor.ProbeStatus{}, since: map[int]time.Time{}, logs: map[int][]supervisor.LogLine{}}
}

func (f *fakeCtrl) Start(id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, id)
	f.seq = append(f.seq, fmt.Sprintf("start %d", id))
	if f.startErr != nil {
		return f.startErr
	}
	f.states[id] = supervisor.StateStarting
	return nil
}

func (f *fakeCtrl) Stop(id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, id)
	f.seq = append(f.seq, fmt.Sprintf("stop %d", id))
	f.states[id] = supervisor.StateStopped
	return nil
}

// CheckStart records the slot and returns checkErr.
func (f *fakeCtrl) CheckStart(sc config.Slot, _ []config.Endpoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, sc)
	return f.checkErr
}

func (f *fakeCtrl) Apply(cfg config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, cfg)
	f.seq = append(f.seq, "apply")
	return f.applyErr
}

func (f *fakeCtrl) Snapshot() []supervisor.SlotStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []supervisor.SlotStatus
	for id := 1; id <= config.MaxSlots; id++ {
		if st, ok := f.states[id]; ok {
			msg, ok := f.msgs[id]
			if !ok {
				msg = "fake " + string(st)
			}
			out = append(out, supervisor.SlotStatus{ID: id, State: st, Msg: msg, Since: f.since[id], Probe: f.probes[id]})
		}
	}
	return out
}

func (f *fakeCtrl) Subscribe() (<-chan supervisor.Event, func()) { return f.bus.Subscribe(8) }

func (f *fakeCtrl) Logs(id int) []supervisor.LogLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs[id]
}

// RotatedSlots mirrors the supervisor rule over the fake's recorded states.
func (f *fakeCtrl) RotatedSlots(cfg config.Config, endpointID string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, sc := range cfg.Slots {
		if !slices.Contains(sc.Route, endpointID) {
			continue
		}
		switch f.states[sc.ID] {
		case supervisor.StateStarting, supervisor.StateUp, supervisor.StateDegraded, supervisor.StateError:
			out = append(out, sc.ID)
		}
	}
	slices.Sort(out)
	return out
}

func (f *fakeCtrl) calls() (starts, stops []int, applies int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.starts...), append([]int(nil), f.stops...), len(f.applies)
}

// callSeq returns a copy of the Start/Stop/Apply call order.
func (f *fakeCtrl) callSeq() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seq...)
}

// setState sets a slot's fake state (tests simulate running slots).
func (f *fakeCtrl) setState(id int, st supervisor.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = st
}

// setStatus sets a slot's fake state, message, last probe and state-change time.
func (f *fakeCtrl) setStatus(id int, st supervisor.State, msg string, since time.Time, p *supervisor.ProbeStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id], f.msgs[id], f.since[id], f.probes[id] = st, msg, since, p
}
