package config

import "net/netip"

// validHost reports whether h is an IP literal (no brackets, no zone) or a DNS hostname made
// of LDH labels (letters, digits, hyphen; 1-63 bytes; no leading or trailing hyphen), at most
// 253 bytes, with an optional trailing dot. IDNs must be given in punycode. This keeps
// control bytes and spaces out of the HTTP CONNECT request line.
func validHost(h string) bool {
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == ""
	}
	h = trimOneDot(h)
	if h == "" || len(h) > 253 {
		return false
	}
	start := 0
	for i := 0; i <= len(h); i++ {
		if i < len(h) && h[i] != '.' {
			continue
		}
		if !validLabel(h[start:i]) {
			return false
		}
		start = i + 1
	}
	return true
}

func trimOneDot(h string) string {
	if len(h) > 1 && h[len(h)-1] == '.' {
		return h[:len(h)-1]
	}
	return h
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}
