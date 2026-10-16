package api

import (
	"fmt"
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
)

// endpointRequest is the POST/PUT body for /api/endpoints. Secrets are pointers so the
// handler can tell "omitted" (nil: keep the stored value) from "" (clear it). The id is
// server-assigned and never read from the body.
type endpointRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind"`

	PrivateKey    *string  `json:"private_key"`
	Addresses     []string `json:"addresses"`
	DNS           []string `json:"dns"`
	PeerPublicKey string   `json:"peer_public_key"`
	PresharedKey  *string  `json:"preshared_key"`
	Endpoint      string   `json:"endpoint"`
	AllowedIPs    []string `json:"allowed_ips"`
	MTU           int      `json:"mtu"`
	MullvadRelay  string   `json:"mullvad_relay"`
	Country       string   `json:"country"`

	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	Password *string `json:"password"`

	// Session configures rotating sticky sessions. current_id is deliberately
	// not a field here: it is server-owned and a body value is ignored.
	Session *sessionRequest `json:"session"`
}

// sessionRequest carries the user-editable session settings. Nil fields keep the stored value.
type sessionRequest struct {
	Charset *string `json:"charset"`
	Length  *int    `json:"length"`
}

// endpointResponse is what the API returns. It has no secret fields at all, only
// *_set flags, so a secret cannot leak by accident.
type endpointResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`

	PrivateKeySet   *bool    `json:"private_key_set,omitempty"`
	Addresses       []string `json:"addresses,omitempty"`
	DNS             []string `json:"dns,omitempty"`
	PeerPublicKey   string   `json:"peer_public_key,omitempty"`
	PresharedKeySet *bool    `json:"preshared_key_set,omitempty"`
	Endpoint        string   `json:"endpoint,omitempty"`
	AllowedIPs      []string `json:"allowed_ips,omitempty"`
	MTU             int      `json:"mtu,omitempty"`
	MullvadRelay    string   `json:"mullvad_relay,omitempty"`
	Country         string   `json:"country,omitempty"`

	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username,omitempty"`
	PasswordSet *bool  `json:"password_set,omitempty"`
	// PasswordHasSession reports whether the stored password holds the {session} token,
	// without revealing the password.
	PasswordHasSession *bool          `json:"password_has_session,omitempty"`
	Session            config.Session `json:"session,omitzero"`
	// SecretsOmitted lists missing secret names (never values).
	SecretsOmitted []string `json:"secrets_omitted,omitempty"`
}

type endpointList struct {
	Endpoints []endpointResponse `json:"endpoints"`
}

// toEndpoint builds the config endpoint for id from the request. Only fields relevant to
// the request's kind are copied. Secrets omitted from the request are taken from prev
// (the stored endpoint on PUT, zero value on POST) when prev has the same kind.
func (req endpointRequest) toEndpoint(id string, prev config.Endpoint) config.Endpoint {
	e := config.Endpoint{ID: id, Name: req.Name, Kind: req.Kind}
	sameKind := prev.Kind == req.Kind
	switch req.Kind {
	case config.KindWireGuard:
		e.PrivateKey = secretOr(req.PrivateKey, prev.PrivateKey, sameKind)
		e.PresharedKey = secretOr(req.PresharedKey, prev.PresharedKey, sameKind)
		e.Addresses, e.DNS, e.AllowedIPs = req.Addresses, req.DNS, req.AllowedIPs
		e.PeerPublicKey, e.Endpoint, e.MTU = req.PeerPublicKey, req.Endpoint, req.MTU
		e.MullvadRelay, e.Country = req.MullvadRelay, req.Country
		e.SecretsOmitted = endpointOmitted(e, prev)
	case config.KindSOCKS5, config.KindHTTP:
		e.Host, e.Port, e.Username = req.Host, req.Port, req.Username
		e.Password = secretOr(req.Password, prev.Password, sameKind)
		e.SecretsOmitted = endpointOmitted(e, prev) // before mergeSession, which reads it
		e.Session = req.mergeSession(e, prev, sameKind)
	}
	return e
}

