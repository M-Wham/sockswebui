package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// omitSecrets simulates a no-secrets config import by clearing secrets in the store
// and recording their names; there is no API that writes the list.
func omitSecrets(t *testing.T, a *authed, epID string, slotID int, epNames, slotNames []string) {
	t.Helper()
	err := a.store.Update(func(c *config.Config) error {
		for i := range c.Endpoints {
			e := &c.Endpoints[i]
			if e.ID != epID {
				continue
			}
			for _, n := range epNames {
				switch n {
				case config.OmittedPrivateKey:
					e.PrivateKey = ""
				case config.OmittedPresharedKey:
					e.PresharedKey = ""
				case config.OmittedPassword:
					e.Password = ""
				}
			}
			e.SecretsOmitted = epNames
		}
		for i := range c.Slots {
			if c.Slots[i].ID == slotID && len(slotNames) > 0 {
				c.Slots[i].SocksAuth.Password = ""
				c.Slots[i].SecretsOmitted = slotNames
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("omit secrets: %v", err)
	}
}

func storedEP(t *testing.T, st *config.Store, id string) config.Endpoint {
	t.Helper()
	cfg := st.Get()
	i := endpointIndex(cfg.Endpoints, id)
	if i < 0 {
		t.Fatalf("endpoint %s not stored", id)
	}
	return cfg.Endpoints[i]
}

func omittedOf(m map[string]any) []string {
	raw, _ := m["secrets_omitted"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// noSecrets fails if any recorded response body contains one of secrets.
func noSecrets(t *testing.T, bodies []string, secrets ...string) {
	t.Helper()
	for _, b := range bodies {
		for _, s := range secrets {
			if s != "" && strings.Contains(b, s) {
				t.Fatalf("response leaks a secret: %s", b)
			}
		}
	}
}

func TestSecretsOmittedEndpointKeepClear(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 1, false)
	omitSecrets(t, a, epID, 0, []string{config.OmittedPrivateKey, config.OmittedPresharedKey}, nil)
	peer := fakeKey(t)
	var bodies []string
	put := func(extra string) map[string]any {
		t.Helper()
		body := fmt.Sprintf(`{"name":"wg","kind":"wireguard","peer_public_key":%q,"addresses":["10.64.0.2/32"],`+
			`"endpoint":"192.0.2.10:51820","allowed_ips":["0.0.0.0/0"]%s}`, peer, extra)
		rec := a.call("PUT", "/api/endpoints/"+epID, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", extra, rec.Code, rec.Body)
		}
		bodies = append(bodies, rec.Body.String())
		return decodeEndpoint(t, rec)
	}
	want := func(m map[string]any, names ...string) {
		t.Helper()
		if got := omittedOf(m); !slices.Equal(got, names) {
			t.Fatalf("response secrets_omitted = %v, want %v", got, names)
		}
		if got := storedEP(t, a.store, epID).SecretsOmitted; !slices.Equal(got, names) && len(got)+len(names) > 0 {
			t.Fatalf("stored secrets_omitted = %v, want %v", got, names)
		}
	}

	// A body carrying secrets_omitted is ignored: the stored list wins.
	want(put(`,"secrets_omitted":[]`), config.OmittedPrivateKey, config.OmittedPresharedKey)
	// Leaving a listed secret empty keeps its name.
	want(put(`,"preshared_key":""`), config.OmittedPrivateKey, config.OmittedPresharedKey)
	// Setting one removes just that name.
	priv, psk := fakeKey(t), fakeKey(t)
	want(put(fmt.Sprintf(`,"private_key":%q`, priv)), config.OmittedPresharedKey)
	// Re-entering the rest empties the list; the field is absent from JSON.
	m := put(fmt.Sprintf(`,"preshared_key":%q`, psk))
	want(m)
	if _, ok := m["secrets_omitted"]; ok {
		t.Fatalf("empty list present in response: %v", m)
	}
	if raw := a.call("GET", "/api/endpoints/"+epID, "").Body.String(); strings.Contains(raw, "secrets_omitted") {
		t.Fatalf("empty list present in GET: %s", raw)
	}
	noSecrets(t, bodies, priv, psk)
}

func TestSecretsOmittedPostBodyIgnored(t *testing.T) {
	a := newAuthed(t)
	m := a.create(`{"name":"p","kind":"socks5","host":"gw.example","port":1080,"secrets_omitted":["password"]}`)
	if got := omittedOf(m); len(got) != 0 {
		t.Fatalf("POST body list honoured: %v", got)
	}
	if got := storedEP(t, a.store, m["id"].(string)).SecretsOmitted; len(got) != 0 {
		t.Fatalf("stored list from POST body: %v", got)
	}
}

func TestSecretsOmittedPasswordHasSession(t *testing.T) {
	a := newAuthed(t)
	const pw = "pw-fixed-{session}-Kq7"
	m := a.create(sessBody("acct-user", pw, sess10))
	id := m["id"].(string)
	cur := sessionOf(m)["current_id"]
	omitSecrets(t, a, id, 0, []string{config.OmittedPassword}, nil)

	var bodies []string
	get := a.call("GET", "/api/endpoints/"+id, "")
	bodies = append(bodies, get.Body.String())
	g := decodeEndpoint(t, get)
	if g["password_has_session"] != true || g["password_set"] != false {
		t.Fatalf("GET flags: %v", g)
	}
	// A PUT that omits the password keeps the list, the session and password_has_session.
	rec := a.call("PUT", "/api/endpoints/"+id, sessBody("acct-user", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	bodies = append(bodies, rec.Body.String())
	p := decodeEndpoint(t, rec)
	if p["password_has_session"] != true || sessionOf(p)["current_id"] != cur ||
		!slices.Equal(omittedOf(p), []string{config.OmittedPassword}) {
		t.Fatalf("PUT keep: %v", p)
	}
	// Re-entering the password clears the list.
	const pw2 = "pw2-fixed-{session}-Zz3"
	p = putEP(a, id, sessBody("acct-user", pw2, ""))
	if len(omittedOf(p)) != 0 || p["password_has_session"] != true || sessionOf(p)["current_id"] != cur {
		t.Fatalf("PUT re-enter: %v", p)
	}
	noSecrets(t, bodies, "pw-fixed-", "pw2-fixed-")
}

func TestSecretsOmittedSlotKeepClear(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 1, false)
	omitSecrets(t, a, epID, 1, nil, []string{config.OmittedSocksPassword})
	body := func(enabled bool, auth string) string {
		return fmt.Sprintf(`{"name":"s1","enabled":%t,"bind_addr":"127.0.0.1","port":1081,"route":[%q],`+
			`"killswitch":"block","socks_auth":%s,"secrets_omitted":[]}`, enabled, epID, auth)
	}
	put := func(b string, code int) *httptest.ResponseRecorder {
		t.Helper()
		rec := a.call("PUT", "/api/slots/1", b)
		if rec.Code != code {
			t.Fatalf("PUT slot: %d %s, want %d", rec.Code, rec.Body, code)
		}
		return rec
	}

	// Leaving the password empty keeps the name; the body list is ignored.
	rec := put(body(false, `{"username":"u"}`), http.StatusOK)
	if got := storedSlot(t, a.store, 1).SecretsOmitted; !slices.Equal(got, []string{config.OmittedSocksPassword}) {
		t.Fatalf("stored list = %v", got)
	}
	if got := omittedOf(decodeEndpoint(t, rec)); !slices.Equal(got, []string{config.OmittedSocksPassword}) {
		t.Fatalf("response list = %v", got)
	}
	// Enabling is rejected while secrets are missing.
	rec = put(body(true, `{"username":"u"}`), http.StatusBadRequest)
	fe := decodeFieldErrors(t, rec)
	if len(fe) == 0 || fe[0].Field != "enabled" || !strings.Contains(fe[0].Message, "secrets missing") {
		t.Fatalf("enable errors: %v", fe)
	}
	// Re-entering the password clears it.
	const pw = "new-socks-pw-Ab9"
	rec = put(body(false, fmt.Sprintf(`{"username":"u","password":%q}`, pw)), http.StatusOK)
	if got := storedSlot(t, a.store, 1).SecretsOmitted; len(got) != 0 {
		t.Fatalf("stored list after re-entry = %v", got)
	}
	if strings.Contains(rec.Body.String(), "secrets_omitted") || strings.Contains(rec.Body.String(), pw) {
		t.Fatalf("response after re-entry: %s", rec.Body)
	}
}

func TestSecretsOmittedStart409(t *testing.T) {
	cases := []struct {
		name             string
		epNames, slNames []string
	}{
		{"slot list", nil, []string{config.OmittedSocksPassword}},
		{"route endpoint list", []string{config.OmittedPrivateKey}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAuthed(t)
			epID, priv := slotFixture(t, a, 1, false)
			omitSecrets(t, a, epID, 1, tc.epNames, tc.slNames)
			rec := a.call("POST", "/api/slots/1/start", "")
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "secrets missing") {
				t.Fatalf("start: %d %s", rec.Code, rec.Body)
			}
			if starts, _, _ := a.ctrl.calls(); len(starts) != 0 {
				t.Fatalf("ctrl.Start called: %v", starts)
			}
			if storedSlot(t, a.store, 1).Enabled {
				t.Fatal("slot enabled after 409")
			}
			noSecrets(t, []string{rec.Body.String()}, priv, testSocksPass)
		})
	}
}

func TestSecretsOmittedKindSwitchKeepsPassword(t *testing.T) {
	a := newAuthed(t)
	m := a.create(sessBody("acct-user", "pw-Kind-Sw1", ""))
	id := m["id"].(string)
	rec := a.call("PUT", "/api/slots/1", fmt.Sprintf(`{"name":"s1","enabled":false,"bind_addr":"127.0.0.1",`+
		`"port":1081,"route":[%q],"killswitch":"allow-direct"}`, id))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT slot: %d %s", rec.Code, rec.Body)
	}
	omitSecrets(t, a, id, 0, []string{config.OmittedPassword}, nil)

	p := putEP(a, id, `{"name":"p","kind":"http","host":"gw.example","port":8080,"username":"acct-user"}`)
	if got := omittedOf(p); !slices.Equal(got, []string{config.OmittedPassword}) {
		t.Fatalf("socks5->http response list = %v", got)
	}
	if got := storedEP(t, a.store, id).SecretsOmitted; !slices.Equal(got, []string{config.OmittedPassword}) {
		t.Fatalf("socks5->http stored list = %v", got)
	}
	rec = a.call("PUT", "/api/slots/1", fmt.Sprintf(`{"name":"s1","enabled":true,"bind_addr":"127.0.0.1",`+
		`"port":1081,"route":[%q],"killswitch":"allow-direct"}`, id))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "secrets missing") {
		t.Fatalf("enable after kind switch: %d %s", rec.Code, rec.Body)
	}
	// A name that does not exist for the new kind is dropped.
	p = putEP(a, id, wgBody("wg", fakeKey(t), "", fakeKey(t)))
	if got := omittedOf(p); len(got) != 0 {
		t.Fatalf("http->wireguard list = %v", got)
	}
}
