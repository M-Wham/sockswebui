package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/M-Wham/sockswebui/internal/config"
)

const (
	exportAdminPW    = "hunter22-long" // loggedIn's admin password
	exportProxyPW    = "ExportCanary-ProxyPW-91c2"
	exportPassphrase = "ExportCanary-Passphrase-3f8e"
)

// exportEnv is a logged-in server holding a WG endpoint, a SOCKS5 upstream and a slot,
// with an injected clock, hostname and FreeOSMemory counter.
type exportEnv struct {
	*authed
	frees   *atomic.Int32
	secrets []string
}

func newExportEnv(t *testing.T) *exportEnv {
	t.Helper()
	a := newAuthed(t)
	slotFixture(t, a, 1, false)
	a.create(socksBody("up-1", "203.0.113.7", exportProxyPW))
	frees := &atomic.Int32{}
	a.srv.freeOSMemory = func() { frees.Add(1) }
	a.srv.now = func() time.Time { return time.Date(2026, 10, 8, 13, 4, 5, 0, time.FixedZone("CET", 3600)) }
	a.srv.hostname = func() (string, error) { return "pi-box", nil }
	a.srv.appVersion = "v9.9.9-test"
	c := a.store.Get()
	var secrets []string
	for _, e := range c.Endpoints {
		for _, s := range []string{e.PrivateKey, e.PresharedKey, e.Password} {
			if s != "" {
				secrets = append(secrets, s)
			}
		}
	}
	for _, sl := range c.Slots {
		if sl.SocksAuth != nil && sl.SocksAuth.Password != "" {
			secrets = append(secrets, sl.SocksAuth.Password)
		}
	}
	if len(secrets) != 4 { // WG private + preshared, proxy password, SOCKS auth password
		t.Fatalf("fixture secrets = %d, want 4", len(secrets))
	}
	return &exportEnv{authed: a, frees: frees, secrets: secrets}
}

func exportBody(cur, mode string, passphrase *string) string {
	m := map[string]any{"current_password": cur, "mode": mode}
	if passphrase != nil {
		m["passphrase"] = *passphrase
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func ptr(s string) *string { return &s }

func (x *exportEnv) export(body string, opts ...reqOpt) *httptest.ResponseRecorder {
	x.t.Helper()
	opts = append([]reqOpt{withCookie(x.cookie), withCSRF(x.csrf)}, opts...)
	return x.do("POST", "/api/config/export", body, opts...)
}

func testKDF(pass, salt []byte, t, m uint32, p uint8) ([]byte, error) {
	return argon2.IDKey(pass, salt, t, m, p, 32), nil
}

func TestExportAuth(t *testing.T) {
	x := newExportEnv(t)
	body := exportBody(exportAdminPW, "no-secrets", nil)
	if rec := x.do("POST", "/api/config/export", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d, want 401", rec.Code)
	}
	if rec := x.do("POST", "/api/config/export", body, withCookie(x.cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF: %d, want 403", rec.Code)
	}
	if n := x.frees.Load(); n != 0 {
		t.Errorf("hash ran %d times on rejected auth", n)
	}
}

func TestExportWrongPassword(t *testing.T) {
	x := newExportEnv(t)
	rec := x.export(exportBody("wrong-password", "encrypted", ptr(exportPassphrase)))
	want := []config.FieldError{{Field: "current_password", Message: "is incorrect"}}
	if got := decodeFieldErrors(t, rec); rec.Code != http.StatusBadRequest || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %d %s, want 400 %v", rec.Code, rec.Body, want)
	}
	if n := x.frees.Load(); n != 1 {
		t.Errorf("hash runs = %d, want 1 (verify only, no export KDF)", n)
	}
}

func TestExportModeAndPassphrase(t *testing.T) {
	x := newExportEnv(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"ModePlain", exportBody(exportAdminPW, "plain", ptr(exportPassphrase)), 400},
		{"ModeEmpty", exportBody(exportAdminPW, "", ptr(exportPassphrase)), 400},
		{"ModeUpper", exportBody(exportAdminPW, "ENCRYPTED", ptr(exportPassphrase)), 400},
		{"PassMissing", exportBody(exportAdminPW, "encrypted", nil), 400},
		{"Pass11", exportBody(exportAdminPW, "encrypted", ptr(strings.Repeat("a", 11))), 400},
		{"Pass1025", exportBody(exportAdminPW, "encrypted", ptr(strings.Repeat("a", 1025))), 400},
		{"Pass12", exportBody(exportAdminPW, "encrypted", ptr(strings.Repeat("a", 12))), 200},
		{"Pass1024", exportBody(exportAdminPW, "encrypted", ptr(strings.Repeat("é", 512))), 200},
		{"NoSecretsWithPass", exportBody(exportAdminPW, "no-secrets", ptr(exportPassphrase)), 400},
		{"NoSecretsEmptyPass", exportBody(exportAdminPW, "no-secrets", ptr("")), 400},
		{"NoSecrets", exportBody(exportAdminPW, "no-secrets", nil), 200},
		{"UnknownField", `{"current_password":"` + exportAdminPW + `","mode":"no-secrets","x":1}`, 400},
		{"NotJSON", `nope`, 400},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every attempt counts against the shared login bucket: one client per case.
			if rec := x.export(tc.body, withRemote(fmt.Sprintf("192.0.2.%d:1", i+1))); rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body, tc.want)
			}
		})
	}
	t.Run("ModeMissingDefaultsToEncrypted", func(t *testing.T) {
		rec := x.export(`{"current_password":"`+exportAdminPW+`","passphrase":"`+exportPassphrase+`"}`,
			withRemote("192.0.2.200:1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s, want 200", rec.Code, rec.Body)
		}
		if _, mode, err := config.OpenExport(rec.Body.Bytes(), []byte(exportPassphrase), testKDF); err != nil || mode != config.ExportModeEncrypted {
			t.Fatalf("mode %q err %v, want encrypted", mode, err)
		}
	})
}

