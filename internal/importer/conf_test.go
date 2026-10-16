package importer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

func TestParseConf(t *testing.T) {
	o := validOpts(t)
	want := config.Endpoint{
		Kind:          config.KindWireGuard,
		PrivateKey:    o.PrivateKey,
		Addresses:     []string{"10.64.0.2/32", "fc00:bbbb:bbbb:bb01::2/128"},
		DNS:           []string{"10.64.0.1"},
		PeerPublicKey: o.PublicKey,
		PresharedKey:  o.PresharedKey,
		Endpoint:      "198.51.100.10:51820",
		AllowedIPs:    []string{"0.0.0.0/0", "::/0"},
		MTU:           DefaultMTU,
	}
	withMTU := want
	withMTU.MTU = 1280

	cases := []struct {
		name string
		text string
		want config.Endpoint
	}{
		{"valid full conf", confText(t, o), want},
		{"comments and blank lines ignored",
			"# Mullvad config\n\n" + strings.Replace(confText(t, o), "[Peer]\n", "[Peer]\n# peer comment\n\n   \n", 1) + "\n# trailing\n",
			want},
		{"keys and sections case-insensitive",
			strings.NewReplacer("[Interface]", "[interface]", "PrivateKey", "privatekey", "PublicKey", "PUBLICKEY",
				"Endpoint", "endpoint", "[Peer]", "[PEER]").Replace(confText(t, o)),
			want},
		{"CRLF line endings", strings.ReplaceAll(confText(t, o), "\n", "\r\n"), want},
		{"explicit MTU kept", confText(t, func() confOpts { c := o; c.MTU = "1280"; return c }()), withMTU},
		{"unknown keys ignored", strings.Replace(confText(t, o), "[Peer]\n", "[Peer]\nPersistentKeepalive = 25\n", 1), want},
		{"leading UTF-8 BOM stripped", "\ufeff" + confText(t, o), want},
		{"Mullvad-shaped without PresharedKey",
			confText(t, func() confOpts { c := o; c.PresharedKey = ""; return c }()),
			func() config.Endpoint { w := want; w.PresharedKey = ""; return w }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConf([]byte(tc.text))
			if err != nil {
				t.Fatalf("ParseConf: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseConf =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
	t.Run("no MTU defaults to 1420", func(t *testing.T) {
		got, err := ParseConf([]byte(confText(t, o)))
		if err != nil || got.MTU != 1420 {
			t.Fatalf("MTU = %d, err = %v; want 1420", got.MTU, err)
		}
	})
	t.Run("result passes config.ValidateEndpoint", func(t *testing.T) {
		got, err := ParseConf([]byte(confText(t, o)))
		if err != nil {
			t.Fatal(err)
		}
		if ve := config.ValidateEndpoint("e", got); ve != nil {
			t.Fatalf("ValidateEndpoint = %v", ve)
		}
	})
}

func TestParseConfMalformed(t *testing.T) {
	o := validOpts(t)
	mod := func(f func(*confOpts)) string { c := o; f(&c); return confText(t, c) }
	full := confText(t, o)
	peerOnly := full[strings.Index(full, "[Peer]"):]
	ifaceOnly := full[:strings.Index(full, "[Peer]")]
	cases := []struct {
		name, text, reason string
	}{
		{"empty file", "", "empty"},
		{"whitespace only", "\n  \n# just a comment\n", "empty"},
		{"no Interface section", peerOnly, "[Interface]"},
		{"zero Peer sections", ifaceOnly, "[Peer]"},
		{"two Peer sections", full + "\n" + peerOnly, "[Peer]"},
		{"two Interface sections", ifaceOnly + full, "[Interface]"},
		{"missing PrivateKey", mod(func(c *confOpts) { c.PrivateKey = "" }), "PrivateKey"},
		{"bad base64 private key", mod(func(c *confOpts) { c.PrivateKey = "not*base64" }), "PrivateKey"},
		{"short private key", mod(func(c *confOpts) { c.PrivateKey = "AAAA" }), "PrivateKey"},
		{"bad base64 public key", mod(func(c *confOpts) { c.PublicKey = "%%%" }), "PublicKey"},
		{"bad base64 preshared key", mod(func(c *confOpts) { c.PresharedKey = "%%%" }), "PresharedKey"},
		{"missing PublicKey", mod(func(c *confOpts) { c.PublicKey = "" }), "PublicKey"},
		{"missing Endpoint", mod(func(c *confOpts) { c.Endpoint = "" }), "Endpoint"},
		{"Endpoint without port", mod(func(c *confOpts) { c.Endpoint = "198.51.100.10" }), "Endpoint"},
		{"missing Address", mod(func(c *confOpts) { c.Address = "" }), "Address"},
		{"bad Address", mod(func(c *confOpts) { c.Address = "10.64.0.2/32, nope" }), "Address"},
		{"missing AllowedIPs", mod(func(c *confOpts) { c.AllowedIPs = "" }), "AllowedIPs"},
		{"bad AllowedIPs", mod(func(c *confOpts) { c.AllowedIPs = "0.0.0.0/99" }), "AllowedIPs"},
		{"bad MTU", mod(func(c *confOpts) { c.MTU = "big" }), "MTU"},
		{"MTU out of range", mod(func(c *confOpts) { c.MTU = "70000" }), "MTU"},
		{"key = value outside a section", "PrivateKey = " + o.PrivateKey + "\n" + full, "outside"},
		{"line without =", strings.Replace(full, "[Peer]\n", "[Peer]\ngarbage line\n", 1), "line"},
		{"unknown section", full + "\n[Foo]\nA = b\n", "section"},
		{"binary garbage", "\x00\x01\x02\xff\xfePK\x03\x04\x00\x00junk", "text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConf([]byte(tc.text))
			if err == nil {
				t.Fatal("ParseConf: want error, got nil")
			}
			msg := err.Error()
			if !strings.Contains(strings.ToLower(msg), strings.ToLower(tc.reason)) {
				t.Errorf("error %q does not mention %q", msg, tc.reason)
			}
			for _, secret := range []string{o.PrivateKey, o.PresharedKey, "not*base64"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error leaks key material: %q", msg)
				}
			}
		})
	}
}
