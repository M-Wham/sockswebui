package api

import (
	"strings"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// slotStatusView is the read-only live state of a slot. Every key is
// always present; a value that is not known yet is null. It never holds a secret:
// last_error is redacted by statusRedactor.
type slotStatusView struct {
	State     supervisor.State `json:"state"`
	ExitIP    *string          `json:"exit_ip"`
	Country   *string          `json:"country"`
	LatencyMS *int64           `json:"latency_ms"`
	LastError *string          `json:"last_error"`
	Since     *time.Time       `json:"since"`
	CheckedAt *time.Time       `json:"checked_at"`
}

// statusRedactor replaces every configured secret and, beyond the log redactor, the
// current session IDs and the usernames rendered from them (status holds
// no credentials, session IDs or rendered usernames).
func statusRedactor(c config.Config) *strings.Replacer {
	vals := secretValues(c)
	for _, e := range c.Endpoints {
		if id := e.Session.CurrentID; id != "" {
			vals = append(vals, id)
			if ru, _ := egress.RenderCredentials(e); ru != e.Username {
				vals = append(vals, ru)
			}
		}
	}
	return replacerFor(vals)
}

// toStatusView builds the view from a supervisor snapshot entry (nil: slot unknown
// to the controller, so STOPPED).
func toStatusView(st *supervisor.SlotStatus, red *strings.Replacer) slotStatusView {
	v := slotStatusView{State: supervisor.StateStopped}
	if st == nil {
		return v
	}
	v.State = st.State
	if !st.Since.IsZero() {
		t := st.Since
		v.Since = &t
	}
	var lastErr string
	if p := st.Probe; p != nil && st.State != supervisor.StateError {
		t := p.CheckedAt
		v.CheckedAt = &t
		if p.Error != "" {
			lastErr = p.Error
		} else {
			// exit_ip and country are already redacted by the supervisor; a session-ID
			// scrub here would mangle short IDs inside an IP address.
			v.ExitIP, v.Country = optStr(p.ExitIP), optStr(p.Country)
			v.LatencyMS = optInt(p.LatencyMS)
		}
	}
	if lastErr == "" && st.State == supervisor.StateError {
		lastErr = st.Msg
	}
	if lastErr != "" {
		lastErr = red.Replace(lastErr)
		v.LastError = &lastErr
	}
	return v
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optInt(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

// slotStatuses takes one controller snapshot and returns a lookup by slot id. The
// snapshot comes first, so the redactor's config is never older than the probe errors
// it scrubs (a rotated session ID cannot slip through).
func (s *Server) slotStatuses() func(id int) slotStatusView {
	snap := s.ctrl.Snapshot()
	red := statusRedactor(s.store.Get())
	return func(id int) slotStatusView {
		for i := range snap {
			if snap[i].ID == id {
				return toStatusView(&snap[i], red)
			}
		}
		return toStatusView(nil, red)
	}
}

// snapshotFrame is the first SSE frame: every controller slot with its state and
// last health. Msg gets the same redaction as status.
func (s *Server) snapshotFrame() snapshotEvent {
	snap := s.ctrl.Snapshot() // before the config, see slotStatuses
	red := statusRedactor(s.store.Get())
	out := snapshotEvent{Slots: make([]snapshotSlot, 0, len(snap))}
	for i := range snap {
		st := toStatusView(&snap[i], red)
		snap[i].Msg = red.Replace(snap[i].Msg)
		out.Slots = append(out.Slots, snapshotSlot{SlotStatus: snap[i], Status: st})
	}
	return out
}
