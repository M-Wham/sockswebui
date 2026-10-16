package api

import (
	"net/http"

	"github.com/M-Wham/sockswebui/internal/config"
)

// verifyAdminPassword re-checks the admin password for a sensitive action (password change,
// config export/import). hash is the stored admin hash the caller read. It returns true only
// on a match. A wrong password writes 400 {field: "current_password", message: "is incorrect"}
// (not 401, so the UI does not mistake it for session expiry); an unusable stored hash writes
// 500; a client that went away while waiting for a hash slot gets nothing. op prefixes logs.
func (s *Server) verifyAdminPassword(w http.ResponseWriter, r *http.Request, op, pw, hash string) bool {
	match, err := verifyPasswordWith(r.Context(), pw, hash, s.freeMem)
	if r.Context().Err() != nil {
		return false
	}
	if err != nil {
		s.log.Error(op+": stored admin hash is unusable", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}
	if !match {
		s.log.Warn(op+": wrong current password", "remote", r.RemoteAddr)
		writeFieldErrors(w, []config.FieldError{{Field: "current_password", Message: "is incorrect"}})
		return false
	}
	return true
}
