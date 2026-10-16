package egress

// wgEncapOverhead is the per-packet cost of carrying a WireGuard tunnel
// inside another (outer IPv6 40 + UDP 8 + WireGuard 32 = 80 bytes).
const wgEncapOverhead = 80

// innerMTU is the effective MTU of a nested (non-first) WireGuard hop whose
// carrying hop has effective MTU prevEffective.
//
// Rule: inner = min(configured if > 0 else 1420, prevEffective − 80).
//
// The importer always writes an MTU (default 1420), so config alone cannot
// tell "unset" from an explicit 1420. The rule never needs to: an explicit
// override BELOW outer−80 (e.g. 1280) wins, while the default — or anything
// larger — is clamped to outer−80, because an inner MTU above that cannot fit
// inside the outer tunnel anyway.
func innerMTU(prevEffective, configured int) int {
	m := configured
	if m <= 0 {
		m = defaultWGMTU
	}
	return min(m, prevEffective-wgEncapOverhead)
}

// wgMTUs maps the configured MTUs of a route's WireGuard hops (route order)
// to their effective MTUs. The first hop rides the device's own link, so it
// keeps its configured MTU (default 1420 when unset); every later hop gets
// innerMTU of the hop before it.
func wgMTUs(configured []int) []int {
	out := make([]int, len(configured))
	for i, m := range configured {
		switch {
		case i > 0:
			out[i] = innerMTU(out[i-1], m)
		case m > 0:
			out[i] = m
		default:
			out[i] = defaultWGMTU
		}
	}
	return out
}
