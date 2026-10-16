package testrig

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/device"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress/netstack"
)

// startClient builds a client wireguard-go device on its own netstack from cfg.
func startClient(t *testing.T, cfg config.Endpoint) (*device.Device, *netstack.Net) {
	t.Helper()
	pfx, err := netip.ParsePrefix(cfg.Addresses[0])
	if err != nil {
		t.Fatalf("parse address: %v", err)
	}
	var dns []netip.Addr
	for _, d := range cfg.DNS {
		dns = append(dns, netip.MustParseAddr(d))
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{pfx.Addr()}, dns, cfg.MTU)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	dev := device.NewDevice(tdev, NewLoopbackBind(), device.NewLogger(device.LogLevelSilent, ""))
	priv, err := KeyHex(cfg.PrivateKey)
	if err != nil {
		t.Fatalf("private key: %v", err)
	}
	pub, err := KeyHex(cfg.PeerPublicKey)
	if err != nil {
		t.Fatalf("peer key: %v", err)
	}
	uapi := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\n", priv, pub, cfg.Endpoint)
	for _, a := range cfg.AllowedIPs {
		uapi += "allowed_ip=" + a + "\n"
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		t.Fatalf("IpcSet: %v", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		t.Fatalf("Up: %v", err)
	}
	return dev, tnet
}

func getEcho(t *testing.T, tnet *netstack.Net, ip netip.Addr) map[string]string {
	t.Helper()
	tr := &http.Transport{DialContext: tnet.DialContext, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := c.Get("http://" + ip.String() + "/")
	if err != nil {
		t.Fatalf("GET through tunnel: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestWGPeerEcho(t *testing.T) {
	p := NewWGPeer(t, "hop-a")
	if !strings.HasPrefix(p.Endpoint(), "127.0.0.1:") {
		t.Fatalf("Endpoint = %q, want 127.0.0.1:<port>", p.Endpoint())
	}
	cfg := p.ClientConfig()
	if cfg.Kind != config.KindWireGuard || cfg.MTU != 1420 || cfg.AllowedIPs[0] != "0.0.0.0/0" {
		t.Fatalf("unexpected client config shape: kind=%q mtu=%d allowed=%v", cfg.Kind, cfg.MTU, cfg.AllowedIPs)
	}
	if cfg.PeerPublicKey != p.PublicKey() {
		t.Fatalf("client config peer key does not match peer PublicKey()")
	}
	dev, tnet := startClient(t, cfg)
	defer dev.Close()

	got := getEcho(t, tnet, p.TunnelIP())
	want := strings.SplitN(cfg.Addresses[0], "/", 2)[0]
	if got["ip"] != want || got["hop"] != "hop-a" {
		t.Fatalf("echo = %v, want ip=%s hop=hop-a", got, want)
	}
}

func TestWGPeerCloseReleasesResources(t *testing.T) {
	before := runtime.NumGoroutine()

	p := NewWGPeer(t, "hop-close")
	dev, tnet := startClient(t, p.ClientConfig())
	_ = getEcho(t, tnet, p.TunnelIP())
	dev.Close()
	ep := p.Endpoint()
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	addr, err := net.ResolveUDPAddr("udp4", ep)
	if err != nil {
		t.Fatal(err)
	}
	uc, err := net.ListenUDP("udp4", addr)
	if err != nil {
		t.Fatalf("peer UDP port not released: %v", err)
	}
	uc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		n := runtime.NumGoroutine()
		if n <= before+2 {
			return
		}
		select {
		case <-ctx.Done():
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: before=%d after=%d\n%s", before, n, buf[:runtime.Stack(buf, true)])
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestWGPeerDNS(t *testing.T) {
	extra := netip.AddrFrom4([4]byte{10, 64, 0, 9})
	p := NewWGPeer(t, "hop-dns", WithDNSRecord("Other.rig.invalid", extra))
	cfg := p.ClientConfig()
	if len(cfg.DNS) != 1 || cfg.DNS[0] != p.TunnelIP().String() {
		t.Fatalf("client DNS = %v, want [%s]", cfg.DNS, p.TunnelIP())
	}
	dev, tnet := startClient(t, cfg)
	defer dev.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for host, want := range map[string]string{EchoHost: p.TunnelIP().String(), "other.rig.invalid": extra.String()} {
		got, err := tnet.LookupContextHost(ctx, host)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Fatalf("lookup %s = %v, %v; want [%s]", host, got, err, want)
		}
	}
	if got, err := tnet.LookupContextHost(ctx, "missing.rig.invalid"); err == nil {
		t.Fatalf("lookup of unknown name = %v, want NXDOMAIN error", got)
	}
}

// tryEcho GETs the echo through tnet with a short timeout.
func tryEcho(tnet *netstack.Net, ip netip.Addr, timeout time.Duration) error {
	tr := &http.Transport{DialContext: tnet.DialContext, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: timeout}
	resp, err := c.Get("http://" + ip.String() + "/")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestWGPeerPause(t *testing.T) {
	p := NewWGPeer(t, "hop-pause")
	dev, tnet := startClient(t, p.ClientConfig())
	defer dev.Close()
	_ = getEcho(t, tnet, p.TunnelIP())

	p.Pause()
	if !p.Paused() {
		t.Fatal("Paused() = false after Pause")
	}
	if err := tryEcho(tnet, p.TunnelIP(), 500*time.Millisecond); err == nil {
		t.Fatal("echo succeeded while peer paused, want packets dropped")
	}
	p.Pause() // idempotent

	p.Resume()
	if p.Paused() {
		t.Fatal("Paused() = true after Resume")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := tryEcho(tnet, p.TunnelIP(), 2*time.Second)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("echo after Resume: %v", err)
		}
	}
}