// mergeSession computes the session for e. Stored settings and current_id are
// kept when the request omits them; no token in either field deletes the session, unless the
// request sent one (then it is kept so Validate rejects the contradiction).
func (req endpointRequest) mergeSession(e, prev config.Endpoint, sameKind bool) config.Session {
	tokens := strings.Count(e.Username, config.SessionToken) + strings.Count(e.Password, config.SessionToken)
	if sameKind && tokens == 0 && passwordHeldSession(config.Endpoint{Username: e.Username,
		Password: e.Password, Session: prev.Session, SecretsOmitted: e.SecretsOmitted}) {
		tokens = 1 // the token is in the omitted password: keep the session
	}
	if tokens == 0 && req.Session == nil {
		return config.Session{}
	}
	var s config.Session
	if sameKind {
		s = prev.Session
	}
	if tokens > 1 && req.Session == nil && s == (config.Session{}) {
		return s // Validate rejects the token count
	}
	if s == (config.Session{}) {
		s.Charset, s.Length = config.SessionAlnum, config.DefaultSessionLength
	}
	if req.Session == nil {
		return s
	}
	if req.Session.Charset != nil {
		s.Charset = *req.Session.Charset
	}
	if req.Session.Length != nil {
		s.Length = *req.Session.Length
	}
	return s
}

func secretOr(v *string, stored string, keep bool) string {
	if v != nil {
		return *v
	}
	if keep {
		return stored
	}
	return ""
}

func toEndpointResponse(e config.Endpoint) endpointResponse {
	r := endpointResponse{ID: e.ID, Name: e.Name, Kind: e.Kind}
	switch e.Kind {
	case config.KindWireGuard:
		r.PrivateKeySet, r.PresharedKeySet = flag(e.PrivateKey != ""), flag(e.PresharedKey != "")
		r.Addresses, r.DNS, r.AllowedIPs = e.Addresses, e.DNS, e.AllowedIPs
		r.PeerPublicKey, r.Endpoint, r.MTU = e.PeerPublicKey, e.Endpoint, e.MTU
		r.MullvadRelay, r.Country = e.MullvadRelay, e.Country
	case config.KindSOCKS5, config.KindHTTP:
		r.Host, r.Port, r.Username = e.Host, e.Port, e.Username
		r.PasswordSet = flag(e.Password != "")
		r.PasswordHasSession = flag(strings.Contains(e.Password, config.SessionToken) || passwordHeldSession(e))
		r.Session = e.Session
	}
	r.SecretsOmitted = e.SecretsOmitted
	return r
}

func flag(b bool) *bool { return &b }

// slotRequest is the PUT /api/slots/{id} body. The id comes from the path. A nil
// SocksAuth disables SOCKS auth; a nil SocksAuth.Password keeps the stored password.
// A nil Health gets config.DefaultHealth(); an empty Killswitch gets "block" (fail closed).
type slotRequest struct {
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	BindAddr   string            `json:"bind_addr"`
	Port       int               `json:"port"`
	SocksAuth  *socksAuthRequest `json:"socks_auth"`
	Route      []string          `json:"route"`
	Killswitch string            `json:"killswitch"`
	// BlockLANTargets absent means false.
	BlockLANTargets bool           `json:"block_lan_targets"`
	Health          *config.Health `json:"health"`
}

type socksAuthRequest struct {
	Username string  `json:"username"`
	Password *string `json:"password"`
}

// slotResponse never carries the SOCKS auth password, only password_set.
// Configured is false for a slot id that has never been saved (defaults shown).
type slotResponse struct {
	ID              int                `json:"id"`
	Configured      bool               `json:"configured"`
	Name            string             `json:"name"`
	Enabled         bool               `json:"enabled"`
	BindAddr        string             `json:"bind_addr"`
	Port            int                `json:"port"`
	SocksAuth       *socksAuthResponse `json:"socks_auth"`
	Route           []string           `json:"route"`
	Killswitch      string             `json:"killswitch"`
	BlockLANTargets bool               `json:"block_lan_targets"`
	Health          config.Health      `json:"health"`
	// SecretsOmitted lists missing secret names (never values).
	SecretsOmitted []string `json:"secrets_omitted,omitempty"`
	// Status is read-only live state. slotRequest has no such field, so a
	// PUT ignores it.
	Status slotStatusView `json:"status"`
}

type socksAuthResponse struct {
	Username    string `json:"username"`
	PasswordSet bool   `json:"password_set"`
}

type slotList struct {
	Slots []slotResponse `json:"slots"`
}

