package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return m
}

func slotBody(port int, route ...string) string {
	r, _ := json.Marshal(route)
	if route == nil {
		r = []byte("[]")
	}
	return fmt.Sprintf(`{"name":"s","enabled":true,"bind_addr":"127.0.0.1","port":%d,"route":%s,"killswitch":"block"}`, port, r)
}

func hasField(errs []config.FieldError, field string) bool {
	for _, fe := range errs {
		if fe.Field == field && fe.Message != "" {
			return true
		}
	}
	return false
}

func TestSlotsGetDefaults(t *testing.T) {
	a := newAuthed(t)
	for id := 1; id <= config.MaxSlots; id++ {
		rec := a.call("GET", fmt.Sprintf("/api/slots/%d", id), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET slot %d: %d %s", id, rec.Code, rec.Body)
		}
		m := decodeMap(t, rec)
		if m["id"] != float64(id) || m["configured"] != false || m["killswitch"] != config.KillswitchBlock || m["enabled"] != false {
			t.Errorf("slot %d defaults: %v", id, m)
		}
		h, _ := m["health"].(map[string]any)
		if h["interval_s"] != float64(config.DefaultHealthSeconds) || h["url"] != config.DefaultHealthURL ||
			h["fail_threshold"] != float64(config.DefaultFailThreshold) {
			t.Errorf("slot %d health defaults: %v", id, h)
		}
		if r, ok := m["route"].([]any); !ok || len(r) != 0 {
			t.Errorf("slot %d route: %v", id, m["route"])
		}
	}
	for _, p := range []string{"0", "5", "-1", "abc"} {
		for _, path := range []string{"/api/slots/" + p} {
			if rec := a.call("GET", path, ""); rec.Code != http.StatusNotFound {
				t.Errorf("GET %s: %d, want 404", path, rec.Code)
			}
		}
		if rec := a.call("PUT", "/api/slots/"+p, slotBody(1080)); rec.Code != http.StatusNotFound {
			t.Errorf("PUT slot %s: %d, want 404", p, rec.Code)
		}
	}
	rec := a.call("GET", "/api/slots", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list: %d", rec.Code)
	}
	var list struct {
		Slots []map[string]any `json:"slots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Slots) != config.MaxSlots {
		t.Fatalf("list: %v %s", err, rec.Body)
	}
}

func TestSlotsPutValid(t *testing.T) {
	a := newAuthed(t)
	wg := a.create(wgBody("wg", fakeKey(t), "", fakeKey(t)))["id"].(string)
	px := a.create(socksBody("p", "h.example", "pw"))["id"].(string)

	rec := a.call("PUT", "/api/slots/2", slotBody(1081, wg, px))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT valid: %d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["configured"] != true || m["port"] != float64(1081) || m["enabled"] != true {
		t.Errorf("PUT response: %v", m)
	}
	cfg := a.store.Get()
	if len(cfg.Slots) != 1 || cfg.Slots[0].ID != 2 || !reflect.DeepEqual(cfg.Slots[0].Route, []string{wg, px}) {
		t.Fatalf("not persisted: %+v", cfg.Slots)
	}
	if cfg.Slots[0].Health != config.DefaultHealth() {
		t.Errorf("omitted health should get defaults, got %+v", cfg.Slots[0].Health)
	}
	got := decodeMap(t, a.call("GET", "/api/slots/2", ""))
	if got["configured"] != true || got["port"] != float64(1081) {
		t.Errorf("GET after PUT: %v", got)
	}

	// Update in place keeps a single slot entry; explicit health is stored.
	rec = a.call("PUT", "/api/slots/2", `{"name":"x","bind_addr":"127.0.0.1","port":1082,"route":[],`+
		`"killswitch":"allow-direct","health":{"interval_s":10,"url":"http://example.test/ip","fail_threshold":3}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT update: %d %s", rec.Code, rec.Body)
	}
	cfg = a.store.Get()
	if len(cfg.Slots) != 1 || cfg.Slots[0].Port != 1082 || cfg.Slots[0].Health.IntervalS != 10 || cfg.Slots[0].Enabled {
		t.Fatalf("update: %+v", cfg.Slots)
	}
}

func TestSlotsPutInvalid(t *testing.T) {
	a := newAuthed(t)
	wg := a.create(wgBody("wg", fakeKey(t), "", fakeKey(t)))["id"].(string)
	px := a.create(socksBody("p", "h.example", "pw"))["id"].(string)
	if rec := a.call("PUT", "/api/slots/1", slotBody(1080)); rec.Code != http.StatusOK {
		t.Fatalf("seed slot 1: %d %s", rec.Code, rec.Body)
	}
	if rec := a.call("PUT", "/api/slots/3", slotBody(1090)); rec.Code != http.StatusOK {
		t.Fatalf("seed slot 3: %d %s", rec.Code, rec.Body)
	}
	before := a.store.Get()

	cases := []struct{ name, id, body, field string }{
		{"proxy before wg", "2", slotBody(1081, px, wg), "route[1]"},
		{"unknown hop", "2", slotBody(1081, "ep-nope"), "route[0]"},
		{"port clash new slot", "2", slotBody(1080), "port"},
		{"port clash existing slot", "1", slotBody(1090), "port"},
		{"bad bind", "2", `{"bind_addr":"localhost","port":1081,"killswitch":"block"}`, "bind_addr"},
		{"bad killswitch", "2", `{"bind_addr":"127.0.0.1","port":1081,"killswitch":"maybe"}`, "killswitch"},
		{"bad health", "2", `{"bind_addr":"127.0.0.1","port":1081,"health":{"interval_s":0,"url":"ftp://x","fail_threshold":1}}`, "health.interval_s"},
		{"auth no user", "2", `{"bind_addr":"127.0.0.1","port":1081,"socks_auth":{"username":"","password":"p"}}`, "socks_auth.username"},
		{"auth no password", "2", `{"bind_addr":"127.0.0.1","port":1081,"socks_auth":{"username":"u"}}`, "socks_auth.password"},
		{"not json", "2", `{nope`, "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := a.call("PUT", "/api/slots/"+tc.id, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body)
			}
			if errs := decodeFieldErrors(t, rec); !hasField(errs, tc.field) {
				t.Errorf("errors %+v lack field %q", errs, tc.field)
			}
			if !reflect.DeepEqual(a.store.Get(), before) {
				t.Error("store changed after invalid request")
			}
		})
	}
	huge := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	if rec := a.call("PUT", "/api/slots/2", huge); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", rec.Code)
	}
}

