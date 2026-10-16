package supervisor

import (
	"errors"
	"strconv"

	"github.com/M-Wham/sockswebui/internal/config"
)

// One WireGuard key holds one live session with its peer, so two running
// slots may not use the same WG endpoint at the same time. A WG endpoint may still appear in several slots'
// routes; starting a slot is refused while another running (STARTING, UP,
// DEGRADED) slot uses one of its WG endpoints. Proxy endpoints are shared
// freely. Every check runs under Manager.life, together with the start it
// guards, so two conflicting starts can never both win.

// ErrEndpointInUse is matched (errors.Is) by every *EndpointInUseError.
var ErrEndpointInUse = errors.New("supervisor: wireguard endpoint in use")

// EndpointInUseError names the running slot that holds a WG endpoint.
type EndpointInUseError struct {
	Slot     int    // the running slot already using Endpoint
	Endpoint string // WG endpoint id
}

func (e *EndpointInUseError) Error() string {
	return "wireguard endpoint " + strconv.Quote(e.Endpoint) + " is in use by running slot " + strconv.Itoa(e.Slot)
}

// Is makes errors.Is(err, ErrEndpointInUse) hold.
func (e *EndpointInUseError) Is(target error) bool { return target == ErrEndpointInUse }

// wgConflict reports the first (lowest id) other running slot sharing a WG
// endpoint with slot id, or nil. Callers hold m.life.
func (m *Manager) wgConflict(id int) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.slots[id]
	if s == nil {
		return nil
	}
	return m.conflictLocked(id, s.eps)
}

// CheckStart reports, without starting anything, whether slot sc (with its
// route endpoints resolved from eps) could start now: an *EndpointInUseError
// when another running slot (id != sc.ID) uses one of its WG endpoints, nil
// otherwise. It is a read-only pre-check for the API; the authoritative
// check is startChecked, under m.life with the start it guards.
func (m *Manager) CheckStart(sc config.Slot, eps []config.Endpoint) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conflictLocked(sc.ID, routeEndpoints(sc.Route, eps))
}

// conflictLocked reports the first (lowest id) running slot other than id
// whose route uses a WG endpoint in eps. Callers hold m.mu.
func (m *Manager) conflictLocked(id int, eps []config.Endpoint) error {
	mine := wgEndpointIDs(eps)
	if len(mine) == 0 {
		return nil
	}
	for _, oid := range m.ids() {
		if oid == id {
			continue
		}
		o := m.slots[oid]
		if st, _ := o.State(); !st.running() {
			continue
		}
		for _, e := range o.eps { // route order: deterministic
			if mine[e.ID] && e.Kind == config.KindWireGuard {
				return &EndpointInUseError{Slot: oid, Endpoint: e.ID}
			}
		}
	}
	return nil
}

// wgEndpointIDs returns the ids of the WireGuard endpoints in eps.
func wgEndpointIDs(eps []config.Endpoint) map[string]bool {
	out := map[string]bool{}
	for _, e := range eps {
		if e.Kind == config.KindWireGuard {
			out[e.ID] = true
		}
	}
	return out
}

// startChecked starts s unless another running slot holds one of its WG
// endpoints. An explicit start (refuse=false) leaves s untouched; an
// automatic start (boot autostart, Apply) moves s to ERROR with the reason.
// Callers hold m.life.
func (m *Manager) startChecked(s *Slot, refuse bool) error {
	if err := m.wgConflict(s.cfg.ID); err != nil {
		if refuse {
			s.refuse(err.Error())
		}
		return err
	}
	return s.Start()
}

// refuse moves a non-running slot to ERROR with msg without starting it.
func (s *Slot) refuse(msg string) {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	running := s.state.running()
	s.mu.Unlock()
	if running {
		return
	}
	if err := s.teardown(); err != nil { // leftovers from an earlier ERROR
		s.log.Warn("teardown before refuse failed", "err", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log.Warn("start refused", "reason", "endpoint_in_use", "err", msg)
	s.setLocked(StateError, s.redact(msg))
}
