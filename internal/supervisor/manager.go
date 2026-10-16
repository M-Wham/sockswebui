package supervisor

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// Manager errors.
var (
	ErrNoSlot = errors.New("supervisor: no such slot")
	ErrClosed = errors.New("supervisor: manager closed")
)

// Options configures a Manager.
type Options struct {
	Config config.Config
	Logger *slog.Logger // process logger; slot logs are redacted before reaching it

	slotOpts []option         // tests only: applied to every slot
	tick     time.Duration    // tests only: throughput tick (default 1 s)
	ticks    <-chan time.Time // tests only: drives throughput ticks instead of a ticker
}

// SlotStatus is a point-in-time view of one slot for the API.
type SlotStatus struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	State    State  `json:"state"`
	Msg      string `json:"msg"`
	Addr     string `json:"addr,omitempty"`
	BytesIn  uint64 `json:"bytes_in"`
	BytesOut uint64 `json:"bytes_out"`
	// Since and Probe feed the API status object (status.go). Since is the zero
	// time and Probe nil while unknown.
	Since time.Time    `json:"-"`
	Probe *ProbeStatus `json:"-"`
}

// Manager owns up to config.MaxSlots slots, each with its own chain (no
// shared netstack). Run autostarts enabled slots,
// Apply restarts slots whose config changed, and all slot events, plus 1 Hz
// throughput snapshots, are fanned out on one Bus.
type Manager struct {
	log      *slog.Logger
	bus      *Bus
	slotOpts []option
	tick     time.Duration
	ticks    <-chan time.Time

	life sync.Mutex // serializes Run, Start, Stop, Apply and Close

	mu     sync.RWMutex
	cfgs   map[int]config.Slot
	slots  map[int]*Slot
	ran    bool
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup // throughput loop
}

