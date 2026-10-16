package importer

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// fakeKey returns a freshly generated random base64 32-byte key. It is never a real key.
func fakeKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// confOpts describes a WireGuard .conf to build at test time. Empty fields are omitted.
type confOpts struct {
	PrivateKey   string
	Address      string
	DNS          string
	MTU          string
	PublicKey    string
	PresharedKey string
	Endpoint     string
	AllowedIPs   string
}

// validOpts returns options for a complete Mullvad-style config with fresh fake keys.
func validOpts(t *testing.T) confOpts {
	t.Helper()
	return confOpts{
		PrivateKey:   fakeKey(t),
		Address:      "10.64.0.2/32, fc00:bbbb:bbbb:bb01::2/128",
		DNS:          "10.64.0.1",
		PublicKey:    fakeKey(t),
		PresharedKey: fakeKey(t),
		Endpoint:     "198.51.100.10:51820",
		AllowedIPs:   "0.0.0.0/0, ::/0",
	}
}

// confText renders o as .conf text with one [Interface] and one [Peer] section.
func confText(t *testing.T, o confOpts) string {
	t.Helper()
	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			b.WriteString(k + " = " + v + "\n")
		}
	}
	b.WriteString("[Interface]\n")
	line("PrivateKey", o.PrivateKey)
	line("Address", o.Address)
	line("DNS", o.DNS)
	line("MTU", o.MTU)
	b.WriteString("\n[Peer]\n")
	line("PublicKey", o.PublicKey)
	line("PresharedKey", o.PresharedKey)
	line("AllowedIPs", o.AllowedIPs)
	line("Endpoint", o.Endpoint)
	return b.String()
}
