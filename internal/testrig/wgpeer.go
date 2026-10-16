package testrig

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress/netstack"
)

// DefaultMTU is the tunnel MTU used by the peer and its client config.
const DefaultMTU = 1420

// WGPeer is an in-process WireGuard peer: wireguard-go on its own gVisor
// netstack, listening on 127.0.0.1 UDP (or, WithHostNet, on another peer's
// netstack), serving an HTTP echo on TunnelIP():80
// and a static DNS responder on TunnelIP():53 (EchoHost → TunnelIP()).
type WGPeer struct {
	name     string
	kp       keyPair
	client   keyPair
	tunnelIP netip.Addr
	clientIP netip.Addr
	port     uint16
	host     netip.Addr // valid when hosted on another netstack
	hostBind *NetBind   // nil unless hosted
	mtu      int
	dev      *device.Device
	tnet     *netstack.Net
	ln       net.Listener
	echoDone <-chan struct{}
	dnsConn  net.PacketConn
	dnsDone  <-chan struct{}
	paused   atomic.Bool

	closeOnce sync.Once
}

// EchoHost is the hostname every peer's DNS responder maps to its TunnelIP.
const EchoHost = "echo.rig.invalid"

type peerOpts struct {
	tunnelIP, clientIP netip.Addr
	records            map[string]netip.Addr
	hostNet            *netstack.Net
	hostIP             netip.Addr
}

// PeerOption customizes NewWGPeer.
type PeerOption func(*peerOpts)

// WithTunnelIPs sets the peer's and the client's tunnel addresses
// (defaults 10.64.0.1 and 10.64.0.2).
func WithTunnelIPs(peer, client netip.Addr) PeerOption {
	return func(o *peerOpts) { o.tunnelIP, o.clientIP = peer, client }
}

// WithDNSRecord adds an A record (name → IPv4) to the peer's DNS responder.
func WithDNSRecord(name string, ip netip.Addr) PeerOption {
	return func(o *peerOpts) { o.records[strings.ToLower(name)] = ip }
}

// WithHostNet hosts the peer's WireGuard UDP socket on hostNet at hostIP
// (an address of hostNet, e.g. an outer peer's TunnelIP) instead of on
// loopback, so the peer is reachable only through that outer tunnel
// (WG-in-WG). The client config's MTU becomes DefaultMTU − 80.
func WithHostNet(hostNet *netstack.Net, hostIP netip.Addr) PeerOption {
	return func(o *peerOpts) { o.hostNet, o.hostIP = hostNet, hostIP.Unmap() }
}

