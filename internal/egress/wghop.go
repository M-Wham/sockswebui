package egress

import (
	"fmt"
	"net/netip"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress/netstack"
)

// defaultWGMTU is the tunnel MTU when the endpoint does not set one.
const defaultWGMTU = 1420

// newOSBind creates the real OS UDP socket of the FIRST WireGuard hop — the
// chain's only OS socket for WireGuard traffic. Tests wrap it to observe it.
var newOSBind = conn.NewDefaultBind

// wgSpec is a parsed, validated WireGuard hop. ipc holds secrets.
type wgSpec struct {
	id       string
	ipc      string
	endpoint netip.AddrPort
	addrs    []netip.Addr
	allowed  []netip.Prefix // masked allowed_ips; a nested hop's endpoint must fall inside
	dns      []netip.Addr
	mtu      int // configured (1420 if unset); Build applies the nesting rule
}

// parseWG validates every WireGuard field needed to bring a hop up, without
// opening anything. The endpoint must be an IP literal: resolving it would
// need the device resolver (fail closed). Errors never contain key material.
func parseWG(path string, ep config.Endpoint) (wgSpec, error) {
	s := wgSpec{id: ep.ID, mtu: ep.MTU}
	if s.mtu == 0 {
		s.mtu = defaultWGMTU
	}
	if ep.PrivateKey == "" {
		return s, fmt.Errorf("egress: %s.private_key: is required to bring the tunnel up", path)
	}
	endpoint, err := netip.ParseAddrPort(ep.Endpoint)
	if err != nil || endpoint.Addr().Zone() != "" {
		return s, fmt.Errorf("egress: %s.endpoint: must be an IP literal ip:port; "+
			"hostnames are not resolved (no device DNS)", path)
	}
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	s.endpoint = endpoint
	if len(ep.Addresses) == 0 {
		return s, fmt.Errorf("egress: %s.addresses: at least one tunnel address is required", path)
	}
	for i, a := range ep.Addresses {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			return s, fmt.Errorf("egress: %s.addresses[%d]: must be an address prefix like 10.64.0.2/32", path, i)
		}
		s.addrs = append(s.addrs, p.Addr())
	}
	if len(ep.AllowedIPs) == 0 {
		return s, fmt.Errorf("egress: %s.allowed_ips: at least one prefix is required", path)
	}
	allowed := make([]netip.Prefix, 0, len(ep.AllowedIPs))
	for i, a := range ep.AllowedIPs {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			return s, fmt.Errorf("egress: %s.allowed_ips[%d]: must be a prefix like 0.0.0.0/0", path, i)
		}
		allowed = append(allowed, p.Masked())
	}
	s.allowed = allowed
	for i, d := range ep.DNS {
		ip, err := netip.ParseAddr(d)
		if err != nil || ip.Zone() != "" {
			return s, fmt.Errorf("egress: %s.dns[%d]: must be an IP address", path, i)
		}
		s.dns = append(s.dns, ip.Unmap())
	}
	if s.ipc, err = wgIPC(path, ep, endpoint, allowed); err != nil {
		return s, fmt.Errorf("egress: %w", err)
	}
	return s, nil
}

// wgTunnel is one running WireGuard hop: a wireguard-go device on its own
// gVisor netstack. Closing the device closes the netstack and the bind.
type wgTunnel struct {
	id    string
	dev   *device.Device
	tnet  *netstack.Net
	addrs []netip.Addr
	dns   []netip.Addr
	mtu   int // the netstack device's actual MTU
}

// onTunnelClose, when set by tests, observes tunnel teardown order.
var onTunnelClose func(id string)

// bindAddr picks the tunnel address a nested hop's netstack bind listens on:
// the first one of the same family as the next hop's endpoint.
func bindAddr(addrs []netip.Addr, next netip.AddrPort) (netip.Addr, bool) {
	for _, a := range addrs {
		if a.Is4() == next.Addr().Is4() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// startWGFn is how Build brings a WireGuard hop up. Unexported fault seam:
// only package egress tests replace it (a dead peer never fails startWG).
var startWGFn = startWG

// startWG brings the hop up on bind. On error everything it opened is closed.
func startWG(s wgSpec, bind conn.Bind) (*wgTunnel, error) {
	tdev, tnet, err := netstack.CreateNetTUN(s.addrs, s.dns, s.mtu)
	if err != nil {
		_ = bind.Close()
		return nil, fmt.Errorf("egress: WireGuard hop %q: netstack: %w", s.id, err)
	}
	dev := device.NewDevice(tdev, bind, device.NewLogger(device.LogLevelSilent, ""))
	if err := dev.IpcSet(s.ipc); err != nil {
		dev.Close()
		return nil, fmt.Errorf("egress: WireGuard hop %q: configuration rejected", s.id)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("egress: WireGuard hop %q: up: %w", s.id, err)
	}
	mtu, err := tdev.MTU()
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("egress: WireGuard hop %q: mtu: %w", s.id, err)
	}
	return &wgTunnel{id: s.id, dev: dev, tnet: tnet, addrs: s.addrs, dns: s.dns, mtu: mtu}, nil
}

// close tears the device, its netstack and its bind down; it blocks until
// wireguard-go's workers have stopped.
func (t *wgTunnel) close() {
	if onTunnelClose != nil {
		onTunnelClose(t.id)
	}
	t.dev.Close()
}
