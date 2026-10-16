package supervisor

import (
	"time"
	"unicode/utf8"

	"github.com/M-Wham/sockswebui/internal/egress"
)

// maxProbeErr caps the stored probe error text.
const maxProbeErr = 300

// ProbeStatus is the last health probe of a slot, as shown in slot status. Error is
// redacted like the slot log. ExitIP, Country and LatencyMS are zero after a
// failed probe.
type ProbeStatus struct {
	CheckedAt time.Time `json:"checked_at"`
	ExitIP    string    `json:"exit_ip,omitempty"`
	Country   string    `json:"country,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// SlotDetail is a read-only view of when the slot last changed state and of its
// last probe. Since is zero and Probe nil while unknown.
type SlotDetail struct {
	Since time.Time
	Probe *ProbeStatus
}

// Detail returns a copy of the slot's status detail.
func (s *Slot) Detail() SlotDetail {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := SlotDetail{Since: s.since}
	if s.lastProbe != nil {
		p := *s.lastProbe
		d.Probe = &p
	}
	return d
}

// noteStateLocked stamps a state change. A slot that is starting, stopped or in
// ERROR has no current probe result. Callers hold s.mu.
func (s *Slot) noteStateLocked(st State) {
	s.since = s.now()
	if st == StateStarting || st == StateStopped || st == StateError {
		s.lastProbe = nil
	}
}

// noteProbe records one probe outcome. The error text is redacted.
func (s *Slot) noteProbe(res Result, err error) {
	p := &ProbeStatus{CheckedAt: s.now()}
	if err != nil {
		p.Error = cutRunes(s.redactStatus(err.Error()), maxProbeErr)
	} else {
		p.ExitIP, p.Country, p.LatencyMS = s.redact(res.ExitIP), s.redact(res.Country), res.Latency.Milliseconds()
	}
	s.mu.Lock()
	s.lastProbe = p
	s.mu.Unlock()
}

// redactStatus is redact plus every route endpoint's rendered username and current
// session ID (status holds neither). It runs before the length cap, so a
// cut cannot leave a prefix of either. The rendered username goes first: it contains the ID.
func (s *Slot) redactStatus(msg string) string {
	vals := s.secrets()
	for _, e := range s.eps {
		if id := e.Session.CurrentID; id != "" {
			if ru, _ := egress.RenderCredentials(e); ru != e.Username {
				vals = append(vals, ru)
			}
			vals = append(vals, id)
		}
	}
	return newRedactor(vals).str(msg)
}

// cutRunes cuts str to at most n bytes without splitting a UTF-8 rune.
func cutRunes(str string, n int) string {
	if len(str) <= n {
		return str
	}
	for n > 0 && !utf8.RuneStart(str[n]) {
		n--
	}
	return str[:n]
}
