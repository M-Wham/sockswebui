package testrig

import (
	"fmt"
	"net"
	"net/netip"
)

// resolveTarget maps a "host:port" target received by a fake upstream to a
// dialable "ip:port". IP literals pass through; hostnames are looked up ONLY in
// the upstream's static hosts map (hostname → IP, or hostname → IP:port to also
// redirect the port, e.g. "echo.rig.invalid:80" to an ephemeral echo port). The
// device resolver is never consulted, so an unknown hostname is an error.
func resolveTarget(hosts map[string]string, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return target, nil
	}
	ip, ok := hosts[host]
	if !ok {
		return "", fmt.Errorf("testrig: host %q not in static hosts map", host)
	}
	if ap, err := netip.ParseAddrPort(ip); err == nil {
		return ap.String(), nil
	}
	if _, err := netip.ParseAddr(ip); err != nil {
		return "", fmt.Errorf("testrig: hosts[%q] = %q is not an IP", host, ip)
	}
	return net.JoinHostPort(ip, port), nil
}
