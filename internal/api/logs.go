package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

const redactedMark = "[REDACTED]"

type logsResponse struct {
	Slot  int                  `json:"slot"`
	Lines []supervisor.LogLine `json:"lines"`
}

// handleSlotLogs returns the slot's ring buffer, oldest first.
// The supervisor already redacts; this re-scrubs every configured secret.
func (s *Server) handleSlotLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := slotID(w, r)
	if !ok {
		return
	}
	red := newSecretRedactor(s.store.Get())
	lines := s.ctrl.Logs(id)
	out := logsResponse{Slot: id, Lines: make([]supervisor.LogLine, 0, len(lines))}
	for _, l := range lines {
		l.Msg, l.Text, l.Level = red.Replace(l.Msg), red.Replace(l.Text), red.Replace(l.Level)
		out.Lines = append(out.Lines, l)
	}
	writeJSON(w, http.StatusOK, out)
}

// newSecretRedactor replaces every configured secret value (WG keys, proxy and
// SOCKS passwords, admin hash, session secret) with redactedMark.
func newSecretRedactor(c config.Config) *strings.Replacer { return replacerFor(secretValues(c)) }

// secretValues lists the configured secret values (see newSecretRedactor).
func secretValues(c config.Config) []string {
	var out []string
	for _, e := range c.Endpoints {
		_, rendered := egress.RenderCredentials(e)
		out = append(out, e.PrivateKey, e.PresharedKey, e.Password, rendered)
	}
	for _, sl := range c.Slots {
		if sl.SocksAuth != nil {
			out = append(out, sl.SocksAuth.Password)
		}
	}
	return append(out, c.Settings.AdminPasswordHash, c.Settings.SessionSecret)
}

// replacerFor maps every non-empty value to redactedMark, longest first, so a
// value that contains another is replaced whole.
func replacerFor(values []string) *strings.Replacer {
	vals := slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == "" })
	slices.SortStableFunc(vals, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(vals))
	for _, v := range vals {
		pairs = append(pairs, v, redactedMark)
	}
	return strings.NewReplacer(pairs...)
}
