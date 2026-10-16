package supervisor

import (
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/M-Wham/sockswebui/internal/egress"
)

// redactedMark replaces secret values in log-safe messages.
const redactedMark = "[REDACTED]"

// secrets lists every configured secret of this slot: WG private/preshared
// keys, proxy passwords (stored and rendered with the session ID) and the SOCKS
// password, since secrets are never logged.
func (s *Slot) secrets() []string {
	var out []string
	for _, e := range s.eps {
		out = append(out, e.PrivateKey, e.PresharedKey, e.Password)
		// The bare session ID is loggable (it is not a secret); validation forbids a
		// password that is only {session}, but never scrub the ID regardless.
		if _, rp := egress.RenderCredentials(e); rp != e.Session.CurrentID {
			out = append(out, rp)
		}
		out = append(out, hexForms(e.PrivateKey)...)
		out = append(out, hexForms(e.PresharedKey)...)
	}
	if a := s.cfg.SocksAuth; a != nil {
		out = append(out, a.Password)
	}
	return out
}

// redact scrubs every configured secret of this slot from msg. Defence in
// depth: egress and config errors already name fields, never values.
func (s *Slot) redact(msg string) string { return newRedactor(s.secrets()).str(msg) }

// hexForms returns the lower- and upper-case hex encodings of a base64
// WireGuard key, the form the UAPI (and so wireguard-go errors) uses.
func hexForms(key string) []string {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil
	}
	lo := hex.EncodeToString(raw)
	return []string{lo, strings.ToUpper(lo)}
}
