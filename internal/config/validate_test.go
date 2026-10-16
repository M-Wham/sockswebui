package config

import (
	"fmt"
	"strings"
	"testing"
)

type validateCase struct {
	name      string
	mutate    func(t *testing.T, c *Config)
	wantField string // empty = valid
}

func validateCases() []validateCase {
	return []validateCase{
		{name: "base config valid", mutate: func(*testing.T, *Config) {}},
		{name: "default config valid", mutate: func(_ *testing.T, c *Config) { *c = Default() }},
		{name: "four slots valid", mutate: func(_ *testing.T, c *Config) {
			c.Slots = []Slot{testSlot(1, 1081), testSlot(2, 1082), testSlot(3, 1083), testSlot(4, 1084)}
		}},
		{name: "five slots rejected", wantField: "slots", mutate: func(_ *testing.T, c *Config) {
			c.Slots = []Slot{testSlot(1, 1081), testSlot(2, 1082), testSlot(3, 1083), testSlot(4, 1084), testSlot(4, 1085)}
		}},
		{name: "slot id 0", wantField: "slots[0].id", mutate: func(_ *testing.T, c *Config) { c.Slots[0].ID = 0 }},
		{name: "slot id 5", wantField: "slots[0].id", mutate: func(_ *testing.T, c *Config) { c.Slots[0].ID = 5 }},
		{name: "duplicate slot id", wantField: "slots[1].id", mutate: func(_ *testing.T, c *Config) {
			c.Slots = append(c.Slots, testSlot(1, 1090))
		}},
		{name: "port clash same bind", wantField: "slots[1].port", mutate: func(_ *testing.T, c *Config) {
			c.Slots = append(c.Slots, testSlot(2, 1080))
		}},
		{name: "port clash wildcard vs specific", wantField: "slots[1].port", mutate: func(_ *testing.T, c *Config) {
			s := testSlot(2, 1080)
			s.BindAddr = "127.0.0.1"
			c.Slots = append(c.Slots, s)
		}},
		{name: "same port distinct specific addrs valid", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].BindAddr = "127.0.0.1"
			s := testSlot(2, 1080)
			s.BindAddr = "192.0.2.1"
			c.Slots = append(c.Slots, s)
		}},
		{name: "slot port equals UI port", wantField: "slots[0].port", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].Port = 8787
		}},
		{name: "slot port equals UI port, specific UI addr", wantField: "slots[0].port", mutate: func(_ *testing.T, c *Config) {
			c.Settings.UIBindAddr = "127.0.0.1:8080"
			c.Slots[0].Port = 8080
		}},
		{name: "slot port equals UI port, disjoint addrs valid", mutate: func(_ *testing.T, c *Config) {
			c.Settings.UIBindAddr = "127.0.0.1:8080"
			c.Slots[0].BindAddr = "192.0.2.1"
			c.Slots[0].Port = 8080
		}},
		{name: "invalid bind_addr", wantField: "slots[0].bind_addr", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].BindAddr = "not-an-ip"
		}},
		{name: "hostname bind_addr", wantField: "slots[0].bind_addr", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].BindAddr = "localhost"
		}},
		{name: "ipv6 bind_addr valid", mutate: func(_ *testing.T, c *Config) { c.Slots[0].BindAddr = "::1" }},
		{name: "port 0", wantField: "slots[0].port", mutate: func(_ *testing.T, c *Config) { c.Slots[0].Port = 0 }},
		{name: "port 70000", wantField: "slots[0].port", mutate: func(_ *testing.T, c *Config) { c.Slots[0].Port = 70000 }},
		{name: "bad killswitch", wantField: "slots[0].killswitch", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].Killswitch = "fallback"
		}},
		{name: "empty killswitch", wantField: "slots[0].killswitch", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].Killswitch = ""
		}},
		{name: "allow-direct valid", mutate: func(_ *testing.T, c *Config) { c.Slots[0].Killswitch = KillswitchAllowDirect }},
		{name: "health interval 0", wantField: "slots[0].health.interval_s", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].Health.IntervalS = 0
		}},
		{name: "health fail_threshold 0", wantField: "slots[0].health.fail_threshold", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].Health.FailThreshold = 0
		}},
		{name: "health url not http", wantField: "slots[0].health.url", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].Health.URL = "ftp://example.test/"
		}},
		{name: "socks auth missing username", wantField: "slots[0].socks_auth.username", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].SocksAuth = &SocksAuth{Password: "pw"}
		}},
		{name: "socks auth valid", mutate: func(_ *testing.T, c *Config) {
			c.Slots[0].SocksAuth = &SocksAuth{Username: "u", Password: "pw"}
		}},
		{name: "bad endpoint kind", wantField: "endpoints[2].kind", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[2].Kind = "openvpn"
		}},
		{name: "socks5 missing host", wantField: "endpoints[2].host", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[2].Host = ""
		}},
		{name: "socks5 missing port", wantField: "endpoints[2].port", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[2].Port = 0
		}},
		{name: "http missing host", wantField: "endpoints[3].host", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[3].Host = ""
		}},
		{name: "http port 70000", wantField: "endpoints[3].port", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[3].Port = 70000
		}},
		{name: "wireguard missing peer_public_key", wantField: "endpoints[0].peer_public_key", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[0].PeerPublicKey = ""
		}},
		{name: "wireguard bad peer_public_key", wantField: "endpoints[0].peer_public_key", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[0].PeerPublicKey = "not-base64!"
		}},
		{name: "wireguard missing endpoint", wantField: "endpoints[0].endpoint", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[0].Endpoint = ""
		}},
		{name: "wireguard endpoint without port", wantField: "endpoints[0].endpoint", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[0].Endpoint = "198.51.100.10"
		}},
		{name: "wireguard hostname endpoint valid", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[0].Endpoint = "se-got-wg-001.relays.example.test:51820"
		}},
		{name: "empty endpoint id", wantField: "endpoints[2].id", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[2].ID = ""
		}},
		{name: "duplicate endpoint id", wantField: "endpoints[3].id", mutate: func(_ *testing.T, c *Config) {
			c.Endpoints[3].ID = "GatewayProxy"
		}},
		{name: "invalid ui_bind_addr", wantField: "settings.ui_bind_addr", mutate: func(_ *testing.T, c *Config) {
			c.Settings.UIBindAddr = "not-an-addr"
		}},
		{name: "ui_bind_addr hostname", wantField: "settings.ui_bind_addr", mutate: func(_ *testing.T, c *Config) {
			c.Settings.UIBindAddr = "localhost:8080"
		}},
		{name: "ui_bind_addr port 0", wantField: "settings.ui_bind_addr", mutate: func(_ *testing.T, c *Config) {
			c.Settings.UIBindAddr = "0.0.0.0:0"
		}},
		{name: "same endpoint across two slots valid", mutate: func(_ *testing.T, c *Config) {
			c.Slots = append(c.Slots, testSlot(2, 1081, "WG-SE", "GatewayProxy"))
		}},
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range validateCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig(t)
			tc.mutate(t, &c)
			err := Validate(c)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			ve := asValidationErrors(t, err)
			if !hasField(ve, tc.wantField) {
				t.Fatalf("Validate() fields = %v, want one at %q", ve, tc.wantField)
			}
		})
	}
}

