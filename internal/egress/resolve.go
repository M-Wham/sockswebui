package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// dialTimeout bounds every TCP dial and every proxy handshake step.
const dialTimeout = 10 * time.Second

// deviceLookup is the single seam through which egress consults the device
// resolver. It is reached only by deviceDial: for the explicit direct ([])
// chain, and for the first proxy hop's own host when that host is a name.
// Target hostnames behind a proxy hop never come here: they are passed to the
// last proxy verbatim so DNS resolves at the exit, not on the device. Tests
// replace it.
var deviceLookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// deviceDial opens a TCP connection over the device route with no target
// check. It is the base dialer for a route's first proxy hop only; the
// direct (empty-route) chain uses deviceDialChecked(checkDirect).
func deviceDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialDevice(ctx, network, addr, nil)
}

// deviceDialChecked returns a device-route dialer that runs check on every
// parsed or resolved IP (zone intact) before dialing it. A refused IP is
// recorded and skipped; if every IP is refused, nothing is dialed.
func deviceDialChecked(check func(netip.Addr) error) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialDevice(ctx, network, addr, check)
	}
}

// dialDevice is the only place egress constructs a net.Dialer. Hostnames are
// resolved via deviceLookup first, so the dialer itself only ever sees IP
// literals.
func dialDevice(ctx context.Context, network, addr string, check func(netip.Addr) error) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: bad address %q: %w", addr, err)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		if ips, err = deviceLookup(ctx, host); err != nil {
			return nil, fmt.Errorf("egress: resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("egress: resolve %q: no addresses", host)
		}
	}
	d := &net.Dialer{Timeout: dialTimeout}
	var errs []error
	for _, ip := range ips {
		if check != nil {
			if err := check(ip); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("egress: dial %s: %w", addr, errors.Join(errs...))
}
