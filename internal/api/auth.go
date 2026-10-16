package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/M-Wham/sockswebui/internal/config"
)

const (
	minPasswordLen = 8    // runes
	maxPasswordLen = 1024 // bytes; bounds argon2 input and request size
	maxAuthBody    = 4096
)

var errAlreadySetup = errors.New("already set up")

type passwordRequest struct {
	Password string `json:"password"`
}

// decodePassword reads {"password": "..."}; on failure it writes a 400 and returns false.
func decodePassword(w http.ResponseWriter, r *http.Request, checkPolicy bool) (string, bool) {
	var req passwordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&req); err != nil {
		writeFieldErrors(w, []config.FieldError{{Field: "body", Message: "must be a JSON object with a password"}})
		return "", false
	}
	switch {
	case len(req.Password) > maxPasswordLen:
		writeFieldErrors(w, []config.FieldError{{Field: "password", Message: "must be at most 1024 bytes"}})
		return "", false
	case checkPolicy && utf8.RuneCountInString(req.Password) < minPasswordLen:
		writeFieldErrors(w, []config.FieldError{{Field: "password", Message: "must be at least 8 characters"}})
		return "", false
	}
	return req.Password, true
}

// handleSetup sets the admin password once, on a store with no admin hash.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.store.Get().Settings.AdminPasswordHash != "" {
		writeError(w, http.StatusConflict, "already set up")
		return
	}
	if !requireJSON(w, r) { // before the limiter and argon2
		return
	}
	// Shared auth limit after the 409 check, so a set-up box spends no
	// budget, and before decode and argon2, so a flood cannot exhaust hash slots.
	if !s.limitAuth(w, r, "setup") {
		return
	}
	if !tryEnterHashQueue(w) {
		return
	}
	defer leaveHashQueue()
	pw, ok := decodePassword(w, r, true)
	if !ok {
		return
	}
	hash, err := hashPasswordWith(r.Context(), pw, s.params, s.freeMem)
	if err != nil {
		s.log.Error("setup: hash password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	secret, err := randomToken(32)
	if err != nil {
		s.log.Error("setup: session secret", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	err = s.store.Update(func(c *config.Config) error {
		if c.Settings.AdminPasswordHash != "" {
			return errAlreadySetup
		}
		c.Settings.AdminPasswordHash = hash
		if c.Settings.SessionSecret == "" {
			c.Settings.SessionSecret = secret
		}
		return nil
	})
	switch {
	case errors.Is(err, errAlreadySetup):
		writeError(w, http.StatusConflict, "already set up")
	case err != nil:
		s.log.Error("setup: save config", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.log.Info("admin password set", "remote", clientKey(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleLogin verifies the admin password and starts a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	settings := s.store.Get().Settings
	if settings.AdminPasswordHash == "" {
		writeError(w, http.StatusConflict, "setup required")
		return
	}
	if !requireJSON(w, r) { // before the limiter and argon2
		return
	}
	// Rate-limit before decoding or running argon2 so a flood cannot exhaust hash slots.
	if !s.limitAuth(w, r, "login") {
		return
	}
	if !tryEnterHashQueue(w) {
		return
	}
	defer leaveHashQueue()
	pw, ok := decodePassword(w, r, false)
	if !ok {
		return
	}
	match, err := verifyPassword(r.Context(), pw, settings.AdminPasswordHash)
	if r.Context().Err() != nil {
		return // client gone while waiting for a hash slot
	}
	if err != nil {
		s.log.Error("login: stored admin hash is unusable", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !match {
		if key := clientKey(r); s.agg.allow("login_failed", key) {
			s.log.Warn("login failed", "remote", key)
		}
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	secret, err := s.sessionSecret(settings)
	if err != nil {
		s.log.Error("login: session secret", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	tok, err := s.sessions.create()
	if err != nil {
		s.log.Error("login: create session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: tok, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil, // TLS is a non-goal; mark Secure only when it is in use
	})
	s.log.Info("login succeeded", "remote", clientKey(r))
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": csrfToken(secret, tok)})
}

// sessionSecret returns the stored session secret, generating and persisting one if a
// hand-edited config lacks it.
func (s *Server) sessionSecret(settings config.Settings) (string, error) {
	if settings.SessionSecret != "" {
		return settings.SessionSecret, nil
	}
	fresh, err := randomToken(32)
	if err != nil {
		return "", err
	}
	err = s.store.Update(func(c *config.Config) error {
		if c.Settings.SessionSecret == "" {
			c.Settings.SessionSecret = fresh
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return s.store.Get().Settings.SessionSecret, nil
}

// handleLogout ends the caller's session (already authenticated by requireAuth).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
	s.log.Info("logout", "remote", clientKey(r))
	w.WriteHeader(http.StatusNoContent)
}

// isExempt reports whether the request may skip auth: only POST /api/setup and
// POST /api/login, and any non-API path (the static UI, wired in later).
func isExempt(r *http.Request) bool {
	if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	return r.Method == http.MethodPost && (r.URL.Path == "/api/setup" || r.URL.Path == "/api/login")
}

// safeMethod reports methods that must not change state and so skip the CSRF check.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// requireAuth enforces the session cookie on API routes and the CSRF header on
// state-changing methods. It fails closed: anything not explicitly exempt is gated.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookieName)
		if err != nil || !s.sessions.valid(c.Value) {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		if !safeMethod(r.Method) && !csrfValid(s.store.Get().Settings.SessionSecret, c.Value, r.Header.Get(csrfHeader)) {
			writeError(w, http.StatusForbidden, "missing or invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
