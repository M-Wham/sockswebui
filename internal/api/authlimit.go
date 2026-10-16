package api

import (
	"net"
	"net/http"
	"net/netip"
)

// clientKey is the auth rate-limit key for r (a client is one IPv4 address or
// one IPv6 /64). The host part of r.RemoteAddr is parsed as an IP and
// unmapped first, so IPv4-mapped IPv6 keys by its full IPv4 address; any other IPv6 keys
// by its /64 prefix with the zone dropped. An unparsable RemoteAddr is used verbatim so it
// never shares a budget with other clients. Proxy headers such as X-Forwarded-For are
// deliberately ignored: they are client-controlled.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return r.RemoteAddr
	}
	a = a.Unmap() // must precede the Is4 check: a mapped IPv4 is not an IPv6 client
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a.WithZone(""), 64).Masked().String()
}

// limitAuth applies the shared auth rate limit: every password-bearing
// route draws on the one s.loginLimiter budget per client. route only labels the log
// line. On rejection it writes 429 with Retry-After and returns false.
func (s *Server) limitAuth(w http.ResponseWriter, r *http.Request, route string) bool {
	ok, retry := s.loginLimiter.allow(clientKey(r))
	if ok {
		return true
	}
	setRetryAfter(w, retry)
	if key := clientKey(r); s.agg.allow("login_rate_limited", key) {
		s.log.Warn("login rate limited", "route", route, "remote", key)
	}
	writeError(w, http.StatusTooManyRequests, "too many login attempts")
	return false
}
