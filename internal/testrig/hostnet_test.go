package testrig

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/device"

	"github.com/M-Wham/sockswebui/internal/egress/netstack"
)

// TestWGPeerHostNet hosts peer B's UDP socket on peer A's netstack: B is
// reachable only through A's tunnel, never on loopback.
func TestWGPeerHostNet(t *testing.T) {
	a := NewWGPeer(t, "hop-a")
	b := NewWGPeer(t, "hop-b",
		WithTunnelIPs(netip.MustParseAddr("10.65.0.1"), netip.MustParseAddr("10.65.0.2")),
		WithHostNet(a.Net(), a.TunnelIP()))
	if !strings.HasPrefix(b.Endpoint(), a.TunnelIP().String()+":") {
		t.Fatalf("hosted Endpoint = %q, want %s:<port>", b.Endpoint(), a.TunnelIP())
	}

	devA, netA := startClient(t, a.ClientConfig())
	defer devA.Close()

	cfgB := b.ClientConfig()
	if cfgB.MTU != DefaultMTU-80 {
		t.Fatalf("hosted client MTU = %d, want %d (outer − 80)", cfgB.MTU, DefaultMTU-80)
	}
	pfx := netip.MustParsePrefix(cfgB.Addresses[0])
	tdev, netB, err := netstack.CreateNetTUN([]netip.Addr{pfx.Addr()}, nil, cfgB.MTU)
	if err != nil {
		t.Fatal(err)
	}
	bind := NewNetBind(netA, a.ClientIP())
	devB := device.NewDevice(tdev, bind, device.NewLogger(device.LogLevelSilent, ""))
	defer devB.Close()
	priv, _ := KeyHex(cfgB.PrivateKey)
	pub, _ := KeyHex(cfgB.PeerPublicKey)
	uapi := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\n", priv, pub, cfgB.Endpoint)
	if err := devB.IpcSet(uapi); err != nil {
		t.Fatal(err)
	}
	if err := devB.Up(); err != nil {
		t.Fatal(err)
	}

	got := getEcho(t, netB, b.TunnelIP())
	if got["hop"] != "hop-b" || got["ip"] != pfx.Addr().String() {
		t.Fatalf("echo = %v, want hop=hop-b ip=%s", got, pfx.Addr())
	}
	if b.HostNetPackets() == 0 {
		t.Fatal("hosted peer counted 0 datagrams on the host netstack")
	}
	if bind.Received() == 0 {
		t.Fatal("client NetBind counted 0 received datagrams")
	}
}
