package api

import (
	"errors"
	"net/http"
	"reflect"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// importConfigResponse is the 200 body of POST /api/config/import.
type importConfigResponse struct {
	Endpoints      int                  `json:"endpoints"`
	Slots          int                  `json:"slots"`
	SecretsMissing importSecretsMissing `json:"secrets_missing"`
	Backup         string               `json:"backup"`
}

// importSecretsMissing lists the endpoints and slots whose own secrets_omitted list is
// non-empty, i.e. the editors where secrets must be re-entered.
type importSecretsMissing struct {
	Endpoints []string `json:"endpoints"`
	Slots     []int    `json:"slots"`
}

// replaceImported replaces all endpoints and slots with payload (steps 4-5, backup,
// apply). The candidate is built inside the store lock from the current Settings, so the
// device-owned fields (admin hash, session secret, UI address) can never be overwritten by
// a stale copy. Race guard: if the admin hash differs from the one the password was
// verified against, a concurrent password change won and the import fails with 409.
// Holds startMu throughout, so no slot start interleaves (see handleSlotStart).
// Running slots are stopped after the backup is written and before the replace, so a
// validation or backup failure leaves them untouched; nothing is started. If the commit
// itself fails, nothing changed, so exactly the slots stopped here are
// restarted on the unchanged config (restartStopped). A successful import ends every admin
// session except caller's; the session secret is kept, so the
// caller's CSRF token stays valid.
func (s *Server) replaceImported(w http.ResponseWriter, r *http.Request, payload config.ExportPayload, mode, verifiedHash, caller string) {
	replace := s.replaceConfig
	if replace == nil {
		replace = s.store.ReplaceWithBackupFunc
	}
	// startMu keeps a slot start from landing between the replace and the apply.
	s.startMu.Lock()
	defer s.startMu.Unlock()
	var pre config.Config // the config the build saw, under the store lock
	var stopped []int     // slots stopped right before the commit
	backup, err := replace(func(cur config.Config) (config.Config, error) {
		pre = cur
		if cur.Settings.AdminPasswordHash != verifiedHash {
			return config.Config{}, errPasswordRaced
		}
		return config.Config{Endpoints: payload.Endpoints, Slots: payload.Slots, Settings: cur.Settings}, nil
	}, func() { stopped = s.stopAllSlots() })
	if err != nil {
		s.restartStopped(stopped, pre)
	}
	switch {
	case errors.Is(err, errPasswordRaced):
		writeError(w, http.StatusConflict, "password changed concurrently; retry")
		return
	case err != nil:
		s.writeStoreError(w, "config import", err, "")
		return
	}
	s.sessions.deleteAllExcept(caller)
	s.applyConfig("config import")
	s.log.Info("config imported", "endpoints", len(payload.Endpoints), "slots", len(payload.Slots), "mode", mode, "remote", clientKey(r))
	writeJSON(w, http.StatusOK, importConfigResponse{
		Endpoints:      len(payload.Endpoints),
		Slots:          len(payload.Slots),
		SecretsMissing: secretsMissing(payload),
		Backup:         backup,
	})
}

// stopAllSlots stops every slot the controller does not report STOPPED and returns their
// ids. It runs under the store lock (it must not touch the store). A failed stop is logged
// and ignored: the slot is STOPPED either way and the saved config has it disabled.
func (s *Server) stopAllSlots() []int {
	var ids []int
	for _, st := range s.ctrl.Snapshot() {
		if st.State == supervisor.StateStopped {
			continue
		}
		ids = append(ids, st.ID)
		if err := s.ctrl.Stop(st.ID); err != nil {
			s.log.Warn("config import: stop slot", "id", st.ID, "err", err)
		}
	}
	return ids
}

// restartStopped restarts ids after a failed import commit, through the normal controller
// Start (kill switch and WG-conflict rules apply). Fail closed: it restarts only
// if the store still holds exactly pre, the config those slots ran on; otherwise they stay
// STOPPED. Caller holds startMu, so no slot start interleaves. A failed start is logged.
func (s *Server) restartStopped(ids []int, pre config.Config) {
	if len(ids) == 0 {
		return
	}
	if !reflect.DeepEqual(s.store.Get(), pre) {
		s.log.Warn("config import: config changed after failed import; slots left stopped", "ids", ids)
		return
	}
	for _, id := range ids {
		if err := s.ctrl.Start(id); err != nil && !errors.Is(err, supervisor.ErrRunning) {
			s.log.Warn("config import: restart slot", "id", id, "err", err)
		}
	}
}

func secretsMissing(p config.ExportPayload) importSecretsMissing {
	out := importSecretsMissing{Endpoints: []string{}, Slots: []int{}}
	for _, e := range p.Endpoints {
		if len(e.SecretsOmitted) > 0 {
			out.Endpoints = append(out.Endpoints, e.ID)
		}
	}
	for _, sl := range p.Slots {
		if len(sl.SecretsOmitted) > 0 {
			out.Slots = append(out.Slots, sl.ID)
		}
	}
	return out
}