func TestSlotsRequireAuth(t *testing.T) {
	a := newAuthed(t)
	if rec := a.do("GET", "/api/slots/1", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET no cookie: %d", rec.Code)
	}
	if rec := a.do("PUT", "/api/slots/1", slotBody(1080), withCookie(a.cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("PUT no CSRF: %d", rec.Code)
	}
	if rec := a.do("POST", "/api/slots/1/start", "", withCookie(a.cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("start no CSRF: %d", rec.Code)
	}
}

func TestSlotSocksAuthWriteOnly(t *testing.T) {
	a := newAuthed(t)
	const pw = "slot-s0cks-secret"
	check := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(rec.Body.String(), pw) {
			t.Errorf("%s: response contains the socks_auth password", what)
		}
	}
	authFor := func(m map[string]any) map[string]any {
		sa, _ := m["socks_auth"].(map[string]any)
		return sa
	}

	rec := a.call("PUT", "/api/slots/1", `{"name":"s","bind_addr":"127.0.0.1","port":1080,"socks_auth":{"username":"u","password":"`+pw+`"}}`)
	check("PUT", rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	sa := authFor(decodeMap(t, rec))
	if sa["username"] != "u" || sa["password_set"] != true {
		t.Errorf("socks_auth response: %v", sa)
	}
	if _, ok := sa["password"]; ok {
		t.Error("response has socks_auth.password field")
	}
	rec = a.call("GET", "/api/slots/1", "")
	check("GET", rec)
	if authFor(decodeMap(t, rec))["password_set"] != true {
		t.Errorf("GET password_set: %s", rec.Body)
	}
	check("GET list", a.call("GET", "/api/slots", ""))

	// Omitted password keeps the stored one.
	rec = a.call("PUT", "/api/slots/1", `{"name":"s2","bind_addr":"127.0.0.1","port":1080,"socks_auth":{"username":"u2"}}`)
	check("PUT keep", rec)
	if rec.Code != http.StatusOK || authFor(decodeMap(t, rec))["password_set"] != true {
		t.Fatalf("PUT keep: %d %s", rec.Code, rec.Body)
	}
	if got := a.store.Get().Slots[0].SocksAuth; got == nil || got.Password != pw || got.Username != "u2" {
		t.Fatalf("stored socks_auth after keep: %+v", got)
	}

	// Omitting socks_auth disables auth entirely.
	rec = a.call("PUT", "/api/slots/1", `{"name":"s2","bind_addr":"127.0.0.1","port":1080}`)
	if rec.Code != http.StatusOK || decodeMap(t, rec)["socks_auth"] != nil || a.store.Get().Slots[0].SocksAuth != nil {
		t.Fatalf("PUT without socks_auth: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(a.logs.String(), pw) {
		t.Error("log contains the socks_auth password")
	}
}

// TestSlotBlockLANRoundTrip: block_lan_targets survives PUT -> GET, an omitted field resets it
// to false, and export -> import keeps it in both modes.
func TestSlotBlockLANRoundTrip(t *testing.T) {
	body := `{"name":"d","enabled":false,"bind_addr":"127.0.0.1","port":1090,"route":[],` +
		`"killswitch":"allow-direct","block_lan_targets":true}`
	a := newExportEnv(t)
	if rec := a.call("PUT", "/api/slots/2", body); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if m := decodeMap(t, a.call("GET", "/api/slots/2", "")); m["block_lan_targets"] != true {
		t.Fatalf("GET block_lan_targets = %v, want true", m["block_lan_targets"])
	}
	if m := decodeMap(t, a.call("GET", "/api/slots/1", "")); m["block_lan_targets"] != false {
		t.Fatalf("GET slot 1 block_lan_targets = %v, want false", m["block_lan_targets"])
	}
	for _, mode := range []string{"encrypted", "no-secrets"} {
		t.Run(mode, func(t *testing.T) {
			var pass *string
			if mode == "encrypted" {
				pass = ptr(exportPassphrase)
			}
			rec := a.export(exportBody(exportAdminPW, mode, pass))
			if rec.Code != http.StatusOK {
				t.Fatalf("export: %d %s", rec.Code, rec.Body)
			}
			b := newImportEnv(t)
			decodeImportResult(t, b.importConfig(rec.Body.Bytes(), exportAdminPW, pass))
			if !storedSlot(t, b.store, 2).BlockLANTargets || storedSlot(t, b.store, 1).BlockLANTargets {
				t.Fatalf("imported slots: %+v", b.store.Get().Slots)
			}
			if m := decodeMap(t, b.call("GET", "/api/slots/2", "")); m["block_lan_targets"] != true {
				t.Fatalf("GET after import = %v, want true", m["block_lan_targets"])
			}
		})
	}
	off := strings.Replace(body, `,"block_lan_targets":true`, "", 1)
	if rec := a.call("PUT", "/api/slots/2", off); rec.Code != http.StatusOK {
		t.Fatalf("PUT without field: %d %s", rec.Code, rec.Body)
	}
	if storedSlot(t, a.store, 2).BlockLANTargets {
		t.Fatal("PUT without block_lan_targets kept true")
	}
}