// toSlot builds the config slot for id. prev is the stored slot (nil if unset) and
// supplies the SOCKS auth password when the request omits it.
func (req slotRequest) toSlot(id int, prev *config.Slot) config.Slot {
	sl := config.Slot{ID: id, Name: req.Name, Enabled: req.Enabled, BindAddr: req.BindAddr,
		Port: req.Port, Route: req.Route, Killswitch: req.Killswitch, BlockLANTargets: req.BlockLANTargets,
		Health: config.DefaultHealth()}
	if sl.Route == nil {
		sl.Route = []string{}
	}
	if sl.Killswitch == "" {
		sl.Killswitch = config.KillswitchBlock
	}
	if req.Health != nil {
		sl.Health = *req.Health
	}
	if req.SocksAuth != nil {
		stored := ""
		if prev != nil && prev.SocksAuth != nil {
			stored = prev.SocksAuth.Password
		}
		sl.SocksAuth = &config.SocksAuth{Username: req.SocksAuth.Username,
			Password: secretOr(req.SocksAuth.Password, stored, true)}
	}
	sl.SecretsOmitted = slotOmitted(sl, prev)
	return sl
}

// defaultSlot is what GET returns for a slot id that has never been saved.
func defaultSlot(id int) config.Slot {
	return config.Slot{ID: id, Name: fmt.Sprintf("Slot %d", id), BindAddr: defaultSlotBind,
		Port: defaultSlotBasePort + id - 1, Route: []string{}, Killswitch: config.KillswitchBlock,
		Health: config.DefaultHealth()}
}

func toSlotResponse(sl config.Slot, configured bool) slotResponse {
	r := slotResponse{ID: sl.ID, Configured: configured, Name: sl.Name, Enabled: sl.Enabled,
		BindAddr: sl.BindAddr, Port: sl.Port, Route: sl.Route, Killswitch: sl.Killswitch, Health: sl.Health,
		BlockLANTargets: sl.BlockLANTargets, SecretsOmitted: sl.SecretsOmitted}
	if r.Route == nil {
		r.Route = []string{}
	}
	if sl.SocksAuth != nil {
		r.SocksAuth = &socksAuthResponse{Username: sl.SocksAuth.Username, PasswordSet: sl.SocksAuth.Password != ""}
	}
	return r
}

// settingsRequest is the PUT /api/settings body. Only ui_bind_addr is writable; the admin
// password hash and session secret cannot be set through this route (unknown fields are ignored).
type settingsRequest struct {
	UIBindAddr string `json:"ui_bind_addr"`
	// StartOnEnable is a pointer: absent leaves the stored value alone.
	StartOnEnable *bool `json:"start_on_enable,omitempty"`
	// CurrentPassword and NewPassword are both-or-neither; pointers distinguish absent
	// from empty. Together they request a password change (see password_change.go).
	CurrentPassword *string `json:"current_password,omitempty"`
	NewPassword     *string `json:"new_password,omitempty"`
}

// settingsResponse never carries admin_password_hash or session_secret. HealthDefaults
// are the read-only defaults; ConfigError is the corrupt-boot message.
type settingsResponse struct {
	UIBindAddr     string        `json:"ui_bind_addr"`
	StartOnEnable  bool          `json:"start_on_enable"`
	HealthDefaults config.Health `json:"health_defaults"`
	ConfigError    string        `json:"config_error"`
	// UIBindEffective and UIBindSource are read-only: the UI
	// address actually used at boot and whether it came from --ui-addr ("flag") or
	// settings.ui_bind_addr ("setting"). PUT ignores both (settingsRequest lacks them).
	UIBindEffective string `json:"ui_bind_effective"`
	UIBindSource    string `json:"ui_bind_source"`
	// AppVersion is the running build version. It is read-only: settingsRequest lacks it.
	AppVersion string `json:"app_version"`
	// CSRFToken is set only in the response to a password change: the caller's session
	// survives but its CSRF token is re-derived from the rotated secret.
	CSRFToken string `json:"csrf_token,omitempty"`
}

// importResult is one per-file outcome of POST /api/endpoints/import. File is
// sanitized for display; Reason and Warnings never contain key material.
type importResult struct {
	File     string   `json:"file"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	ID       string   `json:"id,omitempty"`
}

type importResponse struct {
	Results []importResult `json:"results"`
}
