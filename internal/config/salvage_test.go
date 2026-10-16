package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// testCreds returns a real argon2id PHC hash (cheap params) and a random session
// secret, both generated at test time. Nothing here is committed.
func testCreds(t *testing.T) (hash, secret string) {
	t.Helper()
	salt := make([]byte, 16)
	sec := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(sec); err != nil {
		t.Fatal(err)
	}
	key := argon2.IDKey([]byte("test password"), salt, 1, 1024, 1, 32)
	hash = fmt.Sprintf("$argon2id$v=%d$m=1024,t=1,p=1$%s$%s", argon2.Version,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	return hash, base64.RawURLEncoding.EncodeToString(sec)
}

func quarantined(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ConfigFileName+".bad-*"))
	return m
}

// savedWithByteFlip marshals a valid config (key order endpoints, slots, settings, as
// Store.Save writes it) and changes the first ',' inside "endpoints" to ';'.
func savedWithByteFlip(t *testing.T, hash, secret string) string {
	t.Helper()
	c := baseConfig(t)
	c.Endpoints[0].Name = "KEEPOUT"
	c.Settings.UIBindAddr = "0.0.0.0:9090"
	c.Settings.AdminPasswordHash, c.Settings.SessionSecret = hash, secret
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(raw, []byte(`"endpoints":[`))
	end := bytes.Index(raw, []byte(`"settings"`))
	i := bytes.IndexByte(raw[start:end], ',')
	if start < 0 || end < start || i < 0 {
		t.Fatalf("unexpected layout: %s", raw)
	}
	raw[start+i] = ';'
	if json.Valid(raw) {
		t.Fatal("byte flip left valid JSON")
	}
	return string(raw)
}

