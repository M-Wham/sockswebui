package api

import (
	"slices"
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
)

// secrets_omitted is server-owned. The request DTOs have no such field and
// decodeJSON does not reject unknown fields, so a body that carries it is silently ignored,
// just like session.current_id: the stored list always wins. A listed name is removed only
// when the request sets that secret to a non-empty value.

// keepOmitted returns the names in prev that are still missing: those for which missing
// reports true. The result is nil (absent from JSON) when nothing is left.
func keepOmitted(prev []string, missing func(name string) bool) []string {
	var out []string
	for _, n := range prev {
		if missing(n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// endpointOmitted prunes prev's list for the new endpoint e. A name stays while it exists
// for e's kind and e's field for it is still empty, including across a kind change
// (socks5<->http both have a password), so a switch cannot clear a missing secret (fail
// closed). Only names the new kind does not have are dropped.
func endpointOmitted(e, prev config.Endpoint) []string {
	return keepOmitted(prev.SecretsOmitted, func(n string) bool {
		switch {
		case n == config.OmittedPrivateKey && e.Kind == config.KindWireGuard:
			return e.PrivateKey == ""
		case n == config.OmittedPresharedKey && e.Kind == config.KindWireGuard:
			return e.PresharedKey == ""
		case n == config.OmittedPassword && e.Kind != config.KindWireGuard:
			return e.Password == ""
		}
		return false
	})
}

// slotOmitted prunes prev's list for the new slot sl: the SOCKS password stays listed while
// SOCKS auth is on and its password is still empty.
func slotOmitted(sl config.Slot, prev *config.Slot) []string {
	if prev == nil {
		return nil
	}
	return keepOmitted(prev.SecretsOmitted, func(n string) bool {
		return n == config.OmittedSocksPassword && sl.SocksAuth != nil && sl.SocksAuth.Password == ""
	})
}

// passwordHeldSession reports whether e's {session} token lives in a password that is
// listed as omitted: the session object is kept and the token is not in the username.
func passwordHeldSession(e config.Endpoint) bool {
	return slices.Contains(e.SecretsOmitted, config.OmittedPassword) && e.Password == "" &&
		e.Session != (config.Session{}) && !strings.Contains(e.Username, config.SessionToken)
}
