package importer

import (
	"path"
	"strings"
	"unicode"

	"github.com/M-Wham/sockswebui/internal/config"
)

// ParseFile parses one uploaded WireGuard .conf named filename. Any provider's
// config is accepted. Mullvad-style names also set MullvadRelay, Country and Name = relay;
// any other name gets Name = the filename stem. ParseConf errors pass through unchanged.
// Pure: no I/O.
func ParseFile(filename string, data []byte) (config.Endpoint, []string, error) {
	e, err := ParseConf(data)
	if err != nil {
		return config.Endpoint{}, nil, err
	}
	if relay, country, ok := MullvadName(filename); ok {
		e.Name, e.MullvadRelay, e.Country = relay, relay, country
		return e, nil, nil
	}
	base := baseName(filename)
	e.Name = sanitizeName(strings.TrimSuffix(base, path.Ext(base)))
	return e, nil, nil
}

// maxNameRunes caps an endpoint name derived from an uploaded filename.
const maxNameRunes = 64

// fallbackName is used when a filename stem is empty after sanitizing.
const fallbackName = "imported"

// sanitizeName strips control characters, trims space, caps the length at maxNameRunes
// runes and falls back to fallbackName when nothing is left.
func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxNameRunes {
		s = strings.TrimSpace(string(r[:maxNameRunes]))
	}
	if s == "" {
		return fallbackName
	}
	return s
}
