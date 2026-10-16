package testrig

import (
	"errors"
	"net"
	"net/netip"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// LoopbackBind is a wireguard-go conn.Bind that only ever binds 127.0.0.1
// (IPv4 loopback), so test tunnels never touch a real interface.
type LoopbackBind struct {
	mu sync.Mutex
	uc *net.UDPConn
}

// NewLoopbackBind returns an unopened loopback-only bind.
func NewLoopbackBind() *LoopbackBind { return &LoopbackBind{} }

type loopEndpoint struct{ ap netip.AddrPort }

func (e loopEndpoint) ClearSrc()           {}
func (e loopEndpoint) SrcToString() string { return "" }
func (e loopEndpoint) DstToString() string { return e.ap.String() }
func (e loopEndpoint) DstToBytes() []byte  { b, _ := e.ap.MarshalBinary(); return b }
func (e loopEndpoint) DstIP() netip.Addr   { return e.ap.Addr() }
func (e loopEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// Open binds 127.0.0.1:port (0 = random).
func (b *LoopbackBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.uc != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		return nil, 0, err
	}
	b.uc = uc
	recv := func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, ap, err := uc.ReadFromUDPAddrPort(bufs[0])
		if err != nil {
			return 0, err
		}
		sizes[0] = n
		eps[0] = loopEndpoint{netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}
		return 1, nil
	}
	return []conn.ReceiveFunc{recv}, uint16(uc.LocalAddr().(*net.UDPAddr).Port), nil
}

// Close releases the UDP socket.
func (b *LoopbackBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.uc == nil {
		return nil
	}
	err := b.uc.Close()
	b.uc = nil
	return err
}

// SetMark is a no-op.
func (b *LoopbackBind) SetMark(uint32) error { return nil }

// BatchSize is 1: one datagram per read/write.
func (b *LoopbackBind) BatchSize() int { return 1 }

// ParseEndpoint accepts only loopback IPv4 endpoints.
func (b *LoopbackBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	if !ap.Addr().Unmap().IsLoopback() || !ap.Addr().Unmap().Is4() {
		return nil, errors.New("testrig: endpoint must be 127.0.0.0/8")
	}
	return loopEndpoint{netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// Send writes each buffer to ep.
func (b *LoopbackBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.mu.Lock()
	uc := b.uc
	b.mu.Unlock()
	if uc == nil {
		return net.ErrClosed
	}
	le, ok := ep.(loopEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	for _, buf := range bufs {
		if _, err := uc.WriteToUDPAddrPort(buf, le.ap); err != nil {
			return err
		}
	}
	return nil
}