func TestStoreCorruptSalvage(t *testing.T) {
	hash, secret := testCreds(t)
	settings := fmt.Sprintf(`{"ui_bind_addr":"0.0.0.0:9090","admin_password_hash":%q,"session_secret":%q}`, hash, secret)
	dHash, dSecret := testCreds(t)
	decoy := fmt.Sprintf(`{"admin_password_hash":%q,"session_secret":%q}`, dHash, dSecret)
	cases := map[string]string{
		"slots wrong type":          `{"endpoints":[{"id":"x","private_key":"KEEPOUT"}],"slots":"oops","settings":` + settings + `}`,
		"fails validation":          `{"endpoints":[],"slots":[{"id":99,"port":-1,"name":"KEEPOUT"}],"settings":` + settings + `}`,
		"syntax error in endpoints": savedWithByteFlip(t, hash, secret),
		"syntax error later":        `{"settings":` + settings + `,"slots":[{"name":"KEEPOUT"`,
		"whitespace around colon":   "{\"endpoints\":[{\"name\":\"KEEPOUT\";}],\"settings\" :\n\t {" + settings[1:] + "}",
		// A `\"settings":{…}` candidate is escaped (inside a string) and must be skipped even
		// though its creds would validate; the real settings object wins.
		"escaped decoy before real": `{"endpoints":[{"name":"\"settings":` + decoy + `}],"settings":` + settings + `}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ConfigFileName)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			want := Default()
			want.Settings.AdminPasswordHash = hash
			want.Settings.SessionSecret = secret

			s := openStore(t, dir)
			if got := s.Get(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Get = %+v, want defaults + salvaged creds", got)
			}
			if !s.CredentialsSalvaged() {
				t.Fatal("CredentialsSalvaged = false")
			}
			le := s.LoadError()
			if le == "" || strings.Contains(le, hash) || strings.Contains(le, secret) {
				t.Fatalf("LoadError = %q (empty or leaks secret)", le)
			}
			bad := quarantined(t, dir)
			if len(bad) != 1 {
				t.Fatalf("quarantine files = %v", bad)
			}
			if b, _ := os.ReadFile(bad[0]); !bytes.Equal(b, []byte(raw)) {
				t.Fatal("quarantined bytes differ from original")
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatalf("salvaged config not persisted: %v", err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
			}
			persisted, _ := os.ReadFile(path)
			if bytes.Contains(persisted, []byte("KEEPOUT")) || bytes.Contains(persisted, []byte("9090")) {
				t.Fatalf("persisted config carries non-credential data: %s", persisted)
			}
			var onDisk Config
			if err := json.Unmarshal(persisted, &onDisk); err != nil {
				t.Fatal(err)
			}
			s2 := openStore(t, dir)
			if s2.LoadError() != "" {
				t.Fatalf("reopen LoadError = %q", s2.LoadError())
			}
			if got := s2.Get(); !reflect.DeepEqual(got, want) {
				t.Fatalf("reopen Get = %+v, want %+v", got, want)
			}
		})
	}
}

func TestStoreCorruptNoSalvage(t *testing.T) {
	hash, secret := testCreds(t)
	cases := map[string]string{
		"pure garbage":         "\x00\xffnot json at all",
		"settings truncated":   fmt.Sprintf(`{"endpoints":[;],"settings":{"admin_password_hash":%q,"session_secret":%q`, hash, secret),
		"settings damaged":     fmt.Sprintf(`{"endpoints":[;],"settings":{"admin_password_hash":%q;"session_secret":%q}}`, hash, secret),
		"settings no colon":    fmt.Sprintf(`{"endpoints":[;],"settings" {"admin_password_hash":%q,"session_secret":%q}}`, hash, secret),
		"settings not brace":   fmt.Sprintf(`{"endpoints":[;],"settings":[{"admin_password_hash":%q,"session_secret":%q}]}`, hash, secret),
		"settings key value":   fmt.Sprintf(`{"endpoints":[;],"x":"settings","y":{"admin_password_hash":%q,"session_secret":%q}}`, hash, secret),
		"escaped decoy only":   `{"endpoints":[{"name":"\"settings":` + fmt.Sprintf(`{"admin_password_hash":%q,"session_secret":%q}`, hash, secret) + `}]}`,
		"hash not argon2id":    fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":"$2b$10$abcdef","session_secret":%q}}`, secret),
		"secret missing":       fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":%q}}`, hash),
		"secret empty":         fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":%q,"session_secret":""}}`, hash),
		"settings not object":  `{"slots":"oops","settings":"nope"}`,
		"hash wrong type":      fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":7,"session_secret":%q}}`, secret),
		"top level not object": `["a"]`,
		"hash prefix only":     fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":"$argon2id$garbage","session_secret":%q}}`, secret),
		"hash bad salt b64":    fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":"$argon2id$v=19$m=1024,t=1,p=1$!!!$YWJj","session_secret":%q}}`, secret),
		"hash huge memory":     fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":"$argon2id$v=19$m=99999999,t=1,p=1$YWJj$YWJj","session_secret":%q}}`, secret),
		"hash wrong version":   fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":"$argon2id$v=16$m=1024,t=1,p=1$YWJj$YWJj","session_secret":%q}}`, secret),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s := openStore(t, dir)
			if got := s.Get(); !reflect.DeepEqual(got, Default()) {
				t.Fatalf("Get = %+v, want Default()", got)
			}
			if s.CredentialsSalvaged() {
				t.Fatal("CredentialsSalvaged = true")
			}
			if s.LoadError() == "" {
				t.Fatal("LoadError empty")
			}
			if len(quarantined(t, dir)) != 1 {
				t.Fatal("quarantine file missing")
			}
			if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); err == nil {
				t.Fatal("config.json written although nothing was salvaged")
			}
		})
	}
}

// TestQuarantineMode: a 0644 config.json is quarantined as a 0600 .bad-* file, since it
// may hold the admin hash and WG keys.
func TestQuarantineMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("\x00\xffnot json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	openStore(t, dir)
	q := quarantined(t, dir)
	if len(q) != 1 {
		t.Fatalf("quarantine files = %v, want 1", q)
	}
	fi, err := os.Stat(q[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("quarantine mode = %o, want 600", got)
	}
}
