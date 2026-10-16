package api

import (
	"net/http"
	"net/url"
)

// WithNameList sets the accepted UI name list the Origin check uses. cmd passes the
// same list it gives HostGuard, so the two layers cannot drift. Without
// it the zero list applies: IP-literal Origins only.
func WithNameList(l NameList) Option { return func(s *Server) { s.names = l } }

// originCheck refuses (403) a state-changing request whose Origin header is present
// but does not name an accepted UI name. The Origin's
// hostname is matched against the name list, never against Host; scheme and port
// are ignored. "null" and malformed values are refused. An absent Origin and safe
// methods (GET, HEAD, OPTIONS, so SSE too) pass. It runs before requireAuth, so a
// refused request reaches no session check, limiter or handler.
func (s *Server) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vals, present := r.Header["Origin"]
		if present && !safeMethod(r.Method) && !s.originAllowed(vals) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(vals []string) bool {
	if len(vals) != 1 || vals[0] == "null" {
		return false
	}
	u, err := url.Parse(vals[0])
	if err != nil || u.Host == "" {
		return false
	}
	h := u.Hostname()
	return h != "" && s.names.Allows(h)
}
