package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

func TestSettingsGet(t *testing.T) {
	a := newAuthed(t)
	rec := a.call("GET", "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["ui_bind_addr"] != config.DefaultUIBindAddr {
		t.Errorf("ui_bind_addr: %v", m["ui_bind_addr"])
	}
	if v, ok := m["config_error"]; !ok || v != "" {
		t.Errorf("config_error should be present and empty, got %v (present=%v)", v, ok)
	}
	h, _ := m["health_defaults"].(map[string]any)
	if h["interval_s"] != float64(config.DefaultHealthSeconds) || h["url"] != config.DefaultHealthURL ||
		h["fail_threshold"] != float64(config.DefaultFailThreshold) {
		t.Errorf("health_defaults: %v", h)
	}
	s := a.store.Get().Settings
	for _, secret := range []string{s.AdminPasswordHash, s.SessionSecret} {
		if secret != "" && strings.Contains(rec.Body.String(), secret) {
			t.Error("settings response contains a secret")
		}
	}
	for _, k := range []string{"admin_password_hash", "session_secret"} {
		if _, ok := m[k]; ok {
			t.Errorf("response has field %q", k)
		}
	}
	if rec := a.do("GET", "/api/settings", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET without cookie: %d", rec.Code)
	}
}

func TestSettingsConfigError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, store: st, logs: nil}
	srv := newServer(st, slog.New(slog.DiscardHandler), testParams)
	e.srv, e.h = srv, srv
	e.setup("hunter22-long")
	c, csrf := e.login("hunter22-long")
	rec := e.do("GET", "/api/settings", "", withCookie(c), withCSRF(csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if msg, _ := decodeMap(t, rec)["config_error"].(string); msg == "" || msg != st.LoadError() {
		t.Errorf("config_error = %q, want store load error %q", msg, st.LoadError())
	}
}

func TestSettingsPut(t *testing.T) {
	a := newAuthed(t)
	before := a.store.Get()

	rec := a.call("PUT", "/api/settings", `{"ui_bind_addr":"not-an-addr"}`)
	if rec.Code != http.StatusBadRequest || !hasField(decodeFieldErrors(t, rec), "ui_bind_addr") {
		t.Fatalf("invalid addr: %d %s", rec.Code, rec.Body)
	}
	if !reflect.DeepEqual(a.store.Get(), before) {
		t.Fatal("store changed after invalid PUT")
	}
	if rec := a.call("PUT", "/api/settings", `{nope`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d", rec.Code)
	}

	// Attempts to set secrets are ignored; ui_bind_addr is persisted.
	rec = a.call("PUT", "/api/settings", `{"ui_bind_addr":"127.0.0.1:9090","admin_password_hash":"x","session_secret":"y"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["ui_bind_addr"] != "127.0.0.1:9090" {
		t.Errorf("response: %v", m)
	}
	after := a.store.Get().Settings
	if after.UIBindAddr != "127.0.0.1:9090" {
		t.Errorf("not persisted: %+v", after.UIBindAddr)
	}
	if after.AdminPasswordHash != before.Settings.AdminPasswordHash || after.SessionSecret != before.Settings.SessionSecret {
		t.Error("PUT changed the password hash or session secret")
	}
	for _, s := range []string{after.AdminPasswordHash, after.SessionSecret} {
		if s != "" && (strings.Contains(rec.Body.String(), s) || strings.Contains(a.logs.String(), s)) {
			t.Error("secret leaked in response or log")
		}
	}

	// Clash with a slot listener is a field error on ui_bind_addr.
	if rec := a.call("PUT", "/api/slots/1", slotBody(1080)); rec.Code != http.StatusOK {
		t.Fatalf("seed slot: %d", rec.Code)
	}
	rec = a.call("PUT", "/api/settings", `{"ui_bind_addr":"0.0.0.0:1080"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("clash: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/api/settings", `{"ui_bind_addr":"127.0.0.1:9091"}`, withCookie(a.cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("PUT without CSRF: %d", rec.Code)
	}
}

// TestSettingsReportsEffectiveBind covers GET /api/settings
// reports the read-only ui_bind_effective and ui_bind_source supplied by cmd through
// WithUIBind, and PUT ignores both (no store change, no failure).
func TestSettingsReportsEffectiveBind(t *testing.T) {
	a := newAuthed(t)
	WithUIBind("127.0.0.2:8081", "flag")(a.srv)

	rec := a.call("GET", "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["ui_bind_effective"] != "127.0.0.2:8081" || m["ui_bind_source"] != "flag" {
		t.Fatalf("GET bind fields: effective=%v source=%v", m["ui_bind_effective"], m["ui_bind_source"])
	}
	stored := a.store.Get().Settings.UIBindAddr
	if m["ui_bind_addr"] != stored {
		t.Errorf("ui_bind_addr = %v, want stored %q", m["ui_bind_addr"], stored)
	}

	// Read-only: a PUT naming both fields saves ui_bind_addr only and still reports
	// the boot-time values.
	rec = a.call("PUT", "/api/settings",
		`{"ui_bind_addr":"127.0.0.1:9090","ui_bind_effective":"10.0.0.9:1","ui_bind_source":"setting"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	m = decodeMap(t, rec)
	if m["ui_bind_effective"] != "127.0.0.2:8081" || m["ui_bind_source"] != "flag" {
		t.Errorf("PUT response bind fields: effective=%v source=%v", m["ui_bind_effective"], m["ui_bind_source"])
	}
	if got := a.store.Get().Settings.UIBindAddr; got != "127.0.0.1:9090" {
		t.Errorf("stored ui_bind_addr = %q, want 127.0.0.1:9090", got)
	}

	// A PUT carrying only the read-only fields leaves the stored bind untouched.
	before := a.store.Get().Settings.UIBindAddr
	rec = a.call("PUT", "/api/settings",
		`{"ui_bind_addr":"127.0.0.1:9090","ui_bind_effective":"","ui_bind_source":"bogus"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT read-only fields: %d %s", rec.Code, rec.Body)
	}
	if got := a.store.Get().Settings.UIBindAddr; got != before {
		t.Errorf("stored ui_bind_addr changed to %q", got)
	}
	m = decodeMap(t, a.call("GET", "/api/settings", ""))
	if m["ui_bind_effective"] != "127.0.0.2:8081" || m["ui_bind_source"] != "flag" {
		t.Errorf("GET after PUT: effective=%v source=%v", m["ui_bind_effective"], m["ui_bind_source"])
	}
}

// Without WithUIBind both fields are still present (empty), so the DTO shape is fixed.
func TestSettingsBindFieldsPresentWithoutOption(t *testing.T) {
	a := newAuthed(t)
	m := decodeMap(t, a.call("GET", "/api/settings", ""))
	for _, k := range []string{"ui_bind_effective", "ui_bind_source"} {
		if v, ok := m[k]; !ok || v != "" {
			t.Errorf("%s = %v (present=%v), want present and empty", k, v, ok)
		}
	}
}

func TestSettingsAppVersion(t *testing.T) {
	a := newAuthed(t)
	a.srv.appVersion = "v9.9.9-test"
	rec := a.call("GET", "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if got := decodeMap(t, rec)["app_version"]; got != "v9.9.9-test" {
		t.Fatalf("app_version: %v", got)
	}
	body := `{"ui_bind_addr":"` + config.DefaultUIBindAddr + `","app_version":"hacked"}`
	rec = a.call("PUT", "/api/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if got := decodeMap(t, rec)["app_version"]; got != "v9.9.9-test" {
		t.Errorf("PUT app_version: %v", got)
	}
	if got := decodeMap(t, a.call("GET", "/api/settings", ""))["app_version"]; got != "v9.9.9-test" {
		t.Errorf("GET after PUT app_version: %v", got)
	}
}
