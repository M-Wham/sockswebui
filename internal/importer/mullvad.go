package importer

import (
	"regexp"
	"strings"
)

// mullvadRe matches the Mullvad relay filename: <cc>-<city>-wg-<nnn>.conf
// (2-letter country, 3-letter city, "wg", digits), applied to the lower-cased base name.
var mullvadRe = regexp.MustCompile(`^(([a-z]{2})-[a-z]{3}-wg-[0-9]+)\.conf$`)

// MullvadName extracts the relay name and country code from a Mullvad-style filename.
// Any directory prefix (with / or \ separators) is ignored; the result is lower-cased.
// ok is false for any other name. Pure: no I/O.
func MullvadName(filename string) (relay, country string, ok bool) {
	m := mullvadRe.FindStringSubmatch(strings.ToLower(baseName(filename)))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// baseName returns the last path element, treating both / and \ as separators
// (zip entries created on Windows may use backslashes).
func baseName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		return name[i+1:]
	}
	return name
}
