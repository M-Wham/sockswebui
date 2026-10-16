package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/M-Wham/sockswebui/internal/config"
)

// Chain dials through a slot's route. It never falls back to the device
// route on failure: the kill-switch policy lives in the supervisor.
type Chain interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

// ErrClosed is returned by DialContext after Close.
var ErrClosed = errors.New("egress: chain closed")

// dialFunc is one layer of the chain: it reaches addr through every hop so far.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// hopPlan is one validated hop, ready to be brought up.
type hopPlan struct {
	ep config.Endpoint
	wg *wgSpec
}

// Build validates route against endpoints (via config.ValidateRoute)
// and every hop's own fields, then composes the hop dialers in order. Nothing
// is opened until the whole route has validated. Proxy hops are dialed per
// connection; WireGuard hops are brought up here and owned by the chain. The
// first WireGuard hop gets the only OS UDP socket; every later WireGuard hop
// binds its UDP on the previous hop's netstack (WG-in-WG) with
// MTU per wgMTUs (outer − 80). An empty route is the
// direct (device route) chain; the device route is the base dialer ONLY when
// the route does not start with a WireGuard hop.
//
// The direct chain refuses denied targets per resolved IP (checkDirect, so
// the device route never reaches loopback, metadata or similar addresses); a route starting with a proxy keeps the unchecked deviceDial
// for that proxy's own address.
func Build(route []string, endpoints []config.Endpoint) (Chain, error) {
	return buildWith(route, endpoints, checkDirect)
}

// Direct returns the direct (device route) chain for a slot. blockLAN is the
// slot's block_lan_targets: true also denies the LAN class (step 5); false is
// the same chain as Build(nil, nil). The always-deny set (steps 1-4, incl. the
// metadata pins) applies either way.
func Direct(blockLAN bool) (Chain, error) {
	return buildWith(nil, nil, directCheck(blockLAN))
}

// buildWith is Build with the direct chain's per-IP check injected. Only
// Build (checkDirect) and tests (a permit-all hook) call it.
func buildWith(route []string, endpoints []config.Endpoint, check func(netip.Addr) error) (Chain, error) {
	plan, err := planRoute(route, endpoints)
	if err != nil {
		return nil, err
	}
	c := &chain{}
	dial := dialFunc(deviceDial)
	if len(plan) == 0 {
		dial = deviceDialChecked(check)
	}
	for _, h := range plan {
		switch h.ep.Kind {
		case config.KindWireGuard:
			var bind conn.Bind
			if n := len(c.tunnels); n == 0 {
				bind = newOSBind()
			} else {
				prev := c.tunnels[n-1]
				local, _ := bindAddr(prev.addrs, h.wg.endpoint) // checked in planRoute
				bind = newNetBind(prev.tnet, local)
			}
			t, err := startWGFn(*h.wg, bind)
			if err != nil {
				c.closeTunnels()
				return nil, err
			}
			c.tunnels = append(c.tunnels, t)
			dial = t.dial
		case config.KindSOCKS5:
			dial = socks5Hop(h.ep, dial)
		case config.KindHTTP:
			dial = httpHop(h.ep, dial)
		}
	}
	c.dial = dial
	return c, nil
}

// planRoute validates the route and each hop without opening anything.
func planRoute(route []string, endpoints []config.Endpoint) ([]hopPlan, error) {
	if errs := config.ValidateRoute("route", route, endpoints); len(errs) > 0 {
		return nil, errs
	}
	byID := make(map[string]config.Endpoint, len(endpoints))
	for _, e := range endpoints {
		byID[e.ID] = e
	}
	plan := make([]hopPlan, 0, len(route))
	var prevWG *wgSpec
	var wgEndpoints []netip.AddrPort // every earlier WG hop's endpoint, in route order
	var mtus []int
	for i, id := range route {
		ep := byID[id]
		path := fmt.Sprintf("route[%d]", i)
		if errs := config.ValidateEndpoint(path, ep); len(errs) > 0 {
			return nil, errs
		}
		h := hopPlan{ep: ep}
		switch ep.Kind {
		case config.KindWireGuard:
			s, err := parseWG(path, ep)
			if err != nil {
				return nil, err
			}
			if prevWG != nil {
				if _, ok := bindAddr(prevWG.addrs, s.endpoint); !ok {
					return nil, fmt.Errorf("egress: %s.endpoint: hop %q has no tunnel address of the "+
						"same family to carry it", path, prevWG.id)
				}
				if err := checkNested(path, s.endpoint, prevWG, wgEndpoints); err != nil {
					return nil, err
				}
			}
			wgEndpoints = append(wgEndpoints, s.endpoint)
			mtus = append(mtus, ep.MTU)
			s.mtu = wgMTUs(mtus)[len(mtus)-1]
			h.wg, prevWG = &s, &s
		case config.KindSOCKS5, config.KindHTTP:
		default:
			return nil, fmt.Errorf("egress: %s %q: %s hops are not supported", path, id, ep.Kind)
		}
		plan = append(plan, h)
	}
	return plan, nil
}

// checkNested validates a nested WireGuard hop's endpoint so its UDP can only
// ride the outer tunnel, never an OS socket: it must
// differ from every earlier WireGuard endpoint, and it must fall inside one of
// the outer hop's allowed_ips prefixes of the same address family (a prefix of
// the other family never contains it), or the outer tunnel has no route for
// it. A build error, so the slot goes ERROR (fail closed).
func checkNested(path string, endpoint netip.AddrPort, outer *wgSpec, earlier []netip.AddrPort) error {
	for _, e := range earlier {
		if e == endpoint {
			return fmt.Errorf("egress: %s.endpoint: equals an earlier WireGuard hop's endpoint", path)
		}
	}
	for _, p := range outer.allowed {
		if p.Contains(endpoint.Addr()) {
			return nil
		}
	}
	return fmt.Errorf("egress: %s.endpoint: not inside hop %q's allowed_ips (same address family "+
		"required), so that tunnel cannot carry it", path, outer.id)
}

type chain struct {
	dial      dialFunc
	tunnels   []*wgTunnel // in route order
	closed    atomic.Bool
	closeOnce sync.Once
}

// DialContext opens a TCP connection to addr through the whole route. The
// host part of addr is never resolved here when the route has a proxy hop.
func (c *chain) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("egress: network %q not supported", network)
	}
	if err := checkAddr(addr); err != nil {
		return nil, err
	}
	return c.dial(ctx, network, addr)
}

// Close makes the chain refuse further dials and tears down its WireGuard
// tunnels (innermost first), which also closes the OS UDP socket. Proxy hops
// hold no resources between dials. Connections already returned belong to
// their callers; those riding a closed tunnel fail. Idempotent.
func (c *chain) Close() error {
	c.closed.Store(true)
	c.closeOnce.Do(c.closeTunnels)
	return nil
}

func (c *chain) closeTunnels() {
	for i := len(c.tunnels) - 1; i >= 0; i-- {
		c.tunnels[i].close()
	}
	c.tunnels = nil
}

// checkAddr rejects malformed targets, including any control or space byte
// that could inject into an HTTP CONNECT request line.
func checkAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("egress: bad address %q", addr)
	}
	for i := 0; i < len(addr); i++ {
		if b := addr[i]; b <= ' ' || b == 0x7f {
			return fmt.Errorf("egress: bad address %q", addr)
		}
	}
	return nil
}
