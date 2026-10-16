package api

import (
	"errors"
	"net/netip"
	"strings"
)

// NameList is the accepted UI name list: any IP literal,
// localhost, the device hostname, <hostname>.local and each --ui-host value.
// Names match case-insensitively on the name only (the port is ignored), with one
// trailing dot trimmed. It is built once at boot and read-only afterwards.
// The zero value accepts IP literals only.
type NameList struct {
	names map[string]struct{}
}

// NewNameList builds the list from the device hostname ("" when unknown) and the
// extra --ui-host names. Invalid entries are skipped, so they can never widen the list.
func NewNameList(hostname string, extra ...string) NameList {
	l := NameList{names: map[string]struct{}{"localhost": {}}}
	if h, ok := normName(hostname); ok && ValidUIHost(h) == nil {
		l.names[h] = struct{}{}
		l.names[h+".local"] = struct{}{}
	}
	for _, e := range extra {
		if n, ok := normName(e); ok && ValidUIHost(n) == nil {
			l.names[n] = struct{}{}
		}
	}
	return l
}

// Allows reports whether hostport (a Host header value, or a bare host name or IP)
// names this UI.
func (l NameList) Allows(hostport string) bool {
	if hostport == "" {
		return false
	}
	if hostport[0] == '[' {
		end := strings.IndexByte(hostport, ']')
		if end < 0 {
			return false
		}
		rest := hostport[end+1:]
		if rest != "" && (rest[0] != ':' || !validPort(rest[1:])) {
			return false
		}
		a, err := netip.ParseAddr(hostport[1:end])
		return err == nil && a.Is6()
	}
	if _, err := netip.ParseAddr(hostport); err == nil {
		return true // bare IPv4 or IPv6 (e.g. from url.Hostname)
	}
	name := hostport
	if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		if !validPort(hostport[i+1:]) {
			return false
		}
		name = hostport[:i]
	}
	if a, err := netip.ParseAddr(name); err == nil {
		return a.Is4()
	}
	n, ok := normName(name)
	if !ok {
		return false
	}
	_, found := l.names[n]
	return found
}

// ValidUIHost checks a --ui-host value: a bare DNS name (letters, digits, hyphens,
// dot-separated labels, one optional trailing dot). Schemes, paths, ports and
// non-ASCII names are refused.
func ValidUIHost(s string) error {
	switch {
	case strings.Contains(s, "://"):
		return errors.New("must be a bare host name, without a scheme")
	case strings.ContainsAny(s, "/?#"):
		return errors.New("must be a bare host name, without a path")
	case strings.ContainsAny(s, ":[]"):
		return errors.New("must be a bare host name, without a port")
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return errors.New("must be a non-empty host name")
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid host name label")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("host name may contain only ASCII letters, digits, hyphens and dots")
			}
		}
	}
	return nil
}

// normName lower-cases an ASCII name and trims one trailing dot. Non-ASCII or
// control bytes are refused before lower-casing, so no Unicode folding (e.g. the
// Kelvin sign to "k") can turn a lookalike into an accepted name.
func normName(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] >= 0x7f {
			return "", false
		}
	}
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	return s, s != ""
}

// validPort reports whether p is 1-5 ASCII digits.
func validPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	return true
}
