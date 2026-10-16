package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func sessBody(user, pass string, extra string) string {
	b := `{"name":"p","kind":"socks5","host":"gw.example","port":1080,"username":"` + user + `"`
	if pass != "" {
		b += `,"password":"` + pass + `"`
	}
	return b + extra + `}`
}

const sess10 = `,"session":{"charset":"alnum","length":10}`

func sessionOf(m map[string]any) map[string]any {
	s, _ := m["session"].(map[string]any)
	return s
}

func putEP(a *authed, id, body string) map[string]any {
	a.t.Helper()
	rec := a.call("PUT", "/api/endpoints/"+id, body)
	if rec.Code != http.StatusOK {
		a.t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	return decodeEndpoint(a.t, rec)
}

func TestEndpointSession(t *testing.T) {
	a := newAuthed(t)
	m := a.create(sessBody("u-{session}", "", sess10))
	id := m["id"].(string)
	cur := sessionOf(m)["current_id"].(string)
	if len(cur) != 10 {
		t.Fatalf("current_id %q", cur)
	}

	// body current_id is ignored
	m2 := a.create(sessBody("u-{session}", "", `,"session":{"charset":"alnum","length":10,"current_id":"AAAAAAAAAA"}`))
	if sessionOf(m2)["current_id"] == "AAAAAAAAAA" {
		t.Error("body current_id honoured")
	}

	// PUT omitting session keeps everything
	m = putEP(a, id, sessBody("u-{session}", "", ""))
	if s := sessionOf(m); s["current_id"] != cur || s["length"] != float64(10) || s["charset"] != "alnum" {
		t.Errorf("omitted session: %v", s)
	}
	// PUT omitting charset/length keeps stored
	m = putEP(a, id, sessBody("u-{session}", "", `,"session":{}`))
	if s := sessionOf(m); s["current_id"] != cur || s["length"] != float64(10) {
		t.Errorf("empty session: %v", s)
	}
	// fixed text edit keeps; token move to password keeps
	m = putEP(a, id, sessBody("other-{session}", "", ""))
	if sessionOf(m)["current_id"] != cur {
		t.Error("fixed text edit changed id")
	}
	m = putEP(a, id, sessBody("u", "pw-{session}", ""))
	if sessionOf(m)["current_id"] != cur || m["password_has_session"] != true {
		t.Errorf("move to password: %v", m)
	}
	// omitted password uses stored for token count and keeps id
	m = putEP(a, id, sessBody("u2", "", ""))
	if sessionOf(m)["current_id"] != cur {
		t.Errorf("omitted password: %v", m)
	}
	// length change regenerates
	m = putEP(a, id, sessBody("u2", "", `,"session":{"length":8}`))
	c8, _ := sessionOf(m)["current_id"].(string)
	if len(c8) != 8 {
		t.Errorf("length 8: %q", c8)
	}
	// charset change regenerates
	m = putEP(a, id, sessBody("u2", "", `,"session":{"charset":"digits"}`))
	cd, _ := sessionOf(m)["current_id"].(string)
	if len(cd) != 8 || strings.Trim(cd, "0123456789") != "" || cd[0] == '0' {
		t.Errorf("digits: %q", cd)
	}
	// removing the token deletes session
	m = putEP(a, id, sessBody("u2", "plain", ""))
	if _, ok := m["session"]; ok {
		t.Errorf("session remained: %v", m)
	}
	rec := a.call("GET", "/api/endpoints/"+id, "")
	if strings.Contains(rec.Body.String(), `"session"`) {
		t.Errorf("GET shows session: %s", rec.Body)
	}
}

func TestEndpointSessionReaderFailure(t *testing.T) {
	a := newAuthed(t)
	a.srv.rand = failReader{}
	rec := a.call("POST", "/api/endpoints", sessBody("u-{session}", "", sess10))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if n := len(a.store.Get().Endpoints); n != 0 {
		t.Errorf("config changed: %d endpoints", n)
	}
}

func TestEndpointSessionLeak(t *testing.T) {
	a := newAuthed(t)
	m := a.create(sessBody("u", "SECRETPW-{session}", sess10))
	a.create(sessBody("u", "plainSECRETPW", ""))
	if m["password_has_session"] != true || m["password_set"] != true {
		t.Errorf("flags: %v", m)
	}
	for _, p := range []string{"/api/endpoints", "/api/endpoints/" + m["id"].(string)} {
		if body := a.call("GET", p, "").Body.String(); strings.Contains(body, "SECRETPW") {
			t.Errorf("%s leaks password: %s", p, body)
		}
	}
	list := a.call("GET", "/api/endpoints", "").Body.String()
	if strings.Count(list, `"password_has_session":true`) != 1 || strings.Count(list, `"password_has_session":false`) != 1 {
		t.Errorf("flags in list: %s", list)
	}
}

func TestEndpointSessionValidation(t *testing.T) {
	a := newAuthed(t)
	rec := a.call("POST", "/api/endpoints", sessBody("USERNAME-{session}", "PWTEXT-{session}", sess10))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if b := rec.Body.String(); strings.Contains(b, "USERNAME") || strings.Contains(b, "PWTEXT") {
		t.Errorf("echoes credentials: %s", b)
	}
	rec = a.call("POST", "/api/endpoints", sessBody("USERNAME-{session}", "PWTEXT-{session}", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("two tokens, no session: %d", rec.Code)
	}
}

func TestEndpointSessionDefaultsWhenOmitted(t *testing.T) {
	a := newAuthed(t)
	m := a.create(sessBody("u-{session}", "", ""))
	s := sessionOf(m)
	if s["charset"] != "alnum" || s["length"] != float64(10) {
		t.Fatalf("defaults: %v", s)
	}
	if id, _ := s["current_id"].(string); len(id) != 10 {
		t.Errorf("current_id: %v", s)
	}
	// PUT adding the token to a plain endpoint also introduces a session.
	p := a.create(sessBody("plain", "", ""))
	m = putEP(a, p["id"].(string), sessBody("u-{session}", "", ""))
	if id, _ := sessionOf(m)["current_id"].(string); len(id) != 10 {
		t.Errorf("PUT introducing token: %v", m)
	}
}

func TestEndpointSessionReaderFailureUpdate(t *testing.T) {
	a := newAuthed(t)
	m := a.create(sessBody("u-{session}", "", sess10))
	id := m["id"].(string)
	cur := sessionOf(m)["current_id"]
	a.srv.rand = failReader{}
	rec := a.call("PUT", "/api/endpoints/"+id, sessBody("u-{session}", "", `,"session":{"length":8}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	got := a.store.Get().Endpoints[0].Session
	if got.CurrentID != cur || got.Length != 10 {
		t.Errorf("stored changed: %+v", got)
	}
}
