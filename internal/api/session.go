package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

const (
	sessionCookieName = "sockswebui_session"
	csrfHeader        = "X-CSRF-Token"
	sessionTTL        = 24 * time.Hour
	maxSessions       = 64
)

// sessionStore keeps opaque session tokens in memory with an absolute expiry.
// Sessions do not survive a restart; users simply log in again.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	now      func() time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time), now: time.Now}
}

// create returns a new random 32-byte token (base64url). Expired sessions are purged;
// if still at capacity, the session closest to expiry is evicted.
func (s *sessionStore) create() (string, error) {
	tok, err := randomToken(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var oldest string
	for t, exp := range s.sessions {
		if !now.Before(exp) {
			delete(s.sessions, t)
		} else if oldest == "" || exp.Before(s.sessions[oldest]) {
			oldest = t
		}
	}
	if len(s.sessions) >= maxSessions {
		delete(s.sessions, oldest)
	}
	s.sessions[tok] = now.Add(sessionTTL)
	return tok, nil
}

// valid reports whether tok is a live session. Expired sessions are removed.
func (s *sessionStore) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if !s.now().Before(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

func (s *sessionStore) delete(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tok)
}

// deleteAllExcept drops every session but keep (used after a password change).
func (s *sessionStore) deleteAllExcept(keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t := range s.sessions {
		if t != keep {
			delete(s.sessions, t)
		}
	}
}

// csrfToken derives the CSRF token for a session: base64url(HMAC-SHA256(secret, token)).
func csrfToken(secret, sessionTok string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(sessionTok))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// csrfValid compares the presented token with the expected one in constant time.
func csrfValid(secret, sessionTok, presented string) bool {
	if secret == "" || presented == "" {
		return false
	}
	want := csrfToken(secret, sessionTok)
	return subtle.ConstantTimeCompare([]byte(want), []byte(presented)) == 1
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
