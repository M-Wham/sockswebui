package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// fakeKey returns a freshly generated base64 32-byte key (never a real WireGuard key).
func fakeKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

type authed struct {
	*testEnv
	cookie *http.Cookie
	csrf   string
}

func newAuthed(t *testing.T) *authed {
	t.Helper()
	e, c, csrf := loggedIn(t)
	return &authed{testEnv: e, cookie: c, csrf: csrf}
}

func (a *authed) call(method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(method, path, body, withCookie(a.cookie), withCSRF(a.csrf))
}

func decodeEndpoint(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode endpoint %q: %v", rec.Body, err)
	}
	return m
}

func (a *authed) create(body string) map[string]any {
	a.t.Helper()
	rec := a.call("POST", "/api/endpoints", body)
	if rec.Code != http.StatusCreated {
		a.t.Fatalf("create: status %d body %s", rec.Code, rec.Body)
	}
	m := decodeEndpoint(a.t, rec)
	if id, _ := m["id"].(string); id == "" {
		a.t.Fatalf("create: no server-assigned id in %s", rec.Body)
	}
	return m
}

func socksBody(name, host, password string) string {
	return fmt.Sprintf(`{"name":%q,"kind":"socks5","host":%q,"port":1080,"username":"u","password":%q}`, name, host, password)
}

func wgBody(name, priv, psk, peer string) string {
	return fmt.Sprintf(`{"name":%q,"kind":"wireguard","private_key":%q,"preshared_key":%q,`+
		`"peer_public_key":%q,"addresses":["10.64.0.2/32"],"dns":["10.64.0.1"],`+
		`"endpoint":"192.0.2.10:51820","allowed_ips":["0.0.0.0/0"],"mtu":1420}`, name, priv, psk, peer)
}