// NewWGPeer starts a peer named name and registers Close with t.Cleanup.
func NewWGPeer(t testing.TB, name string, opts ...PeerOption) *WGPeer {
	t.Helper()
	p, err := StartWGPeer(name, opts...)
	if err != nil {
		t.Fatalf("testrig: start WG peer %q: %v", name, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// StartWGPeer is NewWGPeer without testing.TB: the caller owns Close.
func StartWGPeer(name string, opts ...PeerOption) (*WGPeer, error) {
	o := peerOpts{
		tunnelIP: netip.AddrFrom4([4]byte{10, 64, 0, 1}),
		clientIP: netip.AddrFrom4([4]byte{10, 64, 0, 2}),
		records:  map[string]netip.Addr{},
	}
	for _, opt := range opts {
		opt(&o)
	}
	if _, ok := o.records[EchoHost]; !ok {
		o.records[EchoHost] = o.tunnelIP
	}
	return startPeer(name, o)
}

func startPeer(name string, o peerOpts) (*WGPeer, error) {
	p := &WGPeer{name: name, tunnelIP: o.tunnelIP, clientIP: o.clientIP, mtu: DefaultMTU}
	if o.hostNet != nil {
		p.mtu = DefaultMTU - 80
	}
	var err error
	if p.kp, err = newKeyPair(); err != nil {
		return nil, err
	}
	if p.client, err = newKeyPair(); err != nil {
		return nil, err
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{p.tunnelIP}, nil, p.mtu)
	if err != nil {
		return nil, err
	}
	p.tnet = tnet
	var bind conn.Bind = NewLoopbackBind()
	if o.hostNet != nil {
		p.hostBind = NewNetBind(o.hostNet, o.hostIP)
		p.host, bind = o.hostIP, p.hostBind
	}
	bind = pauseBind{Bind: bind, paused: &p.paused}
	p.dev = device.NewDevice(tdev, bind, device.NewLogger(device.LogLevelSilent, ""))
	bits := 32
	if p.clientIP.Is6() {
		bits = 128
	}
	uapi := fmt.Sprintf("private_key=%x\nlisten_port=0\npublic_key=%x\nallowed_ip=%s\n",
		p.kp.priv, p.client.pub, netip.PrefixFrom(p.clientIP, bits))
	if err := p.dev.IpcSet(uapi); err != nil {
		p.dev.Close()
		return nil, err
	}
	if err := p.dev.Up(); err != nil {
		p.dev.Close()
		return nil, err
	}
	if p.port, err = listenPort(p.dev); err != nil {
		p.dev.Close()
		return nil, err
	}
	p.ln, err = tnet.ListenTCPAddrPort(netip.AddrPortFrom(p.tunnelIP, 80))
	if err != nil {
		p.dev.Close()
		return nil, err
	}
	p.echoDone = ServeEcho(p.ln, name)
	if p.dnsConn, err = tnet.ListenUDPAddrPort(netip.AddrPortFrom(p.tunnelIP, 53)); err != nil {
		_ = p.ln.Close()
		p.dev.Close()
		<-p.echoDone
		return nil, err
	}
	p.dnsDone = ServeDNS(p.dnsConn, o.records)
	return p, nil
}

// listenPort reads the bound UDP port back from the device via UAPI.
func listenPort(dev *device.Device) (uint16, error) {
	s, err := dev.IpcGet()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "listen_port="); ok {
			n, err := strconv.ParseUint(v, 10, 16)
			if err == nil && n != 0 {
				return uint16(n), nil
			}
		}
	}
	return 0, fmt.Errorf("listen_port not reported")
}

// Name is the hop name reported by the echo endpoint.
func (p *WGPeer) Name() string { return p.name }

// PublicKey is the peer's base64 public key.
func (p *WGPeer) PublicKey() string { return b64(p.kp.pub) }

// Endpoint is the peer's UDP address: 127.0.0.1:<port>, or hostIP:<port>
// when hosted WithHostNet.
func (p *WGPeer) Endpoint() string {
	host := "127.0.0.1"
	if p.host.IsValid() {
		host = p.host.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(int(p.port)))
}

// HostNetPackets counts WireGuard datagrams the peer received on its host
// netstack (always 0 unless hosted WithHostNet).
func (p *WGPeer) HostNetPackets() int64 {
	if p.hostBind == nil {
		return 0
	}
	return p.hostBind.Received()
}

// TunnelIP is the peer's address inside the tunnel (hosts the echo on :80).
func (p *WGPeer) TunnelIP() netip.Addr { return p.tunnelIP }

// ClientIP is the address assigned to the client in ClientConfig.
func (p *WGPeer) ClientIP() netip.Addr { return p.clientIP }

// Net is the peer-side netstack, for hosting extra listeners in later tests.
func (p *WGPeer) Net() *netstack.Net { return p.tnet }

// ClientConfig returns a WireGuard endpoint that connects to this peer.
func (p *WGPeer) ClientConfig() config.Endpoint {
	bits := 32
	if p.clientIP.Is6() {
		bits = 128
	}
	return config.Endpoint{
		ID:            "testrig-" + p.name,
		Name:          p.name,
		Kind:          config.KindWireGuard,
		PrivateKey:    b64(p.client.priv),
		Addresses:     []string{netip.PrefixFrom(p.clientIP, bits).String()},
		PeerPublicKey: p.PublicKey(),
		Endpoint:      p.Endpoint(),
		DNS:           []string{p.tunnelIP.String()},
		AllowedIPs:    []string{"0.0.0.0/0"},
		MTU:           p.mtu,
	}
}

// Pause drops every WireGuard datagram the peer sends or receives at its
// bind, simulating a dead hop. Keys, ports and sessions are kept, so a
// client can recover after Resume. Idempotent.
func (p *WGPeer) Pause() { p.paused.Store(true) }

// Resume undoes Pause. Idempotent.
func (p *WGPeer) Resume() { p.paused.Store(false) }

// Paused reports whether the peer is dropping packets.
func (p *WGPeer) Paused() bool { return p.paused.Load() }

// pauseBind wraps a bind and, while paused, silently drops inbound and
// outbound datagrams.
type pauseBind struct {
	conn.Bind
	paused *atomic.Bool
}

func (b pauseBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actual, err := b.Bind.Open(port)
	if err != nil {
		return nil, 0, err
	}
	wrapped := make([]conn.ReceiveFunc, len(fns))
	for i, f := range fns {
		wrapped[i] = func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
			for {
				n, err := f(bufs, sizes, eps)
				if err != nil || !b.paused.Load() {
					return n, err
				}
			}
		}
	}
	return wrapped, actual, nil
}

func (b pauseBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	if b.paused.Load() {
		return nil
	}
	return b.Bind.Send(bufs, ep)
}

// Close stops the echo and DNS servers, the device, its netstack, and the UDP socket.
// It is idempotent and waits for the echo goroutine to exit.
func (p *WGPeer) Close() error {
	p.closeOnce.Do(func() {
		_ = p.ln.Close()
		_ = p.dnsConn.Close()
		p.dev.Close()
		for _, done := range []<-chan struct{}{p.echoDone, p.dnsDone} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
	})
	return nil
}
