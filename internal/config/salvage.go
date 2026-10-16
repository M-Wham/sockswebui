package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// quarantinePrefix starts every corrupt-config quarantine name.
const quarantinePrefix = ConfigFileName + ".bad-"

// bootFromQuarantine runs when config.json is absent. If a quarantine file exists, the
// newest one is the only salvage source: LoadError names it and
// salvageFrom recovers the credentials when it can. Old quarantine files are pruned only
// after the salvage was persisted, and never the source. An unreadable newest file is an error,
// never a fresh device with setup open.
func (s *Store) bootFromQuarantine() error {
	name, err := newestQuarantine(s.dir)
	if err != nil || name == "" {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return fmt.Errorf("read quarantined config %s: %w", name, err)
	}
	s.loadErr = fmt.Sprintf("%s is missing; a corrupt config was quarantined earlier as %s", ConfigFileName, name)
	if s.salvageFrom(raw) {
		s.pruneQuarantine(name)
	}
	return nil
}

// newestQuarantine returns the name of the newest config.json.bad-<ts>[.N] in dir, or "".
// Only names quarantine itself produces count (pre-import backups and anything else never
// match). Newest = latest timestamp suffix, then highest .N (uniquePath).
func newestQuarantine(dir string) (string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list data dir: %w", err)
	}
	best, bestTS, bestN := "", "", -1
	for _, e := range ents {
		ts, n, ok := parseQuarantineName(e.Name())
		if !ok {
			continue
		}
		if ts > bestTS || (ts == bestTS && n > bestN) {
			best, bestTS, bestN = e.Name(), ts, n
		}
	}
	return best, nil
}

// parseQuarantineName splits config.json.bad-<ts>[.N] into ts and N (0 when absent).
func parseQuarantineName(name string) (ts string, n int, ok bool) {
	rest, found := strings.CutPrefix(name, quarantinePrefix)
	if !found || len(rest) < len(quarantineTimeFormat) {
		return "", 0, false
	}
	ts, suffix := rest[:len(quarantineTimeFormat)], rest[len(quarantineTimeFormat):]
	if _, err := time.Parse(quarantineTimeFormat, ts); err != nil {
		return "", 0, false
	}
	if suffix == "" {
		return ts, 0, true
	}
	digits, found := strings.CutPrefix(suffix, ".")
	if !found || digits == "" || digits[0] == '0' || strings.Trim(digits, "0123456789") != "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, false
	}
	return ts, n, true
}

var settingsKey = []byte(`"settings"`)

// salvageCredentials best-effort recovers only settings.admin_password_hash and
// settings.session_secret from the bytes of a corrupt config.
//
// The whole file is NOT decoded, so a syntax error elsewhere (e.g. inside "endpoints",
// which serializes first) does not defeat salvage. Instead each occurrence of the literal
// `"settings"` is a candidate: one preceded by `\` (escaped, inside a string) is skipped;
// otherwise it must be followed by optional JSON whitespace, ':', optional whitespace and
// '{'. The stdlib decoder then decodes exactly one value starting at that '{'. No other
// JSON parsing is done by hand.
//
// The first candidate that decodes AND validates wins: the hash must be a well-formed
// argon2id PHC string (ParseArgon2idHash) and the secret non-empty; both are returned
// together or not at all. Nothing else is read.
func salvageCredentials(raw []byte) (hash, secret string, ok bool) {
	for off := 0; ; {
		i := bytes.Index(raw[off:], settingsKey)
		if i < 0 {
			return "", "", false
		}
		at := off + i
		off = at + len(settingsKey)
		if at > 0 && raw[at-1] == '\\' {
			continue
		}
		if h, s, ok := decodeSettingsAt(raw, off); ok {
			return h, s, true
		}
	}
}

// decodeSettingsAt expects ws ':' ws '{' at raw[pos:] and decodes that one object.
func decodeSettingsAt(raw []byte, pos int) (hash, secret string, ok bool) {
	pos = skipWS(raw, pos)
	if pos >= len(raw) || raw[pos] != ':' {
		return "", "", false
	}
	pos = skipWS(raw, pos+1)
	if pos >= len(raw) || raw[pos] != '{' {
		return "", "", false
	}
	var probe struct {
		AdminPasswordHash string `json:"admin_password_hash"`
		SessionSecret     string `json:"session_secret"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw[pos:])).Decode(&probe); err != nil {
		return "", "", false
	}
	h, s := probe.AdminPasswordHash, probe.SessionSecret
	if _, err := ParseArgon2idHash(h); err != nil || s == "" {
		return "", "", false
	}
	return h, s, true
}

// skipWS skips JSON insignificant whitespace (RFC 8259 section 2).
func skipWS(raw []byte, pos int) int {
	for pos < len(raw) {
		switch raw[pos] {
		case ' ', '\t', '\n', '\r':
			pos++
		default:
			return pos
		}
	}
	return pos
}
