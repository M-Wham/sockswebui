package supervisor

import (
	"slices"

	"github.com/M-Wham/sockswebui/internal/config"
)

// Session rotation (a new upstream proxy session ID). A rotate is an ordinary Apply with a new
// current_id on one endpoint: sameSlot deep-compares the route endpoints, so
// every slot using it is rebuilt and the new chain renders the new ID at dial
// time (egress.RenderCredentials). Nothing here generates IDs: backoff
// rebuilds and restarts reuse the slot's endpoints as they are, so rotation
// never happens implicitly.

// RotatedSlots returns, ascending, the ids of slots in cfg whose route
// contains endpointID and whose current state is STARTING, UP, DEGRADED or
// ERROR: the slots an Apply rotating that endpoint rebuilds and restarts
// (the API's rebuilt_slots). Call it before Apply.
func (m *Manager) RotatedSlots(cfg config.Config, endpointID string) []int {
	return rotatedSlots(cfg, endpointID, m.Snapshot())
}

// rotatedSlots is RotatedSlots over a pre-apply snapshot.
func rotatedSlots(cfg config.Config, endpointID string, snap []SlotStatus) []int {
	state := make(map[int]State, len(snap))
	for _, st := range snap {
		state[st.ID] = st.State
	}
	var out []int
	for _, sc := range cfg.Slots {
		st, ok := state[sc.ID]
		if !ok || !slices.Contains(sc.Route, endpointID) {
			continue
		}
		if st.running() || st == StateError {
			out = append(out, sc.ID)
		}
	}
	slices.Sort(out)
	return out
}

// rotatedEndpoints returns the endpoints of next whose session ID changed
// from a previous non-empty ID in prev. A first ID (template introduced) is
// not a rotation.
func rotatedEndpoints(prev, next []config.Endpoint) []config.Endpoint {
	var out []config.Endpoint
	for _, e := range next {
		i := slices.IndexFunc(prev, func(p config.Endpoint) bool { return p.ID == e.ID })
		if i < 0 {
			continue
		}
		old := prev[i].Session.CurrentID
		if old != "" && e.Session.CurrentID != "" && old != e.Session.CurrentID {
			out = append(out, e)
		}
	}
	return out
}

// logRotations writes one line per rotated endpoint into the rebuilt slot's
// log. current_id and the endpoint name are not secrets.
func logRotations(s *Slot, prev []config.Endpoint) {
	for _, e := range rotatedEndpoints(prev, s.eps) {
		s.log.Info("session rotated endpoint=" + e.Name + " id=" + e.Session.CurrentID)
	}
}
