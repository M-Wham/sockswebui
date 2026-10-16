package egress

import (
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
)

// RenderCredentials returns the username and password to send upstream: the
// stored fields with config.SessionToken replaced by the endpoint's current
// session ID, so a rotation changes the upstream identity. It renders at dial time and never mutates ep;
// an endpoint without a session ID gets its fields back unchanged. Exported
// so log redactors register exactly the password egress sends.
func RenderCredentials(ep config.Endpoint) (user, pass string) {
	id := ep.Session.CurrentID
	if id == "" {
		return ep.Username, ep.Password
	}
	return strings.ReplaceAll(ep.Username, config.SessionToken, id),
		strings.ReplaceAll(ep.Password, config.SessionToken, id)
}
