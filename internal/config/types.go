package config

import "encoding/json"

// Endpoint kinds.
const (
	KindWireGuard = "wireguard"
	KindSOCKS5    = "socks5"
	KindHTTP      = "http"
)

// Kill-switch policies. Block is the fail-closed default.
const (
	KillswitchBlock       = "block"
	KillswitchAllowDirect = "allow-direct"
)

// Names used in Endpoint.SecretsOmitted and Slot.SecretsOmitted. They name
// fields, never values.
const (
	OmittedPrivateKey    = "private_key"
	OmittedPresharedKey  = "preshared_key"
	OmittedPassword      = "password"
	OmittedSocksPassword = "socks_auth.password"
)

// Limits and defaults.
const (
	MaxSlots             = 4
	MaxRouteHops         = 4
	DefaultUIBindAddr    = "0.0.0.0:8787"
	DefaultHealthURL     = "https://am.i.mullvad.net/json"
	DefaultHealthSeconds = 30
	DefaultFailThreshold = 2
)

// Config is the whole persisted configuration (config.json).
type Config struct {
	Endpoints []Endpoint `json:"endpoints"`
	Slots     []Slot     `json:"slots"`
	Settings  Settings   `json:"settings"`
}

// Endpoint is one hop: a WireGuard tunnel or an upstream SOCKS5/HTTP proxy.
// Fields marked secret are plain strings here; the API layer redacts them.
type Endpoint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`

	// WireGuard fields.
	PrivateKey    string   `json:"private_key,omitempty"` // secret
	Addresses     []string `json:"addresses,omitempty"`
	DNS           []string `json:"dns,omitempty"`
	PeerPublicKey string   `json:"peer_public_key,omitempty"`
	PresharedKey  string   `json:"preshared_key,omitempty"` // secret
	Endpoint      string   `json:"endpoint,omitempty"`      // host:port
	AllowedIPs    []string `json:"allowed_ips,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	MullvadRelay  string   `json:"mullvad_relay,omitempty"`
	Country       string   `json:"country,omitempty"`

	// SOCKS5 / HTTP proxy fields.
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"` // secret

	// Session is the sticky-session template state. Zero value = no session; see Session.
	Session Session `json:"session,omitzero"`

	// SecretsOmitted names secrets missing after a no-secrets config import:
	// OmittedPrivateKey, OmittedPresharedKey, OmittedPassword. Server-owned: never read from a
	// request body; a PUT that sets a listed secret removes its name.
	SecretsOmitted []string `json:"secrets_omitted,omitempty"`
}

// Slot is one local SOCKS5 listener and its egress route.
type Slot struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	BindAddr   string     `json:"bind_addr"`
	Port       int        `json:"port"`
	SocksAuth  *SocksAuth `json:"socks_auth,omitempty"`
	Route      []string   `json:"route"`
	Killswitch string     `json:"killswitch"`
	// BlockLANTargets denies LAN-class targets on device-route dials only.
	// Absent in older configs and files means false.
	BlockLANTargets bool   `json:"block_lan_targets,omitempty"`
	Health          Health `json:"health"`
	// SecretsOmitted is the slot's own missing-secret list: OmittedSocksPassword.
	// Server-owned, like Endpoint.SecretsOmitted.
	SecretsOmitted []string `json:"secrets_omitted,omitempty"`
}

// SocksAuth is optional username/password auth for a slot's SOCKS5 listener.
type SocksAuth struct {
	Username string `json:"username"`
	Password string `json:"password"` // secret
}

// Health configures the per-slot probe through the full chain.
type Health struct {
	IntervalS     int    `json:"interval_s"`
	URL           string `json:"url"`
	FailThreshold int    `json:"fail_threshold"`
}

// Settings are global application settings.
type Settings struct {
	UIBindAddr        string `json:"ui_bind_addr"`
	AdminPasswordHash string `json:"admin_password_hash,omitempty"` // secret
	SessionSecret     string `json:"session_secret,omitempty"`      // secret
	// StartOnEnable: saving a slot with enabled changed starts or stops it.
	// A file without the field loads as true (see Settings.UnmarshalJSON).
	StartOnEnable bool `json:"start_on_enable"`
}

// UnmarshalJSON defaults StartOnEnable to true when the field is absent.
func (s *Settings) UnmarshalJSON(raw []byte) error {
	type plain Settings
	p := plain{StartOnEnable: true}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	*s = Settings(p)
	return nil
}

// DefaultHealth returns the health defaults.
func DefaultHealth() Health {
	return Health{IntervalS: DefaultHealthSeconds, URL: DefaultHealthURL, FailThreshold: DefaultFailThreshold}
}

// Default returns a config with default settings, no endpoints and no slots.
func Default() Config {
	return Config{
		Endpoints: []Endpoint{},
		Slots:     []Slot{},
		Settings:  Settings{UIBindAddr: DefaultUIBindAddr, StartOnEnable: true},
	}
}
