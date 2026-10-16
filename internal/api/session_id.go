package api

import (
	"io"

	"github.com/M-Wham/sockswebui/internal/config"
)

// ensureSessionID applies the sticky-session ID rules to next, which already carries the
// stored current_id (see mergeSession). It generates a new ID when none is stored or the
// stored one no longer fits charset/length; every other save keeps it. Invalid charset or
// length are left for config.Validate to report. Any reader failure is returned (500).
func ensureSessionID(next *config.Endpoint, r io.Reader) error {
	s := &next.Session
	if *s == (config.Session{}) {
		return nil
	}
	if s.CurrentID != "" && config.SessionIDFits(s.CurrentID, s.Charset, s.Length) {
		return nil
	}
	old := s.CurrentID
	s.CurrentID = ""
	if s.Charset != config.SessionAlnum && s.Charset != config.SessionDigits {
		return nil
	}
	if s.Length < config.MinSessionLength || s.Length > config.MaxSessionLength {
		return nil
	}
	id, err := config.NewSessionID(r, s.Charset, s.Length, old)
	if err != nil {
		return err
	}
	s.CurrentID = id
	return nil
}
