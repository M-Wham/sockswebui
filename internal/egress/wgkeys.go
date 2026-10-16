package egress

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"github.com/M-Wham/sockswebui/internal/config"
)

// keyHex converts a base64 WireGuard key (config form) to the hex form the
// wireguard-go UAPI expects. Errors name the field, never the value.
func keyHex(field, b64Key string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("%s: must be a base64-encoded 32-byte key", field)
	}
	return hex.EncodeToString(raw), nil
}

// wgIPC renders the UAPI configuration for one WireGuard hop whose peer is
// reached at endpoint. The result contains secrets: it is handed to IpcSet
// and never logged, wrapped into an error, or stored beyond Build.
func wgIPC(path string, ep config.Endpoint, endpoint netip.AddrPort, allowed []netip.Prefix) (string, error) {
	priv, err := keyHex(path+".private_key", ep.PrivateKey)
	if err != nil {
		return "", err
	}
	pub, err := keyHex(path+".peer_public_key", ep.PeerPublicKey)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("private_key=" + priv + "\n")
	b.WriteString("replace_peers=true\n")
	b.WriteString("public_key=" + pub + "\n")
	if ep.PresharedKey != "" {
		psk, err := keyHex(path+".preshared_key", ep.PresharedKey)
		if err != nil {
			return "", err
		}
		b.WriteString("preshared_key=" + psk + "\n")
	}
	b.WriteString("endpoint=" + endpoint.String() + "\n")
	b.WriteString("replace_allowed_ips=true\n")
	for _, p := range allowed {
		b.WriteString("allowed_ip=" + p.String() + "\n")
	}
	return b.String(), nil
}
