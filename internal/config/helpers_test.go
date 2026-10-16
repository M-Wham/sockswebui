package config

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// fakeKey returns a freshly generated, random base64 32-byte key. It is never a real key.
func fakeKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func wgEndpoint(t *testing.T, id string) Endpoint {
	t.Helper()
	return Endpoint{
		ID:            id,
		Name:          id,
		Kind:          KindWireGuard,
		PrivateKey:    fakeKey(t),
		Addresses:     []string{"10.64.0.2/32"},
		DNS:           []string{"10.64.0.1"},
		PeerPublicKey: fakeKey(t),
		Endpoint:      "198.51.100.10:51820",
		AllowedIPs:    []string{"0.0.0.0/0", "::/0"},
	}
}

func proxyEndpoint(id, kind string) Endpoint {
	return Endpoint{ID: id, Name: id, Kind: kind, Host: "proxy.example.test", Port: 1080}
}

func testSlot(id int, port int, route ...string) Slot {
	return Slot{
		ID:         id,
		Name:       "slot",
		Enabled:    true,
		BindAddr:   "0.0.0.0",
		Port:       port,
		Route:      route,
		Killswitch: KillswitchBlock,
		Health:     DefaultHealth(),
	}
}

// baseConfig returns a valid config with WG-SE, WG-US, GatewayProxy (socks5), OtherProxy (http)
// and one slot on port 1080 routed through WG-SE.
func baseConfig(t *testing.T) Config {
	t.Helper()
	c := Default()
	c.Endpoints = []Endpoint{
		wgEndpoint(t, "WG-SE"),
		wgEndpoint(t, "WG-US"),
		proxyEndpoint("GatewayProxy", KindSOCKS5),
		proxyEndpoint("OtherProxy", KindHTTP),
	}
	c.Slots = []Slot{testSlot(1, 1080, "WG-SE")}
	return c
}

func asValidationErrors(t *testing.T, err error) ValidationErrors {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("error type = %T, want ValidationErrors", err)
	}
	if len(ve) == 0 {
		t.Fatal("empty ValidationErrors returned as error")
	}
	return ve
}

func hasField(ve ValidationErrors, field string) bool {
	for _, fe := range ve {
		if fe.Field == field {
			return true
		}
	}
	return false
}
