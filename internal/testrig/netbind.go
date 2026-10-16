package testrig

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/conn"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"github.com/M-Wham/sockswebui/internal/egress/netstack"
)

// NetBind is a wireguard-go conn.Bind whose UDP socket lives on a netstack
// (another tunnel's network), never on an OS socket. It counts the datagrams
// it receives so tests can prove WireGuard traffic arrived via that netstack.
type NetBind struct {
	tnet  *netstack.Net
	local netip.Addr
	rx    atomic.Int64

	mu sync.Mutex
	uc *gonet.UDPConn
}

var _ conn.Bind = (*NetBind)(nil)

// NewNetBind returns an unopened bind that will listen on local (an address
// of tnet). Endpoints must be IP literals of the same family.
func NewNetBind(tnet *netstack.Net, local netip.Addr) *NetBind {
	return &NetBind{tnet: tnet, local: local.Unmap()}
}

// Received is the number of datagrams read from the netstack so far.
func (b *NetBind) Received() int64 { return b.rx.Load() }

// Open binds local:port (0 = random) on the netstack.
func (b *NetBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.uc != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	uc, err := b.tnet.ListenUDPAddrPort(netip.AddrPortFrom(b.local, port))
	if err != nil {
		return nil, 0, err
	}
	b.uc = uc
	recv := func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, from, err := uc.ReadFrom(bufs[0])
		if err != nil {
			if b.current() != uc {
				return 0, net.ErrClosed
			}
			return 0, err
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			return 0, errors.New("testrig: unexpected source address type")
		}
		b.rx.Add(1)
		ap := ua.AddrPort()
		sizes[0] = n
		eps[0] = loopEndpoint{netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}
		return 1, nil
	}
	return []conn.ReceiveFunc{recv}, uint16(uc.LocalAddr().(*net.UDPAddr).Port), nil
}

func (b *NetBind) current() *gonet.UDPConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.uc
}

// Close releases the netstack socket, unblocking a pending receive.
func (b *NetBind) Close() error {
	b.mu.Lock()
	uc := b.uc
	b.uc = nil
	b.mu.Unlock()
	if uc == nil {
		return nil
	}
	return uc.Close()
}

// SetMark is a no-op.
func (b *NetBind) SetMark(uint32) error { return nil }

// BatchSize is 1: one datagram per read/write.
func (b *NetBind) BatchSize() int { return 1 }

// ParseEndpoint accepts only IP literal ip:port of the bind's family.
func (b *NetBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	if ap.Addr().Unmap().Is4() != b.local.Is4() {
		return nil, errors.New("testrig: endpoint family differs from bind")
	}
	return loopEndpoint{netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// Send writes each buffer to ep over the netstack.
func (b *NetBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	uc := b.current()
	if uc == nil {
		return net.ErrClosed
	}
	le, ok := ep.(loopEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	dst := net.UDPAddrFromAddrPort(le.ap)
	for _, buf := range bufs {
		if _, err := uc.WriteTo(buf, dst); err != nil {
			return err
		}
	}
	return nil
}
