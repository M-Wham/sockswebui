package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

var errSlotNotFound = errors.New("slot not configured")

// handleSlotStart starts a configured slot and persists enabled=true (desired
// running). The slot comes up asynchronously: 202 with its status.
func (s *Server) handleSlotStart(w http.ResponseWriter, r *http.Request) {
	id, ok := slotID(w, r)
	if !ok {
		return
	}
	// Unlocked pre-check: a fast 404/409 before touching the controller.
	if _, ok := s.startableSlot(w, id); !ok {
		return
	}
	if s.startChecked != nil {
		s.startChecked()
	}
	// Authoritative re-check under startMu, which config import holds across its replace
	// and apply, so an import cannot land between this check and ctrl.Start.
	s.startMu.Lock()
	defer s.startMu.Unlock()
	sl, ok := s.startableSlot(w, id)
	if !ok {
		return
	}
	if s.startRechecked != nil {
		s.startRechecked()
	}
	err := s.ctrl.Start(id)
	switch {
	case err == nil, errors.Is(err, supervisor.ErrRunning):
	case writeEndpointInUse(w, sl, err):
		return
	case errors.Is(err, supervisor.ErrNoSlot):
		writeError(w, http.StatusNotFound, "slot not found")
		return
	default:
		s.log.Error("start slot", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !s.setEnabled(w, id, true) {
		return
	}
	s.log.Info("slot start requested", "id", id)
	writeJSON(w, http.StatusAccepted, s.slotStatus(id))
}

// startableSlot returns saved slot id, or writes 404 if it is not configured or 409
// while its secrets are missing. One store snapshot covers both checks.
func (s *Server) startableSlot(w http.ResponseWriter, id int) (config.Slot, bool) {
	cfg := s.store.Get()
	i := slices.IndexFunc(cfg.Slots, func(c config.Slot) bool { return c.ID == id })
	if i < 0 {
		writeError(w, http.StatusNotFound, "slot not configured")
		return config.Slot{}, false
	}
	if config.SlotSecretsMissing(cfg, cfg.Slots[i]) {
		writeError(w, http.StatusConflict, config.SecretsMissingMessage)
		return config.Slot{}, false
	}
	return cfg.Slots[i], true
}

// writeEndpointInUse writes the 409 for a WG endpoint held by another running
// slot and reports whether err was one.
func writeEndpointInUse(w http.ResponseWriter, sl config.Slot, err error) bool {
	if !errors.Is(err, supervisor.ErrEndpointInUse) {
		return false
	}
	fe := config.FieldError{Field: "route", Message: "a wireguard endpoint is in use by another running slot"}
	var inUse *supervisor.EndpointInUseError
	if errors.As(err, &inUse) {
		fe = config.FieldError{Field: routeField(sl, inUse.Endpoint), Message: fmt.Sprintf(
			"wireguard endpoint %q is in use by running slot %d", inUse.Endpoint, inUse.Slot)}
	}
	writeJSON(w, http.StatusConflict, errorBody{Error: "wireguard endpoint in use", Errors: []config.FieldError{fe}})
	return true
}

// handleSlotStop stops a configured slot and persists enabled=false.
func (s *Server) handleSlotStop(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.configuredSlot(w, r)
	if !ok {
		return
	}
	// startMu serialises stop with start, slot saves and import (config and run state).
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if err := s.ctrl.Stop(id); err != nil {
		if errors.Is(err, supervisor.ErrNoSlot) {
			writeError(w, http.StatusNotFound, "slot not found")
			return
		}
		// The slot is STOPPED even when closing its chain failed.
		s.log.Warn("stop slot", "id", id, "err", err)
	}
	if !s.setEnabled(w, id, false) {
		return
	}
	s.log.Info("slot stopped", "id", id)
	writeJSON(w, http.StatusOK, s.slotStatus(id))
}

// configuredSlot resolves {id} to a saved slot; anything else is a 404.
func (s *Server) configuredSlot(w http.ResponseWriter, r *http.Request) (int, config.Slot, bool) {
	id, ok := slotID(w, r)
	if !ok {
		return 0, config.Slot{}, false
	}
	i := slices.IndexFunc(s.store.Get().Slots, func(sl config.Slot) bool { return sl.ID == id })
	if i < 0 {
		writeError(w, http.StatusNotFound, "slot not configured")
		return 0, config.Slot{}, false
	}
	return id, s.store.Get().Slots[i], true
}

// setEnabled persists the slot's desired state and syncs the controller.
func (s *Server) setEnabled(w http.ResponseWriter, id int, on bool) bool {
	err := s.store.Update(func(c *config.Config) error {
		i := slices.IndexFunc(c.Slots, func(sl config.Slot) bool { return sl.ID == id })
		if i < 0 {
			return errSlotNotFound
		}
		c.Slots[i].Enabled = on
		return nil
	})
	switch {
	case errors.Is(err, errSlotNotFound):
		writeError(w, http.StatusNotFound, "slot not configured")
		return false
	case err != nil:
		s.log.Error("persist slot enabled", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}
	s.applyConfig("set enabled")
	return true
}

// applyConfig hands the saved config to the controller, which rebuilds only
// the slots whose config changed. Failures surface as slot ERROR events.
func (s *Server) applyConfig(op string) {
	if err := s.ctrl.Apply(s.store.Get()); err != nil {
		s.log.Warn(op+": apply config", "err", err)
	}
}

// slotStatus returns slot id's live status, STOPPED if the controller lacks it.
func (s *Server) slotStatus(id int) supervisor.SlotStatus {
	red := newSecretRedactor(s.store.Get())
	for _, st := range s.ctrl.Snapshot() {
		if st.ID == id {
			st.Msg = red.Replace(st.Msg)
			return st
		}
	}
	return supervisor.SlotStatus{ID: id, State: supervisor.StateStopped}
}

// routeField names the route position of endpoint epID in sl.
func routeField(sl config.Slot, epID string) string {
	if i := slices.Index(sl.Route, epID); i >= 0 {
		return fmt.Sprintf("route[%d]", i)
	}
	return "route"
}
