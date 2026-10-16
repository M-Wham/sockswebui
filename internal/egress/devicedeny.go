package egress

import (
	"errors"
	"fmt"
	"net/netip"
)

// ErrDeniedTarget is returned when the direct chain refuses a target address
// that must never be reached over the device route (loopback, link-local,
// cloud metadata and similar; see deniedAt).
var ErrDeniedTarget = errors.New("egress: target address not allowed on the device route")

// directPolicy selects how the direct chain treats the LAN class (step 5).
// It can only add step 5: no value skips steps 1-4, which always apply.
type directPolicy uint8

const (
	// policyLANAllowed is the default (block_lan_targets false): LAN targets pass.
	policyLANAllowed directPolicy = 0
	// policyBlockLAN (block_lan_targets true) also denies the LAN class.
	policyBlockLAN directPolicy = 1
)

// policyFor maps the slot's block_lan_targets flag to a policy.
func policyFor(blockLAN bool) directPolicy {
	if blockLAN {
		return policyBlockLAN
	}
	return policyLANAllowed
}

// denyLAN reports whether step 5 denies the LAN class under p.
func (p directPolicy) denyLAN() bool { return p == policyBlockLAN }

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// nat64WKP is the RFC 6052 well-known prefix; its low 32 bits are an IPv4.
var nat64WKP = netip.MustParsePrefix("64:ff9b::/96")

// alwaysDeny is step 4: denied whatever block_lan_targets says.
var alwaysDeny = prefixes(
	// unspecified and "this network"
	"0.0.0.0/8", "::/128",
	// loopback
	"127.0.0.0/8", "::1/128",
	// link-local and deprecated site-local
	"169.254.0.0/16", "fe80::/10", "fec0::/10",
	// multicast, reserved, broadcast
	"224.0.0.0/4", "ff00::/8", "240.0.0.0/4", "255.255.255.255/32",
	// IPv4-compatible (deprecated) and discard-only
	"::/96", "100::/64",
	// documentation
	"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32", "3fff::/20",
	// transition forms, denied outright: 6to4 and Teredo
	"2002::/16", "2001::/32",
	// metadata pins inside the LAN class (AWS IMDS v6, Alibaba)
	"fd00:ec2::254/128", "100.100.100.200/32",
)

// lanClass is step 5: private ranges, allowed unless block_lan_targets.
var lanClass = prefixes(
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "64:ff9b:1::/48",
)

func inTable(table []netip.Prefix, ip netip.Addr) bool {
	for _, p := range table {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// denyStep names the device-route deny step that denied an address; stepAllow
// means it passed. Tests assert on it so the step order stays observable
// (e.g. a zoned mapped loopback must fall at step 1, not at the loopback row).
type denyStep uint8

const (
	stepAllow  denyStep = 0
	stepZone   denyStep = 1 // invalid or zoned original address
	stepNAT64  denyStep = 3 // embedded IPv4 of 64:ff9b::/96 denied
	stepAlways denyStep = 4 // always-deny table (incl. metadata pins)
	stepLAN    denyStep = 5 // LAN class under a block-LAN policy
)

// deniedOnDevice reports whether orig must not be dialed over the device
// route.
func deniedOnDevice(orig netip.Addr, p directPolicy) bool {
	return deniedAt(orig, p) != stepAllow
}

// deniedAt runs the checks in their load-bearing order (steps 1-6 below) and returns the step that denied orig.
func deniedAt(orig netip.Addr, p directPolicy) denyStep {
	// 1. Zone and validity on the ORIGINAL address: Unmap drops the zone.
	if !orig.IsValid() || orig.Zone() != "" {
		return stepZone
	}
	// 2. Unmap.
	ip := orig.Unmap()
	// 3. NAT64 well-known prefix: also judge the embedded IPv4.
	if nat64WKP.Contains(ip) {
		b := ip.As16()
		if classStep(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), p) != stepAllow {
			return stepNAT64
		}
	}
	// 4-6.
	return classStep(ip, p)
}

// classStep runs steps 4 (always-deny) and 5 (LAN class) on an unmapped,
// zoneless address; otherwise step 6 allows.
func classStep(ip netip.Addr, p directPolicy) denyStep {
	if inTable(alwaysDeny, ip) {
		return stepAlways
	}
	if inTable(lanClass, ip) && p.denyLAN() {
		return stepLAN
	}
	return stepAllow
}

// directCheck returns the production per-IP check for the direct chain
// under the slot's block_lan_targets flag.
func directCheck(blockLAN bool) func(netip.Addr) error {
	p := policyFor(blockLAN)
	return func(ip netip.Addr) error {
		if deniedOnDevice(ip, p) {
			return fmt.Errorf("%w: %s", ErrDeniedTarget, ip)
		}
		return nil
	}
}

// checkDirect is the default-policy check (block_lan_targets false).
var checkDirect = directCheck(false)
