package api

import (
	"net/http"

	"github.com/M-Wham/sockswebui/internal/config"
)

// registerSettings wires GET/PUT /api/settings. There is deliberately no way
// to read or write the admin password hash or session secret here.
func (s *Server) registerSettings() {
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handleUpdateSettings)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsView(s.store.Get().Settings))
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if !decodeJSON(w, r, &req, "settings") {
		return
	}
	if req.CurrentPassword != nil || req.NewPassword != nil {
		s.changePassword(w, r, req)
		return
	}
	var updated config.Settings
	err := s.store.Update(func(c *config.Config) error {
		c.Settings.UIBindAddr = req.UIBindAddr
		if req.StartOnEnable != nil {
			c.Settings.StartOnEnable = *req.StartOnEnable
		}
		updated = c.Settings
		return nil
	})
	if err != nil {
		s.writeStoreError(w, "update settings", err, "settings")
		return
	}
	s.log.Info("settings updated", "ui_bind_addr", updated.UIBindAddr, "start_on_enable", updated.StartOnEnable)
	writeJSON(w, http.StatusOK, s.settingsView(updated))
}

// settingsView copies only non-secret settings; config_error surfaces the corrupt-config banner.
func (s *Server) settingsView(st config.Settings) settingsResponse {
	return settingsResponse{UIBindAddr: st.UIBindAddr, StartOnEnable: st.StartOnEnable, HealthDefaults: config.DefaultHealth(),
		ConfigError: s.store.LoadError(), UIBindEffective: s.uiBindAddr, UIBindSource: s.uiBindSource,
		AppVersion: s.appVersion}
}