func TestValidationErrorFields(t *testing.T) {
	for _, tc := range validateCases() {
		if tc.wantField == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig(t)
			tc.mutate(t, &c)
			ve := asValidationErrors(t, Validate(c))
			for _, fe := range ve {
				if fe.Field == "" {
					t.Errorf("entry %+v has empty field path", fe)
				}
				if strings.TrimSpace(fe.Message) == "" {
					t.Errorf("entry %+v has empty message", fe)
				}
			}
			if !strings.Contains(ve.Error(), tc.wantField) {
				t.Errorf("Error() = %q, want it to mention %q", ve.Error(), tc.wantField)
			}
		})
	}
	t.Run("route field path", func(t *testing.T) {
		c := baseConfig(t)
		c.Slots[0].Route = []string{"WG-SE", "GatewayProxy", "WG-US"}
		ve := asValidationErrors(t, Validate(c))
		if !hasField(ve, "slots[0].route[2]") {
			t.Fatalf("fields = %v, want slots[0].route[2]", ve)
		}
	})
	t.Run("no secret values in messages", func(t *testing.T) {
		c := baseConfig(t)
		c.Endpoints[0].PeerPublicKey = ""
		c.Endpoints[0].PresharedKey = "bad-psk-value"
		ve := asValidationErrors(t, Validate(c))
		if strings.Contains(ve.Error(), c.Endpoints[0].PrivateKey) || strings.Contains(ve.Error(), "bad-psk-value") {
			t.Fatalf("validation error leaks a secret: %q", ve.Error())
		}
	})
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.Settings.UIBindAddr != "0.0.0.0:8787" {
		t.Errorf("UIBindAddr = %q", d.Settings.UIBindAddr)
	}
	if len(d.Slots) != 0 || len(d.Endpoints) != 0 {
		t.Errorf("Default has slots/endpoints: %+v", d)
	}
	h := DefaultHealth()
	if h.IntervalS != 30 || h.URL != "https://am.i.mullvad.net/json" || h.FailThreshold != 2 {
		t.Errorf("DefaultHealth = %+v", h)
	}
}

// TestValidateEndpoint checks that ValidateEndpoint reports exactly the per-endpoint errors
// Validate reports (id presence/uniqueness are collection rules and stay in Validate).
func TestValidateEndpoint(t *testing.T) {
	for _, tc := range validateCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig(t)
			tc.mutate(t, &c)
			var want ValidationErrors
			if ve, ok := Validate(c).(ValidationErrors); ok {
				for _, fe := range ve {
					if strings.HasPrefix(fe.Field, "endpoints[") && !strings.HasSuffix(fe.Field, ".id") {
						want = append(want, fe)
					}
				}
			}
			var got ValidationErrors
			for i, e := range c.Endpoints {
				got = append(got, ValidateEndpoint(fmt.Sprintf("endpoints[%d]", i), e)...)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("ValidateEndpoint errors = %v, want %v", got, want)
			}
		})
	}
	t.Run("custom path and no id required", func(t *testing.T) {
		got := ValidateEndpoint("endpoint", Endpoint{Kind: KindWireGuard})
		if !hasField(got, "endpoint.peer_public_key") || !hasField(got, "endpoint.endpoint") || hasField(got, "endpoint.id") {
			t.Fatalf("fields = %v", got)
		}
	})
	t.Run("valid endpoint returns nil", func(t *testing.T) {
		if got := ValidateEndpoint("e", wgEndpoint(t, "")); got != nil {
			t.Fatalf("ValidateEndpoint = %v, want nil", got)
		}
	})
}
