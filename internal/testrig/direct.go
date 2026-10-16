package testrig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
)

// errDirectClosed is returned by DirectChain.DialContext after Close.
var errDirectClosed = errors.New("testrig: direct chain closed")

// DirectChain is a test-only stand-in for the egress empty-route ("direct")
// chain. It has the egress.Chain method set, so tests can inject it through
// the supervisor's builder seam (which lets tests inject a fake chain) and reach the loopback
// sentinel and echo servers once production egress denies loopback. It
// refuses any target that is not a loopback IP literal, so it can never
// reach the real network (tests stay hermetic). Testrig, like tests, may
// open sockets outside internal/egress.
type DirectChain struct {
	d        net.Dialer
	blockLAN bool
	closed   atomic.Bool
}

// LoopbackDirect returns a fresh DirectChain that records blockLAN, the
// block_lan_targets policy the supervisor built it with (BlockLAN). The
// policy does not change dialing: only loopback is ever reachable. It holds
// no resources; Close only makes later dials fail and leaves returned conns
// open.
func LoopbackDirect(blockLAN bool) *DirectChain {
	return &DirectChain{d: net.Dialer{Timeout: 5 * time.Second}, blockLAN: blockLAN}
}

// BlockLAN reports the block_lan_targets policy the chain was built with.
func (c *DirectChain) BlockLAN() bool { return c.blockLAN }

// DialContext dials addr with a plain net.Dialer if addr is a loopback
// IP:port; any other target is refused.
func (c *DirectChain) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if c.closed.Load() {
		return nil, errDirectClosed
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().Unmap().IsLoopback() {
		return nil, fmt.Errorf("testrig: direct chain refuses non-loopback target %q", addr)
	}
	return c.d.DialContext(ctx, network, addr)
}

// Close marks the chain closed; it is idempotent.
func (c *DirectChain) Close() error {
	c.closed.Store(true)
	return nil
}
