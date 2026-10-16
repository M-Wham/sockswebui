package importer

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/M-Wham/sockswebui/internal/config"
)

// DefaultMTU is applied when a .conf has no MTU line (the wg-quick / wireguard-go default).
// The WG-in-WG reduction (outer − 80) is applied per route elsewhere, not here.
const DefaultMTU = 1420

// ParseConf parses one WireGuard .conf (one [Interface], exactly one [Peer]) into a
// wireguard Endpoint. It is pure: no I/O. Name, ID, relay and country are left empty.
// Error messages are human-readable and never contain key material.
func ParseConf(data []byte) (config.Endpoint, error) {
	data = bytes.TrimPrefix(data, []byte("\ufeff")) // editors on Windows may prepend a BOM
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return config.Endpoint{}, errors.New("not a text file (binary content)")
	}
	ifaces, peers, err := splitSections(string(data))
	if err != nil {
		return config.Endpoint{}, err
	}
	switch {
	case len(ifaces) == 0 && len(peers) == 0:
		return config.Endpoint{}, errors.New("empty WireGuard config")
	case len(ifaces) != 1:
		return config.Endpoint{}, fmt.Errorf("expected exactly one [Interface] section, found %d", len(ifaces))
	case len(peers) != 1:
		return config.Endpoint{}, fmt.Errorf("expected exactly one [Peer] section, found %d", len(peers))
	}
	e := config.Endpoint{Kind: config.KindWireGuard, MTU: DefaultMTU}
	if err := applyInterface(&e, ifaces[0]); err != nil {
		return config.Endpoint{}, err
	}
	if err := applyPeer(&e, peers[0]); err != nil {
		return config.Endpoint{}, err
	}
	if ve := config.ValidateEndpoint("endpoint", e); ve != nil {
		return config.Endpoint{}, fmt.Errorf("invalid WireGuard config: %s", ve[0].Message)
	}
	return e, nil
}

// section is one [Interface] or [Peer] block: lower-cased keys mapped to their values in
// file order (keys may repeat, e.g. several Address lines).
type section map[string][]string

func splitSections(text string) (ifaces, peers []section, err error) {
	var cur section
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if c := strings.IndexByte(line, '#'); c >= 0 {
			line = strings.TrimSpace(line[:c])
		}
		if line == "" {
			continue
		}
		n := i + 1
		if strings.HasPrefix(line, "[") {
			switch strings.ToLower(line) {
			case "[interface]":
				cur = section{}
				ifaces = append(ifaces, cur)
			case "[peer]":
				cur = section{}
				peers = append(peers, cur)
			default:
				return nil, nil, fmt.Errorf("line %d: unknown section header (want [Interface] or [Peer])", n)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, nil, fmt.Errorf("line %d: expected key = value", n)
		}
		if cur == nil {
			return nil, nil, fmt.Errorf("line %d: key = value outside a section (before [Interface] or [Peer])", n)
		}
		key := strings.ToLower(strings.TrimSpace(k))
		cur[key] = append(cur[key], strings.TrimSpace(v))
	}
	return ifaces, peers, nil
}

func applyInterface(e *config.Endpoint, s section) error {
	var err error
	if e.PrivateKey, err = keyField(s, "privatekey", "PrivateKey", true); err != nil {
		return err
	}
	if e.Addresses, err = listField(s, "address", "Address", true, validAddress); err != nil {
		return err
	}
	if e.DNS, err = listField(s, "dns", "DNS", false, nil); err != nil {
		return err
	}
	if v := last(s, "mtu"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > 65535 {
			return errors.New("[Interface] MTU must be a number between 1 and 65535")
		}
		e.MTU = n
	}
	return nil
}

func applyPeer(e *config.Endpoint, s section) error {
	var err error
	if e.PeerPublicKey, err = keyField(s, "publickey", "PublicKey", true); err != nil {
		return err
	}
	if e.PresharedKey, err = keyField(s, "presharedkey", "PresharedKey", false); err != nil {
		return err
	}
	if e.AllowedIPs, err = listField(s, "allowedips", "AllowedIPs", true, validPrefix); err != nil {
		return err
	}
	e.Endpoint = last(s, "endpoint")
	if e.Endpoint == "" {
		return errors.New("[Peer] is missing Endpoint")
	}
	if host, port, splitErr := net.SplitHostPort(e.Endpoint); splitErr != nil || host == "" || port == "" {
		return errors.New("[Peer] Endpoint must be host:port")
	}
	return nil
}

// keyField returns a base64 32-byte key. Errors name the field but never echo the value.
func keyField(s section, key, label string, required bool) (string, error) {
	v := last(s, key)
	if v == "" {
		if required {
			return "", fmt.Errorf("missing %s", label)
		}
		return "", nil
	}
	if b, err := base64.StdEncoding.DecodeString(v); err != nil || len(b) != 32 {
		return "", fmt.Errorf("%s is not a valid base64-encoded 32-byte key", label)
	}
	return v, nil
}

// listField joins every occurrence of key, split on commas, checking each item with valid.
func listField(s section, key, label string, required bool, valid func(string) bool) ([]string, error) {
	var out []string
	for _, v := range s[key] {
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if valid != nil && !valid(item) {
				return nil, fmt.Errorf("%s entry %q is not a valid IP address or CIDR prefix", label, item)
			}
			out = append(out, item)
		}
	}
	if required && len(out) == 0 {
		return nil, fmt.Errorf("missing %s", label)
	}
	return out, nil
}

func last(s section, key string) string {
	if v := s[key]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func validPrefix(s string) bool {
	_, err := netip.ParsePrefix(s)
	return err == nil
}

func validAddress(s string) bool {
	if validPrefix(s) {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}
