package egress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"github.com/M-Wham/sockswebui/internal/egress/netstack"
)

// netBind is a wireguard-go conn.Bind whose UDP socket lives on the PREVIOUS
// WireGuard hop's netstack (gonet), so a nested hop's encrypted datagrams are
// themselves carried through the outer tunnel and never touch an OS socket.
//
// It binds one socket on local (one of the outer hop's tunnel addresses);
// endpoints must be of the same address family. Endpoints are IP literals
// only: nothing is ever resolved (fail closed, no device DNS).
type netBind struct {
	tnet  *netstack.Net
	local netip.Addr

	mu sync.Mutex
	uc *gonet.UDPConn
}

var _ conn.Bind = (*netBind)(nil)

func newNetBind(tnet *netstack.Net, local netip.Addr) *netBind {
	return &netBind{tnet: tnet, local: local.Unmap()}
}

// netEndpoint is a destination on the outer tunnel's network.
type netEndpoint struct{ ap netip.AddrPort }

func (e netEndpoint) ClearSrc()           {}
func (e netEndpoint) SrcToString() string { return "" }
func (e netEndpoint) DstToString() string { return e.ap.String() }
func (e netEndpoint) DstToBytes() []byte  { b, _ := e.ap.MarshalBinary(); return b }
func (e netEndpoint) DstIP() netip.Addr   { return e.ap.Addr() }
func (e netEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// Open binds local:port (0 = ephemeral) on the outer netstack and returns a
// single ReceiveFunc. After Close it returns net.ErrClosed, which tells
// wireguard-go's receive routine to stop.
func (b *netBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.uc != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	uc, err := b.tnet.ListenUDPAddrPort(netip.AddrPortFrom(b.local, port))
	if err != nil {
		return nil, 0, fmt.Errorf("egress: nested WireGuard bind on %s: %w", b.local, err)
	}
	ap, err := netip.ParseAddrPort(uc.LocalAddr().String())
	if err != nil {
		_ = uc.Close()
		return nil, 0, fmt.Errorf("egress: nested WireGuard bind: local address: %w", err)
	}
	b.uc = uc
	return []conn.ReceiveFunc{b.receiveFunc(uc)}, ap.Port(), nil
}

func (b *netBind) receiveFunc(uc *gonet.UDPConn) conn.ReceiveFunc {
	return func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, from, err := uc.ReadFrom(bufs[0])
		if err != nil {
			if b.isClosed(uc) {
				return 0, net.ErrClosed
			}
			return 0, err
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			return 0, fmt.Errorf("egress: nested WireGuard bind: unexpected source %T", from)
		}
		ap := ua.AddrPort()
		sizes[0] = n
		eps[0] = netEndpoint{netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}
		return 1, nil
	}
}

// isClosed reports whether uc is no longer the bind's open socket.
func (b *netBind) isClosed(uc *gonet.UDPConn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.uc != uc
}

// Close closes the socket, unblocking any pending receive. Idempotent.
func (b *netBind) Close() error {
	b.mu.Lock()
	uc := b.uc
	b.uc = nil
	b.mu.Unlock()
	if uc == nil {
		return nil
	}
	return uc.Close()
}

// SetMark is a no-op: there is no OS socket to mark.
func (b *netBind) SetMark(uint32) error { return nil }

// BatchSize is 1: one datagram per read/write.
func (b *netBind) BatchSize() int { return 1 }

// ParseEndpoint accepts only an IP literal ip:port (never resolves names).
func (b *netBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil || ap.Addr().Zone() != "" {
		return nil, errors.New("egress: nested WireGuard endpoint must be an IP literal ip:port")
	}
	return netEndpoint{netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// Send writes each buffer to ep through the outer tunnel.
func (b *netBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.mu.Lock()
	uc := b.uc
	b.mu.Unlock()
	if uc == nil {
		return net.ErrClosed
	}
	ne, ok := ep.(netEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	if ne.ap.Addr().Is4() != b.local.Is4() {
		return fmt.Errorf("egress: nested WireGuard endpoint %s: address family differs from bind %s", ne.ap, b.local)
	}
	dst := net.UDPAddrFromAddrPort(ne.ap)
	for _, buf := range bufs {
		if _, err := uc.WriteTo(buf, dst); err != nil {
			return err
		}
	}
	return nil
}