func TestEndpointsCRUD(t *testing.T) {
	a := newAuthed(t)

	s5 := a.create(socksBody("p1", "proxy.example", "pw-socks"))
	ht := a.create(`{"name":"h1","kind":"http","host":"203.0.113.5","port":3128}`)
	wg := a.create(wgBody("se-got-wg-001", fakeKey(t), fakeKey(t), fakeKey(t)))
	ids := []string{s5["id"].(string), ht["id"].(string), wg["id"].(string)}
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatalf("ids not unique: %v", ids)
	}
	if s5["kind"] != "socks5" || s5["host"] != "proxy.example" || s5["port"] != float64(1080) {
		t.Errorf("socks5 response: %v", s5)
	}
	if wg["endpoint"] != "192.0.2.10:51820" || wg["mtu"] != float64(1420) {
		t.Errorf("wireguard response: %v", wg)
	}

	// Client-supplied id is ignored.
	x := a.create(`{"id":"mine","name":"x","kind":"http","host":"h","port":80}`)
	if x["id"] == "mine" {
		t.Error("client-supplied id was honoured")
	}

	rec := a.call("GET", "/api/endpoints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Endpoints []map[string]any `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Endpoints) != 4 {
		t.Fatalf("list: %v, body %s", err, rec.Body)
	}

	rec = a.call("GET", "/api/endpoints/"+ids[2], "")
	if rec.Code != http.StatusOK || decodeEndpoint(t, rec)["name"] != "se-got-wg-001" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	rec = a.call("PUT", "/api/endpoints/"+ids[0], `{"name":"p1b","kind":"socks5","host":"other.example","port":9050}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	got := decodeEndpoint(t, rec)
	if got["id"] != ids[0] || got["name"] != "p1b" || got["host"] != "other.example" || got["port"] != float64(9050) {
		t.Errorf("put response: %v", got)
	}
	if st := a.store.Get().Endpoints[0]; st.Host != "other.example" || st.Name != "p1b" {
		t.Errorf("store after put: %+v", st)
	}

	rec = a.call("DELETE", "/api/endpoints/"+ids[1], "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if n := len(a.store.Get().Endpoints); n != 3 {
		t.Errorf("store has %d endpoints after delete, want 3", n)
	}

	for _, m := range []string{"GET", "PUT", "DELETE"} {
		body := ""
		if m == "PUT" {
			body = `{"name":"n","kind":"http","host":"h","port":80}`
		}
		if rec := a.call(m, "/api/endpoints/"+ids[1], body); rec.Code != http.StatusNotFound {
			t.Errorf("%s deleted id: %d, want 404", m, rec.Code)
		}
		if rec := a.call(m, "/api/endpoints/nope", body); rec.Code != http.StatusNotFound {
			t.Errorf("%s unknown id: %d, want 404", m, rec.Code)
		}
	}
}

func TestEndpointsCRUDRequiresAuth(t *testing.T) {
	a := newAuthed(t)
	if rec := a.do("GET", "/api/endpoints", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("list without cookie: %d", rec.Code)
	}
	rec := a.do("POST", "/api/endpoints", socksBody("p", "h", "x"), withCookie(a.cookie))
	if rec.Code != http.StatusForbidden {
		t.Errorf("create without CSRF: %d", rec.Code)
	}
	if n := len(a.store.Get().Endpoints); n != 0 {
		t.Errorf("store has %d endpoints", n)
	}
}

func TestEndpointSecretsWriteOnly(t *testing.T) {
	a := newAuthed(t)
	const socksPW, httpPW = "s0cks-secret-pw", "http-secret-pw"
	priv, psk, peer := fakeKey(t), fakeKey(t), fakeKey(t)
	secrets := []string{socksPW, httpPW, priv, psk}
	check := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(rec.Body.String(), s) {
				t.Errorf("%s: response contains a secret value", what)
			}
		}
	}
	post := func(body string) map[string]any {
		t.Helper()
		rec := a.call("POST", "/api/endpoints", body)
		check("POST", rec)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		return decodeEndpoint(t, rec)
	}

	s5 := post(socksBody("p", "h.example", socksPW))
	ht := post(`{"name":"h","kind":"http","host":"h2","port":8080,"username":"u","password":"` + httpPW + `"}`)
	wg := post(wgBody("wg", priv, psk, peer))
	if s5["password_set"] != true || ht["password_set"] != true {
		t.Errorf("password_set: socks5=%v http=%v", s5["password_set"], ht["password_set"])
	}
	if wg["private_key_set"] != true || wg["preshared_key_set"] != true {
		t.Errorf("wg key flags: %v", wg)
	}
	if wg["peer_public_key"] != peer {
		t.Errorf("peer public key (not a secret) should be returned")
	}
	for _, k := range []string{"password", "private_key", "preshared_key"} {
		for _, m := range []map[string]any{s5, ht, wg} {
			if _, ok := m[k]; ok {
				t.Errorf("response has secret field %q", k)
			}
		}
	}
	s5id, wgid := s5["id"].(string), wg["id"].(string)

	check("GET list", a.call("GET", "/api/endpoints", ""))
	for _, id := range []string{s5id, ht["id"].(string), wgid} {
		check("GET one", a.call("GET", "/api/endpoints/"+id, ""))
	}

	// PUT omitting secrets keeps them.
	rec := a.call("PUT", "/api/endpoints/"+s5id, `{"name":"p2","kind":"socks5","host":"h.example","port":1080,"username":"u"}`)
	check("PUT keep", rec)
	if rec.Code != http.StatusOK || decodeEndpoint(t, rec)["password_set"] != true {
		t.Fatalf("PUT keep: %d %s", rec.Code, rec.Body)
	}
	rec = a.call("PUT", "/api/endpoints/"+wgid, `{"name":"wg2","kind":"wireguard","peer_public_key":"`+peer+`","endpoint":"192.0.2.10:51820"}`)
	check("PUT wg keep", rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT wg keep: %d %s", rec.Code, rec.Body)
	}
	cfg := a.store.Get()
	if cfg.Endpoints[0].Password != socksPW || cfg.Endpoints[2].PrivateKey != priv || cfg.Endpoints[2].PresharedKey != psk {
		t.Fatal("stored secrets not kept on PUT without secrets")
	}

	// PUT with explicit empty string clears.
	rec = a.call("PUT", "/api/endpoints/"+s5id, `{"name":"p2","kind":"socks5","host":"h.example","port":1080,"password":""}`)
	check("PUT clear", rec)
	if rec.Code != http.StatusOK || decodeEndpoint(t, rec)["password_set"] != false {
		t.Fatalf("PUT clear: %d %s", rec.Code, rec.Body)
	}
	rec = a.call("PUT", "/api/endpoints/"+wgid, `{"name":"wg2","kind":"wireguard","peer_public_key":"`+peer+`","endpoint":"192.0.2.10:51820","preshared_key":""}`)
	check("PUT wg clear", rec)
	m := decodeEndpoint(t, rec)
	if rec.Code != http.StatusOK || m["preshared_key_set"] != false || m["private_key_set"] != true {
		t.Fatalf("PUT wg clear: %d %s", rec.Code, rec.Body)
	}
	cfg = a.store.Get()
	if cfg.Endpoints[0].Password != "" || cfg.Endpoints[2].PresharedKey != "" || cfg.Endpoints[2].PrivateKey != priv {
		t.Fatal("explicit empty secret did not clear (or cleared the wrong one)")
	}

	// Invalid secret: the field error must not echo it.
	bad := "not-a-key-" + socksPW
	rec = a.call("PUT", "/api/endpoints/"+wgid, `{"name":"wg2","kind":"wireguard","peer_public_key":"`+peer+`","endpoint":"192.0.2.10:51820","private_key":"`+bad+`"}`)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), bad) {
		t.Fatalf("bad key: %d %s", rec.Code, rec.Body)
	}
	check("PUT bad key", rec)

	for _, s := range secrets {
		if strings.Contains(a.logs.String(), s) {
			t.Error("log contains a secret value")
		}
	}
}

func TestEndpointInvalid(t *testing.T) {
	a := newAuthed(t)
	ok := a.create(socksBody("p", "h.example", "pw"))
	id := ok["id"].(string)
	before := a.store.Get()

	cases := []struct{ name, method, path, body, field string }{
		{"socks5 without host", "POST", "/api/endpoints", `{"name":"x","kind":"socks5","port":1080}`, "host"},
		{"bad port", "POST", "/api/endpoints", `{"name":"x","kind":"http","host":"h","port":70000}`, "port"},
		{"bad kind", "POST", "/api/endpoints", `{"name":"x","kind":"ftp","host":"h","port":21}`, "kind"},
		{"wg without peer", "POST", "/api/endpoints", `{"name":"x","kind":"wireguard","endpoint":"192.0.2.1:51820"}`, "peer_public_key"},
		{"not json", "POST", "/api/endpoints", `{nope`, "body"},
		{"put without host", "PUT", "/api/endpoints/" + id, `{"name":"x","kind":"socks5","port":1080}`, "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := a.call(tc.method, tc.path, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body)
			}
			errs := decodeFieldErrors(t, rec)
			found := false
			for _, fe := range errs {
				found = found || (fe.Field == tc.field && fe.Message != "")
			}
			if !found {
				t.Errorf("errors %+v lack field %q", errs, tc.field)
			}
			if !reflect.DeepEqual(a.store.Get(), before) {
				t.Error("store changed after invalid request")
			}
		})
	}

	// Oversized body is rejected.
	huge := `{"name":"` + strings.Repeat("a", 2<<20) + `","kind":"http","host":"h","port":80}`
	if rec := a.call("POST", "/api/endpoints", huge); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d, want 400", rec.Code)
	}

	// Deleting an endpoint used in a slot route is a validation error; nothing persisted.
	err := a.store.Update(func(c *config.Config) error {
		c.Slots = append(c.Slots, config.Slot{ID: 1, Name: "s1", BindAddr: "127.0.0.1", Port: 1081,
			Route: []string{id}, Killswitch: config.KillswitchBlock, Health: config.DefaultHealth()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before = a.store.Get()
	rec := a.call("DELETE", "/api/endpoints/"+id, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete in-use: %d %s", rec.Code, rec.Body)
	}
	if errs := decodeFieldErrors(t, rec); len(errs) == 0 || errs[0].Field != "id" {
		t.Errorf("delete in-use errors: %+v", errs)
	}
	if !reflect.DeepEqual(a.store.Get(), before) {
		t.Error("store changed after rejected delete")
	}
	// Changing an in-use proxy to wireguard would break route rules only if order is wrong;
	// a single-hop route stays valid, so ensure PUT to an invalid kind is rejected here too.
	rec = a.call("PUT", "/api/endpoints/"+id, `{"name":"x","kind":"ftp"}`)
	if rec.Code != http.StatusBadRequest || !reflect.DeepEqual(a.store.Get(), before) {
		t.Errorf("put invalid kind on in-use endpoint: %d", rec.Code)
	}
}
