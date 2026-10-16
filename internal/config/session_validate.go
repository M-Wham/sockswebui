package config

import "strings"

// validateSession applies the sticky-session rules to a socks5/http endpoint. Messages never echo username
// or password contents. While the password is listed in SecretsOmitted the
// token-count rule is skipped: the token may have been in the password that was not exported.
func validateSession(errs *ValidationErrors, p string, e Endpoint) {
	tokens := strings.Count(e.Username, SessionToken) + strings.Count(e.Password, SessionToken)
	present := e.Session != Session{}
	if !passwordOmitted(e) && (tokens > 1 || (present && tokens != 1)) {
		errs.add(p+".session", "the %s token must appear exactly once across username and password", SessionToken)
	}
	if e.Username == SessionToken {
		errs.add(p+".username", "must contain fixed text besides the %s token", SessionToken)
	}
	if e.Password == SessionToken {
		errs.add(p+".password", "must contain fixed text besides the %s token", SessionToken)
	}
	if !present {
		return
	}
	s := e.Session
	alpha := sessionAlphabet(s.Charset)
	if alpha == "" {
		errs.add(p+".session.charset", "must be %q or %q", SessionAlnum, SessionDigits)
	}
	lengthOK := s.Length >= MinSessionLength && s.Length <= MaxSessionLength
	if !lengthOK {
		errs.add(p+".session.length", "must be between %d and %d", MinSessionLength, MaxSessionLength)
	}
	if s.CurrentID != "" && alpha != "" && lengthOK && !validSessionID(s.CurrentID, s.Charset, s.Length) {
		errs.add(p+".session.current_id", "does not match the session charset and length")
	}
	if e.Kind == KindSOCKS5 && lengthOK {
		for _, f := range []struct{ name, val string }{{"username", e.Username}, {"password", e.Password}} {
			if strings.Contains(f.val, SessionToken) && len(f.val)-len(SessionToken)+s.Length > 255 {
				errs.add(p+"."+f.name, "rendered value would exceed 255 bytes (SOCKS5 limit)")
			}
		}
	}
}
