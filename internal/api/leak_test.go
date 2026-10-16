package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// leakRecorder captures every response body and response header except Set-Cookie
// (which legitimately carries the session token to the browser).
type leakRecorder struct {
	a      *authed
	bodies bytes.Buffer
}

func (l *leakRecorder) call(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	l.a.t.Helper()
	rec := l.a.do(method, path, body, opts...)
	l.record(method, path, rec)
	return rec
}

func (l *leakRecorder) record(method, path string, rec *httptest.ResponseRecorder) {
	fmt.Fprintf(&l.bodies, "== %s %s -> %d\n", method, path, rec.Code)
	for k, vs := range rec.Header() {
		if k != "Set-Cookie" {
			fmt.Fprintf(&l.bodies, "%s: %s\n", k, strings.Join(vs, ","))
		}
	}
	l.bodies.Write(rec.Body.Bytes())
	l.bodies.WriteByte('\n')
}

func (l *leakRecorder) authedCall(method, path, body string) *httptest.ResponseRecorder {
	l.a.t.Helper()
	return l.call(method, path, body, withCookie(l.a.cookie), withCSRF(l.a.csrf))
}

func (l *leakRecorder) mustCreate(body string) string {
	l.a.t.Helper()
	rec := l.authedCall("POST", "/api/endpoints", body)
	if rec.Code != http.StatusCreated {
		l.a.t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return decodeEndpoint(l.a.t, rec)["id"].(string)
}

// recordSnapshot adds the first SSE frame of GET /api/events to the captured bodies.
func (l *leakRecorder) recordSnapshot(e *testEnv) {
	l.a.t.Helper()
	ts := httptest.NewServer(e.h)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.AddCookie(l.a.cookie)
	res, err := ts.Client().Do(req)
	if err != nil {
		l.a.t.Fatal(err)
	}
	defer res.Body.Close()
	f := nextEvent(l.a.t, bufio.NewReader(res.Body))
	if f.event != "snapshot" || !strings.Contains(f.data, `"last_error"`) {
		l.a.t.Fatalf("snapshot frame = %+v", f)
	}
	fmt.Fprintf(&l.bodies, "== SSE snapshot\n%s\n", f.data)
}

// TestNoSecretLeaks drives every route with distinctive secrets and asserts none of
// them appear in any response body/header (except Set-Cookie) or in any slog output.
func TestNoSecretLeaks(t *testing.T) {
	const (
		adminPW   = "LeakCanary-AdminPW-7f3a9c"
		socksPW   = "LeakCanary-Socks5PW-1b2e4d"
		httpPW    = "LeakCanary-HttpPW-5c6d7e"
		slotPW    = "LeakCanary-SlotPW-8e9f0a"
		socksPW2  = "LeakCanary-Socks5PW2-c3d4"
		badWGPriv = "LeakCanary-BadWGPriv-not-base64"
	)
	wgPriv, wgPSK, wgPeer := fakeKey(t), fakeKey(t), fakeKey(t)
	wgPriv2 := fakeKey(t)

	e := newTestEnv(t)
	l := &leakRecorder{a: &authed{testEnv: e}}

	// Unauthenticated / setup / login paths, including failures.
	l.call("GET", "/api/endpoints", "")
	l.call("POST", "/api/login", `{"password":"`+adminPW+`"}`) // before setup: 409
	if rec := l.call("POST", "/api/setup", `{"password":"`+adminPW+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	l.call("POST", "/api/setup", `{"password":"`+adminPW+`"}`)        // 409 already set up
	l.call("POST", "/api/login", `{"password":"`+adminPW+`-wrong"}`)  // 401
	rec := l.call("POST", "/api/login", `{"password":"`+adminPW+`"}`) // 200
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	l.a.cookie = sessionCookie(t, rec)
	var lb struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lb); err != nil {
		t.Fatal(err)
	}
	l.a.csrf = lb.CSRFToken

	// Missing CSRF on a write with a secret in the body.
	l.call("POST", "/api/endpoints", socksBody("nocsrf", "h.example", socksPW), withCookie(l.a.cookie))

	s5 := l.mustCreate(socksBody("p-socks", "proxy.example", socksPW))
	hp := l.mustCreate(fmt.Sprintf(`{"name":"p-http","kind":"http","host":"203.0.113.5","port":3128,"username":"u","password":%q}`, httpPW))
	wg := l.mustCreate(wgBody("se-got-wg-001", wgPriv, wgPSK, wgPeer))

	// Invalid inputs carrying secrets: validation errors must not echo them.
	l.authedCall("POST", "/api/endpoints", wgBody("bad-wg", badWGPriv, wgPSK, wgPeer))
	l.authedCall("POST", "/api/endpoints", fmt.Sprintf(`{"name":"","kind":"socks5","host":"","port":0,"password":%q}`, socksPW2))
	l.authedCall("PUT", "/api/endpoints/"+wg, wgBody("se-got-wg-001", badWGPriv, wgPSK, wgPeer))
	l.authedCall("POST", "/api/endpoints", `{"password":"`+socksPW2+`",`) // malformed JSON

	// Updates: omitted secret keeps stored; new secret replaces.
	l.authedCall("PUT", "/api/endpoints/"+s5, `{"name":"p-socks","kind":"socks5","host":"proxy.example","port":1080,"username":"u"}`)
	l.authedCall("PUT", "/api/endpoints/"+wg, wgBody("se-got-wg-001", wgPriv2, wgPSK, wgPeer))

	slotPut := func(id int, route []string, pw string) *httptest.ResponseRecorder {
		r, _ := json.Marshal(route)
		return l.authedCall("PUT", fmt.Sprintf("/api/slots/%d", id), fmt.Sprintf(
			`{"name":"s%d","enabled":true,"bind_addr":"127.0.0.1","port":%d,"route":%s,"killswitch":"block",`+
				`"socks_auth":{"username":"slotuser","password":%q}}`, id, 1080+id, r, pw))
	}
	if rec := slotPut(1, []string{wg, s5, hp}, slotPW); rec.Code != http.StatusOK {
		t.Fatalf("PUT slot: %d %s", rec.Code, rec.Body)
	}
	slotPut(2, []string{"no-such-endpoint"}, slotPW) // invalid route: 400
	l.authedCall("PUT", "/api/slots/1", `{"name":"s1","enabled":true,"bind_addr":"127.0.0.1","port":1081,`+
		`"route":[],"killswitch":"block","socks_auth":{"username":"slotuser"}}`) // keep stored password
	l.authedCall("DELETE", "/api/endpoints/"+s5, "") // 409 in use? either way captured

	// Import: conf + zip carrying distinctive keys, a duplicate and a malformed
	// conf that still contains a private key. Neither key may be echoed anywhere.
	imp := l.sweepImport()

	// Status: upstream error text that echoes every stored secret must come out
	// redacted from GET /api/slots, GET /api/slots/{id} and the SSE snapshot.
	echo := "upstream echoed " + strings.Join([]string{wgPriv2, wgPSK, httpPW, slotPW,
		e.store.Get().Settings.AdminPasswordHash, e.store.Get().Settings.SessionSecret}, " ")
	e.ctrl.setStatus(1, supervisor.StateDegraded, "kill switch: "+echo, time.Now(), &supervisor.ProbeStatus{
		CheckedAt: time.Now(), Error: echo})
	e.ctrl.setStatus(2, supervisor.StateError, "build route: "+echo, time.Now(), nil)
	l.recordSnapshot(e)

	// GET every endpoint/slot/settings route.
	l.authedCall("GET", "/api/endpoints", "")
	for _, id := range append([]string{s5, hp, wg}, imp.ids...) {
		l.authedCall("GET", "/api/endpoints/"+id, "")
	}
	l.authedCall("GET", "/api/slots", "")
	for id := 1; id <= 4; id++ {
		l.authedCall("GET", fmt.Sprintf("/api/slots/%d", id), "")
		l.authedCall("POST", fmt.Sprintf("/api/slots/%d/start", id), "")
		l.authedCall("POST", fmt.Sprintf("/api/slots/%d/stop", id), "")
	}
	l.authedCall("GET", "/api/settings", "")
	l.authedCall("PUT", "/api/settings", `{"ui_bind_addr":"127.0.0.1:8080","admin_password_hash":"x","session_secret":"y"}`)
	l.authedCall("GET", "/api/settings", "")

	// Change password: neither password, hash nor session secret (old or new) may leak.
	const newPW = "LeakCanary-NewAdminPW-4d8b21"
	oldSet := e.store.Get().Settings
	l.authedCall("PUT", "/api/settings", `{"ui_bind_addr":"127.0.0.1:8080","current_password":"`+adminPW+`-wrong","new_password":"`+newPW+`"}`) // 400
	cp := l.authedCall("PUT", "/api/settings", `{"ui_bind_addr":"127.0.0.1:8080","current_password":"`+adminPW+`","new_password":"`+newPW+`"}`)
	if cp.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", cp.Code, cp.Body)
	}
	var cb struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(cp.Body.Bytes(), &cb); err != nil || cb.CSRFToken == "" {
		t.Fatalf("change password body %q: %v", cp.Body, err)
	}
	l.a.csrf = cb.CSRFToken
	l.call("POST", "/api/login", `{"password":"`+adminPW+`"}`) // old password: 401
	l.call("POST", "/api/login", `{"password":"`+newPW+`"}`)   // new password: 200
	l.authedCall("POST", "/api/logout", "")
	l.authedCall("GET", "/api/endpoints", "") // 401 after logout
	if strings.Contains(e.logs.String(), cb.CSRFToken) {
		t.Error("new csrf token leaked in logs")
	}

	cfg := e.store.Get()
	secrets := map[string]string{
		"admin password":      adminPW,
		"admin argon2 hash":   cfg.Settings.AdminPasswordHash,
		"session secret":      cfg.Settings.SessionSecret,
		"session token":       l.a.cookie.Value,
		"new admin password":  newPW,
		"old admin hash":      oldSet.AdminPasswordHash,
		"old session secret":  oldSet.SessionSecret,
		"socks5 password":     socksPW,
		"socks5 password (2)": socksPW2,
		"http password":       httpPW,
		"slot socks password": slotPW,
		"wg private key":      wgPriv,
		"wg private key (2)":  wgPriv2,
		"wg preshared key":    wgPSK,
		"invalid wg priv":     badWGPriv,
	}
	for i, k := range imp.secrets {
		secrets[fmt.Sprintf("imported key %d", i)] = k
	}
	// Sanity: the stored config really holds the secrets we are hunting for.
	for _, name := range []string{"admin argon2 hash", "session secret", "session token"} {
		if len(secrets[name]) < 16 {
			t.Fatalf("%s unexpectedly short/empty: %q", name, secrets[name])
		}
	}
	if sa := cfg.Slots; len(sa) == 0 || sa[0].SocksAuth == nil || sa[0].SocksAuth.Password != slotPW {
		t.Fatalf("slot password not stored: %+v", sa)
	}

	// The CSRF token is legitimately returned by login, but must never be logged.
	if strings.Contains(e.logs.String(), lb.CSRFToken) || len(lb.CSRFToken) < 16 {
		t.Error("csrf token leaked in logs (or unexpectedly short)")
	}
	if n := countWireguard(cfg.Endpoints, imp.secrets); n != len(imp.ids) || n < 3 {
		t.Fatalf("stored imported keys = %d, want %d (>=3): sweep did not import", n, len(imp.ids))
	}

	haystacks := map[string]string{"responses": l.bodies.String(), "logs": e.logs.String()}
	for where, text := range haystacks {
		for name, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s leaked in %s", name, where)
			}
		}
	}
	if !strings.Contains(l.bodies.String(), `"password_set":true`) {
		t.Error("expected password_set:true in responses (sweep did not exercise secrets)")
	}
}