func TestExportBodyTooLarge(t *testing.T) {
	x := newExportEnv(t)
	big := exportBody(exportAdminPW, "encrypted", ptr(strings.Repeat("a", maxExportBody)))
	rec := x.export(big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d %s, want 413", rec.Code, rec.Body)
	}
	if n := x.frees.Load(); n != 0 {
		t.Errorf("hash ran %d times before the size check", n)
	}
}

func TestExportHeaders(t *testing.T) {
	x := newExportEnv(t)
	rec := x.export(exportBody(exportAdminPW, "no-secrets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	want := map[string]string{
		"Content-Type":           "application/json",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-store",
		"Content-Disposition":    `attachment; filename="sockswebui-pi-box-20261008-120405.json"`,
	}
	for k, v := range want {
		if got := rec.Header().Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	var f config.ExportFile
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.AppVersion != "v9.9.9-test" || f.CreatedAt != "2026-10-08T12:04:05Z" || f.Mode != "no-secrets" {
		t.Errorf("header fields: %+v", f)
	}
}

func TestExportFilenameHost(t *testing.T) {
	cases := map[string]string{
		"pi-box":                  "pi-box",
		"Router.lan":              "Router.lan",
		"my host!/\"x\"":          "myhostx",
		"":                        "device",
		"!!!":                     "device",
		"ümlaut":                  "mlaut",
		strings.Repeat("a", 64):   strings.Repeat("a", 63),
		strings.Repeat("b", 63):   strings.Repeat("b", 63),
		"a\r\nContent-Type: evil": "aContent-Typeevil",
	}
	for in, want := range cases {
		if got := exportHost(in); got != want {
			t.Errorf("exportHost(%q) = %q, want %q", in, got, want)
		}
	}
	x := newExportEnv(t)
	x.srv.hostname = func() (string, error) { return "", fmt.Errorf("no hostname") }
	rec := x.export(exportBody(exportAdminPW, "no-secrets", nil))
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="sockswebui-device-20261008-120405.json"` {
		t.Errorf("hostname error: Content-Disposition = %q", got)
	}
	x.srv.hostname = func() (string, error) { return strings.Repeat("h", 70) + "$", nil }
	rec = x.export(exportBody(exportAdminPW, "no-secrets", nil))
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="sockswebui-`+strings.Repeat("h", 63)+`-20261008-120405.json"` {
		t.Errorf("long hostname: Content-Disposition = %q", got)
	}
}

func TestExportEncryptedRoundTrip(t *testing.T) {
	x := newExportEnv(t)
	rec := x.export(exportBody(exportAdminPW, "encrypted", ptr(exportPassphrase)))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	if n := x.frees.Load(); n != 2 {
		t.Errorf("hash runs = %d, want 2 (verify + export KDF)", n)
	}
	for _, s := range append(x.secrets, "se-got-wg-001", "203.0.113.7") {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("encrypted file contains plaintext %q", s)
		}
	}
	p, mode, err := config.OpenExport(rec.Body.Bytes(), []byte(exportPassphrase), testKDF)
	if err != nil || mode != config.ExportModeEncrypted {
		t.Fatalf("open: mode %q err %v", mode, err)
	}
	c := x.store.Get()
	if !reflect.DeepEqual(p.Endpoints, c.Endpoints) || !reflect.DeepEqual(p.Slots, c.Slots) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", p, c)
	}
	if _, _, err := config.OpenExport(rec.Body.Bytes(), []byte(exportPassphrase+"x"), testKDF); err != config.ErrDecrypt {
		t.Errorf("wrong passphrase: %v", err)
	}
}

func TestExportNoSecrets(t *testing.T) {
	x := newExportEnv(t)
	rec := x.export(exportBody(exportAdminPW, "no-secrets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	if n := x.frees.Load(); n != 1 {
		t.Errorf("hash runs = %d, want 1 (verify only)", n)
	}
	body := rec.Body.String()
	st := x.store.Get().Settings
	for _, s := range append(x.secrets, "admin_password_hash", "session_secret", "ui_bind_addr",
		st.AdminPasswordHash, st.SessionSecret, exportAdminPW) {
		if strings.Contains(body, s) {
			t.Errorf("no-secrets file contains %q", s)
		}
	}
	p, _, err := config.OpenExport(rec.Body.Bytes(), nil, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Endpoints) != 2 || len(p.Slots) != len(x.store.Get().Slots) {
		t.Fatalf("payload: %+v", p)
	}
	for _, e := range p.Endpoints {
		if len(e.SecretsOmitted) == 0 {
			t.Errorf("endpoint %s has no omitted list", e.ID)
		}
	}
}
