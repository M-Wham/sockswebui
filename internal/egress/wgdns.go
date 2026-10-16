package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// resolve maps host to tunnel-side addresses. IP literals pass through;
// names are looked up ONLY with the hop's own dns servers over its netstack
// so DNS resolves at the exit, never net.DefaultResolver or deviceLookup. No dns servers
// configured means a name cannot be resolved: fail closed.
func (t *wgTunnel) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	if len(t.dns) == 0 {
		return nil, fmt.Errorf("egress: WireGuard hop %q has no dns servers to resolve %q", t.id, host)
	}
	names, err := t.tnet.LookupContextHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolve %q via WireGuard hop %q: %w", host, t.id, err)
	}
	var ips []netip.Addr
	for _, n := range names {
		if ip, err := netip.ParseAddr(n); err == nil {
			ips = append(ips, ip.Unmap())
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("egress: resolve %q via WireGuard hop %q: no addresses", host, t.id)
	}
	return ips, nil
}

// dial opens a TCP connection to addr over the tunnel's netstack. It is the
// dialFunc for the WG hop: the target when WG is the last hop, otherwise the
// next proxy hop (whose hostname is then resolved by the tunnel too).
func (t *wgTunnel) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: bad address %q: %w", addr, err)
	}
	pn, err := strconv.ParseUint(port, 10, 16)
	if err != nil || pn == 0 {
		return nil, fmt.Errorf("egress: bad address %q", addr)
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	ips, err := t.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, ip := range ips {
		if (network == "tcp4" && !ip.Is4()) || (network == "tcp6" && !ip.Is6()) {
			continue
		}
		c, err := t.tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ip, uint16(pn)))
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("egress: dial %s via WireGuard hop %q: no %s address", addr, t.id, network)
	}
	return nil, fmt.Errorf("egress: dial %s via WireGuard hop %q: %w", addr, t.id, errors.Join(errs...))
}
