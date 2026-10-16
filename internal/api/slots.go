package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// Defaults shown for a slot id that has never been saved. Loopback keeps an unconfigured
// slot private if it is saved as-is.
const (
	defaultSlotBind     = "127.0.0.1"
	defaultSlotBasePort = 1080
)

// registerSlots wires the slot routes.
func (s *Server) registerSlots() {
	s.mux.HandleFunc("GET /api/slots", s.handleListSlots)
	s.mux.HandleFunc("GET /api/slots/{id}", s.handleGetSlot)
	s.mux.HandleFunc("PUT /api/slots/{id}", s.handleUpdateSlot)
	s.mux.HandleFunc("POST /api/slots/{id}/start", s.handleSlotStart)
	s.mux.HandleFunc("POST /api/slots/{id}/stop", s.handleSlotStop)
	s.mux.HandleFunc("GET /api/slots/{id}/logs", s.handleSlotLogs)
}

func (s *Server) handleListSlots(w http.ResponseWriter, _ *http.Request) {
	statuses := s.slotStatuses() // snapshot before config
	cfg := s.store.Get()
	out := slotList{Slots: make([]slotResponse, 0, config.MaxSlots)}
	for id := 1; id <= config.MaxSlots; id++ {
		v := slotView(cfg.Slots, id)
		v.Status = statuses(id)
		out.Slots = append(out.Slots, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSlot(w http.ResponseWriter, r *http.Request) {
	id, ok := slotID(w, r)
	if !ok {
		return
	}
	status := s.slotStatuses()(id) // snapshot before config
	v := slotView(s.store.Get().Slots, id)
	v.Status = status
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleUpdateSlot(w http.ResponseWriter, r *http.Request) {
	id, ok := slotID(w, r)
	if !ok {
		return
	}
	var req slotRequest
	if !decodeJSON(w, r, &req, "slot") {
		return
	}
	// startMu (then the store lock) spans validate..persist..apply, so a start cannot run
	// ctrl.Start between its re-check and this update (fail closed).
	s.startMu.Lock()
	defer s.startMu.Unlock()
	var updated config.Slot
	var prefix string
	var wasEnabled, startOnEnable bool
	err := s.store.Update(func(c *config.Config) error {
		var prev *config.Slot
		others := make([]config.Slot, 0, len(c.Slots))
		for i := range c.Slots {
			if c.Slots[i].ID == id {
				prev = &c.Slots[i]
			} else {
				others = append(others, c.Slots[i])
			}
		}
		updated = req.toSlot(id, prev)
		wasEnabled = prev != nil && prev.Enabled
		startOnEnable = c.Settings.StartOnEnable
		// Validate with the target slot last so cross-slot clashes are reported on it.
		c.Slots = append(others, updated)
		prefix = fmt.Sprintf("slots[%d]", len(others))
		if ve := socksAuthErrors(prefix, updated); ve != nil {
			return ve
		}
		if err := config.Validate(*c); err != nil {
			return err
		}
		// Enabling must not take a WG endpoint another running slot holds; refuse
		// before persisting. A race with Apply is caught again under the manager lock.
		if updated.Enabled {
			if err := s.ctrl.CheckStart(updated, c.Endpoints); err != nil {
				return err
			}
		}
		slices.SortFunc(c.Slots, func(a, b config.Slot) int { return a.ID - b.ID })
		return nil
	})
	if writeEndpointInUse(w, updated, err) {
		return
	}
	if err != nil {
		s.writeStoreError(w, "update slot", err, prefix)
		return
	}
	s.log.Info("slot updated", "id", id, "enabled", updated.Enabled, "hops", len(updated.Route), "remote", clientKey(r))
	s.applyConfig("update slot") // a running slot is stopped, rebuilt and started
	if startOnEnable && updated.Enabled != wasEnabled && s.syncRunState(w, id, updated) {
		return
	}
	resp := toSlotResponse(updated, true)
	resp.Status = s.slotStatuses()(id)
	writeJSON(w, http.StatusOK, resp)
}

// syncRunState starts or stops slot id after a save that changed enabled, with
// settings.start_on_enable true. Caller holds startMu. The config is already
// saved and applied. It returns true after writing a 409 for an endpoint taken since
// the pre-check; that case first rolls enabled back to false, so the saved config matches
// the error. Other start failures are logged at error level. The slot state stays as it
// is, unless Start itself moved it to ERROR; the saved config is kept.
func (s *Server) syncRunState(w http.ResponseWriter, id int, sl config.Slot) bool {
	if !sl.Enabled {
		if err := s.ctrl.Stop(id); err != nil && !errors.Is(err, supervisor.ErrNoSlot) {
			s.log.Warn("stop slot after disable", "id", id, "err", err)
		}
		return false
	}
	err := s.ctrl.Start(id)
	switch {
	case err == nil, errors.Is(err, supervisor.ErrRunning):
		return false
	case errors.Is(err, supervisor.ErrEndpointInUse):
		s.rollbackEnabled(id)
		return writeEndpointInUse(w, sl, err)
	default:
		s.log.Error("start slot after enable", "id", id, "err", err)
		return false
	}
}

// rollbackEnabled saves enabled=false for slot id and applies it. Caller holds startMu.
func (s *Server) rollbackEnabled(id int) {
	err := s.store.Update(func(c *config.Config) error {
		if i := slices.IndexFunc(c.Slots, func(sl config.Slot) bool { return sl.ID == id }); i >= 0 {
			c.Slots[i].Enabled = false
		}
		return nil
	})
	if err != nil {
		s.log.Error("roll back enabled", "id", id, "err", err)
		return
	}
	s.applyConfig("roll back enabled")
}

// socksAuthErrors requires a password when SOCKS auth is enabled: an empty password
// would make the listener accept any client that knows the username. A password listed in
// SecretsOmitted may stay empty: config.Validate then keeps the slot from being enabled.
func socksAuthErrors(prefix string, sl config.Slot) config.ValidationErrors {
	if sl.SocksAuth == nil || sl.SocksAuth.Password != "" ||
		slices.Contains(sl.SecretsOmitted, config.OmittedSocksPassword) {
		return nil
	}
	return config.ValidationErrors{{Field: prefix + ".socks_auth.password",
		Message: "is required when SOCKS auth is enabled"}}
}

// slotID parses the {id} path value; ids outside 1..MaxSlots get a 404.
func slotID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 || id > config.MaxSlots {
		writeError(w, http.StatusNotFound, "slot not found")
		return 0, false
	}
	return id, true
}

// slotView returns the stored slot id, or its defaults when it has never been saved.
func slotView(slots []config.Slot, id int) slotResponse {
	for _, sl := range slots {
		if sl.ID == id {
			return toSlotResponse(sl, true)
		}
	}
	return toSlotResponse(defaultSlot(id), false)
}
