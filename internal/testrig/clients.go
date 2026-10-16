package testrig

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// AddClient registers one more client key on the peer, with tunnel address
// clientIP, and returns its endpoint config. WireGuard binds a session to a
// key, so two tunnels that must run at the same time against the same peer
// (e.g. two slots whose routes both start at this relay) each need their own
// client key, exactly like two Mullvad devices on one relay.
func (p *WGPeer) AddClient(t testing.TB, clientIP netip.Addr) config.Endpoint {
	t.Helper()
	ep, err := p.AddClientKey(clientIP)
	if err != nil {
		t.Fatalf("testrig: %v", err)
	}
	return ep
}

// AddClientKey is AddClient without testing.TB.
func (p *WGPeer) AddClientKey(clientIP netip.Addr) (config.Endpoint, error) {
	kp, err := newKeyPair()
	if err != nil {
		return config.Endpoint{}, fmt.Errorf("client key: %w", err)
	}
	bits := 32
	if clientIP.Is6() {
		bits = 128
	}
	uapi := fmt.Sprintf("public_key=%x\nallowed_ip=%s\n", kp.pub, netip.PrefixFrom(clientIP, bits))
	if err := p.dev.IpcSet(uapi); err != nil {
		return config.Endpoint{}, fmt.Errorf("add client to %s: %w", p.name, err)
	}
	ep := p.ClientConfig()
	ep.PrivateKey = b64(kp.priv)
	ep.Addresses = []string{netip.PrefixFrom(clientIP, bits).String()}
	return ep, nil
}