// New returns a Manager with every configured slot STOPPED. Nothing runs
// until Run.
func New(opts Options) *Manager {
	m := &Manager{
		log:      opts.Logger,
		bus:      NewBus(),
		slotOpts: opts.slotOpts,
		tick:     opts.tick,
		ticks:    opts.ticks,
		cfgs:     map[int]config.Slot{},
		slots:    map[int]*Slot{},
		stop:     make(chan struct{}),
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.tick <= 0 {
		m.tick = DefaultThroughputTick
	}
	for _, sc := range opts.Config.Slots {
		m.cfgs[sc.ID] = sc
		m.slots[sc.ID] = m.newSlot(sc, opts.Config.Endpoints)
	}
	return m
}

// newSlot builds slot sc; extra options (e.g. withRing) apply after the
// test-only slot options.
func (m *Manager) newSlot(sc config.Slot, all []config.Endpoint, extra ...option) *Slot {
	opts := append(append([]option(nil), m.slotOpts...), extra...)
	return NewSlot(sc, routeEndpoints(sc.Route, all), m.bus, m.log, opts...)
}

// stopLogged stops s, logging (redacted, into the slot's ring) any error.
// The slot is STOPPED either way.
func stopLogged(s *Slot, why string) {
	if err := s.Stop(); err != nil {
		s.log.Error("slot stop failed", "during", why, "err", err)
	}
}

// routeEndpoints returns the endpoints route references, in route order
// without duplicates. Missing IDs are left for egress.Build to reject.
func routeEndpoints(route []string, all []config.Endpoint) []config.Endpoint {
	var out []config.Endpoint
	for _, id := range route {
		if slices.ContainsFunc(out, func(e config.Endpoint) bool { return e.ID == id }) {
			continue
		}
		if i := slices.IndexFunc(all, func(e config.Endpoint) bool { return e.ID == id }); i >= 0 {
			out = append(out, all[i])
		}
	}
	return out
}

// Run starts every enabled slot (boot autostart, ascending id) and the
// throughput loop. Slot failures surface as ERROR events; a slot whose WG
// endpoint is already used by a lower-id running slot goes ERROR and its
// ErrEndpointInUse is included in Run's error.
func (m *Manager) Run() error {
	m.life.Lock()
	defer m.life.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.ran {
		return errors.New("supervisor: manager already running")
	}
	m.ran = true
	m.wg.Add(1)
	go m.throughputLoop(m.stop)
	var errs []error
	for _, id := range m.ids() {
		if m.cfgs[id].Enabled {
			if err := m.startChecked(m.slots[id], true); err != nil && !errors.Is(err, ErrRunning) {
				errs = append(errs, fmt.Errorf("slot %d: %w", id, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Start starts slot id. It fails with ErrEndpointInUse, leaving the slot
// untouched, while another running slot uses one of its WG endpoints.
func (m *Manager) Start(id int) error {
	m.life.Lock()
	defer m.life.Unlock()
	s, err := m.slot(id)
	if err != nil {
		return err
	}
	err = m.startChecked(s, false)
	if errors.Is(err, ErrEndpointInUse) {
		err = fmt.Errorf("slot %d: %w", id, err)
	}
	return err
}

// Stop stops slot id. Idempotent.
func (m *Manager) Stop(id int) error {
	m.life.Lock()
	defer m.life.Unlock()
	s, err := m.slot(id)
	if err != nil {
		return err
	}
	return s.Stop()
}

// Apply installs cfg. A slot whose config (ignoring name and enabled) or
// route endpoints changed is replaced: stopped, rebuilt and, if it was
// running or in ERROR, started again (no hot mutation of a running slot). Unchanged
// slots are untouched and removed slots are stopped. Only with
// cfg.Settings.StartOnEnable does enabled drive running state here: a slot
// whose enabled flips true→false is stopped and rebuilt STOPPED, and an added
// enabled slot starts; false→true alone starts nothing. With it false,
// enabled only decides boot autostart (Run) and the start/stop endpoints are
// the only way to change running state.
func (m *Manager) Apply(cfg config.Config) error {
	if len(cfg.Slots) > config.MaxSlots {
		return fmt.Errorf("supervisor: %d slots, max %d", len(cfg.Slots), config.MaxSlots)
	}
	next := make(map[int]config.Slot, len(cfg.Slots))
	for _, sc := range cfg.Slots {
		if _, dup := next[sc.ID]; dup {
			return fmt.Errorf("supervisor: duplicate slot id %d", sc.ID)
		}
		next[sc.ID] = sc
	}
	m.life.Lock()
	defer m.life.Unlock()
	if m.closed {
		return ErrClosed
	}
	slots := make(map[int]*Slot, len(next))
	var start []*Slot
	for id, sc := range next {
		eps := routeEndpoints(sc.Route, cfg.Endpoints)
		old := m.slots[id]
		// enabled true→false = stop, only with start_on_enable;
		// false→true alone starts nothing.
		soe := cfg.Settings.StartOnEnable
		disable := soe && old != nil && m.cfgs[id].Enabled && !sc.Enabled
		if old != nil && !disable && sameSlot(old, sc, eps) {
			slots[id] = old
			continue
		}
		restart := soe && old == nil && sc.Enabled && m.ran
		var extra []option
		if old != nil {
			st, _ := old.State()
			restart = !disable && (st.running() || st == StateError)
			why := "apply rebuild"
			if disable {
				why = "apply disable"
			}
			stopLogged(old, why) // rebuild proceeds regardless
			extra = append(extra, withRing(old.ring))
		}
		ns := m.newSlot(sc, cfg.Endpoints, extra...)
		slots[id] = ns
		if restart && old != nil {
			logRotations(ns, old.eps) // one rotation line per affected slot
		}
		if restart {
			start = append(start, ns)
		}
	}
	for id, old := range m.slots {
		if _, keep := next[id]; !keep {
			stopLogged(old, "apply remove")
		}
	}
	m.mu.Lock()
	m.cfgs, m.slots = next, slots
	m.mu.Unlock()
	slices.SortFunc(start, func(a, b *Slot) int { return a.cfg.ID - b.cfg.ID }) // lowest id wins a WG conflict
	var errs []error
	for _, s := range start {
		if err := m.startChecked(s, true); err != nil {
			errs = append(errs, fmt.Errorf("slot %d: %w", s.cfg.ID, err))
		}
	}
	return errors.Join(errs...)
}

// sameSlot reports whether s already runs sc with route endpoints eps.
// Name and Enabled do not affect the running slot.
func sameSlot(s *Slot, sc config.Slot, eps []config.Endpoint) bool {
	a, b := s.cfg, sc
	a.Name, b.Name, a.Enabled, b.Enabled = "", "", false, false
	return reflect.DeepEqual(a, b) && reflect.DeepEqual(s.eps, eps)
}

// Snapshot returns every slot's status, ordered by id.
func (m *Manager) Snapshot() []SlotStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SlotStatus, 0, len(m.slots))
	for _, id := range m.ids() {
		s, sc := m.slots[id], m.cfgs[id]
		st, msg := s.State()
		in, outB := s.Bytes()
		d := s.Detail()
		out = append(out, SlotStatus{ID: id, Name: sc.Name, Enabled: sc.Enabled, State: st, Msg: msg,
			Addr: s.Addr(), BytesIn: in, BytesOut: outB, Since: d.Since, Probe: d.Probe})
	}
	return out
}

// Subscribe returns future events (state and throughput) for every slot.
func (m *Manager) Subscribe() (<-chan Event, func()) { return m.bus.Subscribe(DefaultEventBuffer) }

// Logs returns slot id's redacted log lines, oldest first (nil if unknown).
func (m *Manager) Logs(id int) []LogLine {
	m.mu.RLock()
	s := m.slots[id]
	m.mu.RUnlock()
	if s == nil {
		return nil
	}
	return s.Logs()
}

// Close stops every slot and the throughput loop. Idempotent.
func (m *Manager) Close() {
	m.life.Lock()
	defer m.life.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.stop)
	m.wg.Wait()
	for _, s := range m.slots {
		stopLogged(s, "close")
	}
}

func (m *Manager) slot(id int) (*Slot, error) {
	if m.closed {
		return nil, ErrClosed
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s := m.slots[id]; s != nil {
		return s, nil
	}
	return nil, ErrNoSlot
}

// ids returns slot ids in ascending order. Callers hold m.mu or m.life.
func (m *Manager) ids() []int {
	ids := make([]int, 0, len(m.slots))
	for id := range m.slots {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
