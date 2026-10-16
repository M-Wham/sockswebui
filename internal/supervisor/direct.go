package supervisor

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
)

// gate is the slot-owned SOCKS dialer: the chain while UP; the
// device route while DEGRADED only if the slot opted into "allow-direct";
// ErrNotUp (SOCKS reply 0x01) otherwise. Every conn it hands out is tracked
// so the kill switch can close it.
type gate struct{ s *Slot }

func (g gate) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	s := g.s
	s.mu.Lock()
	st, ch := s.state, s.chain
	s.mu.Unlock()
	switch {
	case st == StateUp && ch != nil:
		return s.dialTracked(ctx, ch, StateUp, ch, network, addr)
	case st == StateDegraded && s.cfg.Killswitch == config.KillswitchAllowDirect:
		return s.fallback(ctx, network, addr)
	}
	return nil, ErrNotUp
}

// fallback dials addr via the device route for an allow-direct slot in
// DEGRADED and logs one "fallback" line per connection. The
// direct dialer is the empty-route chain from the slot builder (egress.Direct
// in production; tests inject a fake through the builder seam), never a
// net.Dialer here: only internal/egress opens outbound sockets.
func (s *Slot) fallback(ctx context.Context, network, addr string) (net.Conn, error) {
	direct, err := s.buildRoute(nil)
	if err != nil {
		return nil, err
	}
	defer direct.Close() // holds no resources; returned conns stay open
	c, err := s.dialTracked(ctx, direct, StateDegraded, nil, network, addr)
	if err != nil {
		s.log.Warn("fallback dial failed", "reason", "allow_direct", "target", addr,
			"err", s.redact(err.Error()))
		return nil, err
	}
	s.log.Warn("fallback", "reason", "allow_direct", "target", addr)
	return c, nil
}

// buildRoute builds route through the slot builder with the slot's
// block_lan_targets policy. It is the one place both device-route paths (an
// empty-route slot and the allow-direct fallback) read the flag.
func (s *Slot) buildRoute(route []string) (egress.Chain, error) {
	return s.build(route, s.eps, s.cfg.BlockLANTargets)
}

// buildEgress is the production builder: an empty route is the direct
// (device route) chain under blockLAN; any other route is egress.Build,
// whose device dials (a first proxy hop's own host) are not filtered.
func buildEgress(route []string, eps []config.Endpoint, blockLAN bool) (egress.Chain, error) {
	if len(route) == 0 {
		return egress.Direct(blockLAN)
	}
	return egress.Build(route, eps)
}

// dialTracked dials through d and registers the conn, unless the slot left
// state want (or, when want is UP, swapped chain away from ch) mid-dial.
func (s *Slot) dialTracked(ctx context.Context, d Dialer, want State, ch egress.Chain, network, addr string) (net.Conn, error) {
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != want || s.chain != ch { // degraded, restored or stopped mid-dial
		_ = c.Close()
		return nil, ErrNotUp
	}
	tc := &trackedConn{Conn: c, s: s}
	s.conns[tc] = struct{}{}
	return tc, nil
}

// trackedConn removes itself from the slot's conn set on Close.
type trackedConn struct {
	net.Conn
	s    *Slot
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.s.mu.Lock()
		delete(c.s.conns, c)
		c.s.mu.Unlock()
	})
	return c.Conn.Close()
}

// CloseWrite keeps half-close working through the wrapper when supported.
func (c *trackedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.ErrUnsupported
}
