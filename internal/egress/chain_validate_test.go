package egress

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/importer"
)

// fakeWGKey returns a freshly generated random base64 32-byte key (never a real key).
func fakeWGKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// wgConf parses a WireGuard .conf with fresh fake keys through the importer,
// so the endpoint has the exact shape a Mullvad upload produces.
func wgConf(t *testing.T, id, address, endpoint, allowed string) config.Endpoint {
	t.Helper()
	text := "[Interface]\nPrivateKey = " + fakeWGKey(t) + "\nAddress = " + address +
		"\nDNS = 10.64.0.1\n\n[Peer]\nPublicKey = " + fakeWGKey(t) +
		"\nEndpoint = " + endpoint + "\nAllowedIPs = " + allowed + "\n"
	ep, err := importer.ParseConf([]byte(text))
	if err != nil {
		t.Fatalf("ParseConf(%s): %v", id, err)
	}
	ep.ID, ep.Name = id, id
	return ep
}

const dualStackAddr = "10.64.0.2/32, fc00:bbbb:bbbb:bb01::2/128"

// testBuildValidateNested is TestBuildValidate/nested WG: the endpoint rules
// for an inner WireGuard hop (distinct endpoint, inside the outer allowed_ips). Failing rows must open no OS UDP socket.
func testBuildValidateNested(t *testing.T) {
	mullvad := "0.0.0.0/0, ::/0"
	for _, tc := range []struct {
		name    string
		eps     []config.Endpoint
		wantErr string // "" = must plan
	}{
		{"equal to outer", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", mullvad),
			wgConf(t, "wgB", dualStackAddr, "198.51.100.10:51820", mullvad),
		}, "route[1].endpoint"},
		{"outside outer allowed_ips", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", "10.0.0.0/8, 192.168.0.0/16"),
			wgConf(t, "wgB", dualStackAddr, "198.51.100.20:51820", mullvad),
		}, "route[1].endpoint"},
		{"covered by outer allowed_ips", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", "10.0.0.0/8, 198.51.100.0/24"),
			wgConf(t, "wgB", dualStackAddr, "198.51.100.20:51820", mullvad),
		}, ""},
		{"third equal to first", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", mullvad),
			wgConf(t, "wgB", dualStackAddr, "198.51.100.20:51820", mullvad),
			wgConf(t, "wgC", dualStackAddr, "198.51.100.10:51820", mullvad),
		}, "route[2].endpoint"},
		{"v6 endpoint, v4-only outer allowed_ips", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", "0.0.0.0/0"),
			wgConf(t, "wgB", dualStackAddr, "[2001:db8::20]:51820", mullvad),
		}, "route[1].endpoint"},
		{"mullvad outer, v4 inner", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", mullvad),
			wgConf(t, "wgB", dualStackAddr, "198.51.100.20:51820", mullvad),
		}, ""},
		{"mullvad outer, v6 inner", []config.Endpoint{
			wgConf(t, "wgA", dualStackAddr, "198.51.100.10:51820", mullvad),
			wgConf(t, "wgB", dualStackAddr, "[2001:db8::20]:51820", mullvad),
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := make([]string, len(tc.eps))
			for i, e := range tc.eps {
				route[i] = e.ID
			}
			if tc.wantErr == "" {
				// planRoute only: the outer endpoints are TEST-NET addresses, so
				// bringing them up would send UDP off the box (hermetic tests).
				if _, err := planRoute(route, tc.eps); err != nil {
					t.Fatalf("planRoute(%v): %v, want ok", route, err)
				}
				return
			}
			binds := recordBinds(t)
			ch, err := Build(route, tc.eps)
			if err == nil {
				_ = ch.Close()
				t.Fatalf("Build(%v) succeeded, want error naming %s", route, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Build(%v) error = %q, want it to name %s", route, err, tc.wantErr)
			}
			if len(*binds) != 0 {
				t.Fatalf("OS binds = %d after rejected build, want 0 (no tunnel started)", len(*binds))
			}
		})
	}
}
