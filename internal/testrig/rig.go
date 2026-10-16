package testrig

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"github.com/M-Wham/sockswebui/internal/config"
)

// Endpoint ids seeded by the --dev-testrig binary (build tag testrig). They are
// fixed and use the server id shape ^ep-[0-9a-f]{16}$ ("726967" is "rig" in hex)
// so a dev-rig config survives export → import, which rejects ids of any other
// shape.
const (
	DevRigWG    = "ep-7269670000000001" // WireGuard tunnel to the rig peer
	DevRigWG2   = "ep-7269670000000002" // same relay, its own client key (for a second concurrent slot)
	DevRigWG3   = "ep-7269670000000003" // third client key, for dev slot 3
	DevRigWG4   = "ep-7269670000000004" // fourth client key, for dev slot 4
	DevRigSOCKS = "ep-72696700000000ff" // SOCKS5 upstream on the peer's netstack, reachable only through rigWG*
)

// Human-visible names of the seeded endpoints (UI and Playwright find hops by name).
const (
	DevRigWGName    = "rigWG"
	DevRigWG2Name   = "rigWG2"
	DevRigWG3Name   = "rigWG3"
	DevRigWG4Name   = "rigWG4"
	DevRigSOCKSName = "rigSOCKS"
)

// DevRigSOCKSUserPrefix is the fixed username prefix the rig SOCKS5 upstream
// accepts (any password), so a `{session}` template such as
// "rig-{session}" works against it. Clients sending no credentials (the seeded
// rigSOCKS endpoint) are accepted too.
const DevRigSOCKSUserPrefix = "rig-"

// DevRigHealthURL is the rig echo: every rig peer's DNS maps EchoHost to its echo.
const DevRigHealthURL = "http://" + EchoHost + "/"

const devRigSOCKSPort = 1080

// DevRig is a self-contained testrig for manual and Playwright runs of the
// binary: one WireGuard peer (echo + DNS inside the tunnel) and a fake SOCKS5
// upstream hosted on that peer's netstack. Keys are generated at start and
// live only in memory.
type DevRig struct {
	peer  *WGPeer
	socks *SOCKS5Upstream
	eps   []config.Endpoint

	closeOnce sync.Once
}

// StartDevRig starts the rig. ctx bounds startup only; the caller owns Close.
func StartDevRig(ctx context.Context) (*DevRig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	peer, err := StartWGPeer(DevRigWGName)
	if err != nil {
		return nil, fmt.Errorf("testrig: dev rig peer: %w", err)
	}
	r := &DevRig{peer: peer}
	if err := r.init(ctx); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func (r *DevRig) init(ctx context.Context) error {
	p := r.peer
	wg := p.ClientConfig()
	wg.ID, wg.Name = DevRigWG, DevRigWGName
	wgs := []config.Endpoint{wg}
	extra := [][2]string{{DevRigWG2, DevRigWG2Name}, {DevRigWG3, DevRigWG3Name}, {DevRigWG4, DevRigWG4Name}}
	for i, idName := range extra {
		ep, err := p.AddClientKey(netip.AddrFrom4([4]byte{10, 64, 0, byte(3 + i)}))
		if err != nil {
			return fmt.Errorf("testrig: dev rig: %w", err)
		}
		ep.ID, ep.Name = idName[0], idName[1]
		wgs = append(wgs, ep)
	}
	ln, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), devRigSOCKSPort))
	if err != nil {
		return fmt.Errorf("testrig: dev rig socks listen: %w", err)
	}
	r.socks = NewSOCKS5Upstream(ln, UpstreamConfig{
		Name: DevRigSOCKSName, Dial: p.Net().DialContext,
		AcceptUser:  func(u string) bool { return strings.HasPrefix(u, DevRigSOCKSUserPrefix) },
		AllowNoAuth: true,
		Hosts:       map[string]string{EchoHost: p.TunnelIP().String()},
	})
	r.eps = append(wgs, config.Endpoint{
		ID: DevRigSOCKS, Name: DevRigSOCKSName, Kind: config.KindSOCKS5,
		Host: p.TunnelIP().String(), Port: devRigSOCKSPort,
	})
	return ctx.Err()
}

// Endpoints returns the rig's endpoint configs (rigWG..rigWG4, rigSOCKS).
func (r *DevRig) Endpoints() []config.Endpoint {
	return append([]config.Endpoint(nil), r.eps...)
}

// Peer is the rig's WireGuard peer (Pause/Resume simulate a dead hop).
func (r *DevRig) Peer() *WGPeer { return r.peer }

// Close stops the upstream and the peer. Idempotent.
func (r *DevRig) Close() error {
	r.closeOnce.Do(func() {
		if r.socks != nil {
			_ = r.socks.Close()
		}
		_ = r.peer.Close()
	})
	return nil
}
