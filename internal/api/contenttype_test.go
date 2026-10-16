package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doCT posts body with an explicit Content-Type ("" sends none).
func (e *testEnv) doCT(method, path, body, ctype string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

var badContentTypes = []string{"text/plain", "", "application/x-www-form-urlencoded", "text/json", "application/jsonx"}

// A no-cors cross-site POST can only send simple Content-Types.
func TestSetupRefusesNonJSONContentType(t *testing.T) {
	for _, ct := range badContentTypes {
		t.Run("ct="+ct, func(t *testing.T) {
			e := newTestEnv(t)
			rec := e.doCT("POST", "/api/setup", `{"password":"hunter22-long"}`, ct)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status %d, want 415", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("body %q is not the error shape", rec.Body)
			}
			if h := e.store.Get().Settings.AdminPasswordHash; h != "" {
				t.Fatal("admin hash was set")
			}
		})
	}
}

func TestJSONEndpointsRefuseNonJSONContentType(t *testing.T) {
	a := newAuthed(t)
	epID, _ := a.create(`{"name":"p","kind":"socks5","host":"h","port":1080}`)["id"].(string)
	cases := []struct{ method, path, body string }{
		{"POST", "/api/login", `{"password":"x"}`},
		{"POST", "/api/endpoints", `{"name":"x","kind":"socks5","host":"h","port":1080}`},
		{"PUT", "/api/endpoints/" + epID, `{"name":"x"}`},
		{"PUT", "/api/slots/1", `{}`},
		{"PUT", "/api/settings", `{}`},
		{"POST", "/api/config/export", `{}`},
	}
	for _, c := range cases {
		for _, ct := range []string{"text/plain", ""} {
			rec := a.doCT(c.method, c.path, c.body, ct, withCookie(a.cookie), withCSRF(a.csrf))
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s ct=%q: status %d, want 415", c.method, c.path, ct, rec.Code)
			}
		}
	}
}

func TestJSONContentTypeParamsAndCaseAccepted(t *testing.T) {
	for _, ct := range []string{"application/json; charset=utf-8", "Application/JSON"} {
		e := newTestEnv(t)
		rec := e.doCT("POST", "/api/setup", `{"password":"hunter22-long"}`, ct)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("ct %q: status %d body %s", ct, rec.Code, rec.Body)
		}
	}
}
