package supervisor

import "time"

// DefaultThroughputTick is how often throughput snapshots are emitted (1 Hz).
const DefaultThroughputTick = time.Second

// Throughput is one per-slot byte-counter snapshot. BytesIn is
// what the slot received from its egress (toward the SOCKS client), BytesOut
// what it sent into it. Rates are bytes per second over the last tick.
type Throughput struct {
	BytesIn  uint64  `json:"bytes_in"`
	BytesOut uint64  `json:"bytes_out"`
	RateIn   float64 `json:"rate_in"`
	RateOut  float64 `json:"rate_out"`
}

// Read counts bytes received from the egress side.
func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.s.rx.Add(uint64(n))
	return n, err
}

// Write counts bytes sent into the egress side.
func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.s.tx.Add(uint64(n))
	return n, err
}

// Bytes returns the slot's cumulative relayed byte counters.
func (s *Slot) Bytes() (in, out uint64) { return s.rx.Load(), s.tx.Load() }

type tpSample struct{ in, out uint64 }

// throughputLoop emits a throughput event per running slot every tick (or, in
// tests, every value on m.ticks), but only while the bus has subscribers. It
// returns when stop is closed.
func (m *Manager) throughputLoop(stop <-chan struct{}) {
	defer m.wg.Done()
	ticks := m.ticks
	if ticks == nil {
		t := time.NewTicker(m.tick)
		defer t.Stop()
		ticks = t.C
	}
	prev := map[*Slot]tpSample{}
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-ticks:
			prev = m.emitThroughput(now, now.Sub(last), prev)
			last = now
		}
	}
}

// emitThroughput samples every slot and publishes rates against prev for
// running slots only: idle STOPPED/ERROR slots emit nothing. It returns the
// new samples (slots no longer managed drop out).
func (m *Manager) emitThroughput(now time.Time, elapsed time.Duration, prev map[*Slot]tpSample) map[*Slot]tpSample {
	m.mu.RLock()
	slots := make(map[int]*Slot, len(m.slots))
	for id, s := range m.slots {
		slots[id] = s
	}
	m.mu.RUnlock()
	publish := m.bus.Subscribers() > 0
	secs := elapsed.Seconds()
	next := make(map[*Slot]tpSample, len(slots))
	for id, s := range slots {
		in, out := s.Bytes()
		cur := tpSample{in, out}
		next[s] = cur
		if !publish || secs <= 0 {
			continue
		}
		if st, _ := s.State(); !st.running() {
			continue
		}
		p := prev[s] // zero for a new slot: its counters started at zero
		m.bus.Publish(Event{Type: EventThroughput, Slot: id, TS: now, Throughput: &Throughput{
			BytesIn: in, BytesOut: out,
			RateIn:  float64(in-p.in) / secs,
			RateOut: float64(out-p.out) / secs,
		}})
	}
	return next
}
