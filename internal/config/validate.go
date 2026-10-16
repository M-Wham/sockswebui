package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
)

// Validate checks a full config. It is pure: no I/O and no DNS. It returns nil or a
// ValidationErrors listing every failure with its field path.
func Validate(c Config) error {
	var errs ValidationErrors
	validateEndpoints(&errs, c.Endpoints)
	uiAddr, uiOK := validateSettings(&errs, c.Settings)
	validateSlots(&errs, c, uiAddr, uiOK)
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func validateSettings(errs *ValidationErrors, s Settings) (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(s.UIBindAddr)
	if err != nil || ap.Port() == 0 {
		errs.add("settings.ui_bind_addr", "must be a literal IP and port (e.g. 0.0.0.0:8787)")
		return netip.AddrPort{}, false
	}
	return ap, true
}

func validateEndpoints(errs *ValidationErrors, eps []Endpoint) {
	seen := make(map[string]bool, len(eps))
	for i, e := range eps {
		p := fmt.Sprintf("endpoints[%d]", i)
		switch {
		case e.ID == "":
			errs.add(p+".id", "is required")
		case seen[e.ID]:
			errs.add(p+".id", "duplicate endpoint id %q", e.ID)
		}
		seen[e.ID] = true
		*errs = append(*errs, ValidateEndpoint(p, e)...)
	}
}

// ValidateEndpoint checks one endpoint's own fields, reporting errors under path (e.g.
// "endpoints[0]"). It returns the same per-endpoint errors Validate reports; id presence and
// uniqueness are collection rules checked only by Validate. It returns nil when valid.
func ValidateEndpoint(path string, e Endpoint) ValidationErrors {
	var errs ValidationErrors
	switch e.Kind {
	case KindWireGuard:
		validateWireGuard(&errs, path, e)
	case KindSOCKS5, KindHTTP:
		if e.Host == "" {
			errs.add(path+".host", "is required for %s endpoints", e.Kind)
		} else if !validHost(e.Host) {
			errs.add(path+".host", "must be an IP address or a DNS hostname (letters, digits, hyphens and dots)")
		}
		if !validPort(e.Port) {
			errs.add(path+".port", "must be between 1 and 65535")
		}
		validateSession(&errs, path, e)
	default:
		errs.add(path+".kind", "must be one of wireguard, socks5, http")
	}
	return errs
}

func validateWireGuard(errs *ValidationErrors, p string, e Endpoint) {
	if e.PeerPublicKey == "" {
		errs.add(p+".peer_public_key", "is required for wireguard endpoints")
	} else if !validKey(e.PeerPublicKey) {
		errs.add(p+".peer_public_key", "must be a base64-encoded 32-byte key")
	}
	if e.PrivateKey != "" && !validKey(e.PrivateKey) {
		errs.add(p+".private_key", "must be a base64-encoded 32-byte key")
	}
	if e.PresharedKey != "" && !validKey(e.PresharedKey) {
		errs.add(p+".preshared_key", "must be a base64-encoded 32-byte key")
	}
	if e.Endpoint == "" {
		errs.add(p+".endpoint", "is required for wireguard endpoints")
	} else if host, port, err := net.SplitHostPort(e.Endpoint); err != nil || host == "" || !validPortString(port) {
		errs.add(p+".endpoint", "must be host:port with a port between 1 and 65535")
	}
	if e.MTU < 0 || e.MTU > 65535 {
		errs.add(p+".mtu", "must be between 0 (default) and 65535")
	}
}

func validateSlots(errs *ValidationErrors, c Config, ui netip.AddrPort, uiOK bool) {
	slots, eps := c.Slots, c.Endpoints
	if len(slots) > MaxSlots {
		errs.add("slots", "at most %d slots are allowed, got %d", MaxSlots, len(slots))
	}
	ids := make(map[int]bool, len(slots))
	type listener struct {
		addr netip.Addr
		port int
		idx  int
	}
	var bound []listener
	for i, s := range slots {
		p := fmt.Sprintf("slots[%d]", i)
		if s.ID < 1 || s.ID > MaxSlots {
			errs.add(p+".id", "must be between 1 and %d", MaxSlots)
		} else if ids[s.ID] {
			errs.add(p+".id", "duplicate slot id %d", s.ID)
		}
		ids[s.ID] = true

		addr, addrErr := netip.ParseAddr(s.BindAddr)
		if addrErr != nil || addr.Zone() != "" {
			errs.add(p+".bind_addr", "must be a literal IP address (e.g. 0.0.0.0 or 127.0.0.1)")
		}
		if !validPort(s.Port) {
			errs.add(p+".port", "must be between 1 and 65535")
		} else if addrErr == nil {
			if uiOK && int(ui.Port()) == s.Port && overlaps(addr, ui.Addr()) {
				errs.add(p+".port", "port %d clashes with the web UI address %s", s.Port, ui)
			}
			for _, b := range bound {
				if b.port == s.Port && overlaps(addr, b.addr) {
					errs.add(p+".port", "port %d on %s clashes with slots[%d]", s.Port, s.BindAddr, b.idx)
					break
				}
			}
			bound = append(bound, listener{addr: addr, port: s.Port, idx: i})
		}

		if s.Killswitch != KillswitchBlock && s.Killswitch != KillswitchAllowDirect {
			errs.add(p+".killswitch", "must be %q or %q", KillswitchBlock, KillswitchAllowDirect)
		}
		if s.SocksAuth != nil && s.SocksAuth.Username == "" {
			errs.add(p+".socks_auth.username", "is required when SOCKS auth is enabled")
		}
		if s.Enabled && SlotSecretsMissing(c, s) {
			errs.add(p+".enabled", "%s", SecretsMissingMessage)
		}
		validateHealth(errs, p+".health", s.Health)
		*errs = append(*errs, ValidateRoute(p+".route", s.Route, eps)...)
	}
}

func validateHealth(errs *ValidationErrors, p string, h Health) {
	if h.IntervalS < 1 {
		errs.add(p+".interval_s", "must be at least 1 second")
	}
	if h.FailThreshold < 1 {
		errs.add(p+".fail_threshold", "must be at least 1")
	}
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs.add(p+".url", "must be an absolute http or https URL")
	}
}

// overlaps reports whether two listen addresses can conflict on the same port:
// identical addresses, or either one is a wildcard (0.0.0.0 / ::).
func overlaps(a, b netip.Addr) bool {
	a, b = a.Unmap(), b.Unmap()
	return a == b || a.IsUnspecified() || b.IsUnspecified()
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func validPortString(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && validPort(n)
}

func validKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}
