package api

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/M-Wham/sockswebui/internal/config"
)

var errPasswordRaced = errors.New("admin password changed concurrently")

// changePasswordErrors validates the shape of a change request before any argon2 work.
func changePasswordErrors(req settingsRequest) []config.FieldError {
	var errs []config.FieldError
	switch {
	case req.CurrentPassword == nil:
		errs = append(errs, config.FieldError{Field: "current_password", Message: "is required to change the password"})
	case len(*req.CurrentPassword) > maxPasswordLen:
		errs = append(errs, config.FieldError{Field: "current_password", Message: "must be at most 1024 bytes"})
	}
	switch {
	case req.NewPassword == nil:
		errs = append(errs, config.FieldError{Field: "new_password", Message: "is required to change the password"})
	case len(*req.NewPassword) > maxPasswordLen:
		errs = append(errs, config.FieldError{Field: "new_password", Message: "must be at most 1024 bytes"})
	case utf8.RuneCountInString(*req.NewPassword) < minPasswordLen:
		errs = append(errs, config.FieldError{Field: "new_password", Message: "must be at least 8 characters"})
	}
	return errs
}

// changePassword handles PUT /api/settings carrying current_password + new_password.
// The body is decoded first only to tell a password change from a plain update; the
// decode is size-capped and cheap. On success it re-hashes, rotates the session secret, drops every other session and
// returns a fresh CSRF token for the caller. A wrong current password is a 400 field
// error (not 401) so the UI does not mistake it for session expiry.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, req settingsRequest) {
	// Shared auth limit before any argon2 work; plain settings updates
	// never reach here, so they spend no budget.
	if !s.limitAuth(w, r, "password-change") {
		return
	}
	if !tryEnterHashQueue(w) {
		return
	}
	defer leaveHashQueue()
	if errs := changePasswordErrors(req); len(errs) > 0 {
		writeFieldErrors(w, errs)
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil { // requireAuth guarantees a cookie; fail closed regardless
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	oldHash := s.store.Get().Settings.AdminPasswordHash
	if !s.verifyAdminPassword(w, r, "change password", *req.CurrentPassword, oldHash) {
		return
	}
	hash, err := hashPassword(r.Context(), *req.NewPassword, s.params)
	if err != nil {
		s.log.Error("change password: hash", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	secret, err := randomToken(32)
	if err != nil {
		s.log.Error("change password: session secret", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var updated config.Settings
	err = s.store.Update(func(c *config.Config) error {
		if c.Settings.AdminPasswordHash != oldHash {
			return errPasswordRaced
		}
		c.Settings.AdminPasswordHash = hash
		c.Settings.SessionSecret = secret
		c.Settings.UIBindAddr = req.UIBindAddr
		updated = c.Settings
		return nil
	})
	if errors.Is(err, errPasswordRaced) {
		writeError(w, http.StatusConflict, "password changed concurrently; retry")
		return
	}
	if err != nil {
		s.writeStoreError(w, "change password", err, "settings")
		return
	}
	s.sessions.deleteAllExcept(cookie.Value)
	s.log.Info("admin password changed", "remote", clientKey(r))
	resp := s.settingsView(updated)
	resp.CSRFToken = csrfToken(secret, cookie.Value)
	writeJSON(w, http.StatusOK, resp)
}
