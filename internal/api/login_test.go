package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginCorrectPassword(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	rec := e.do("POST", "/api/login", `{"password":"hunter22-long"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	sc := rec.Header().Get("Set-Cookie")
	for _, want := range []string{sessionCookieName + "=", "HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(sc, want) {
			t.Errorf("Set-Cookie %q missing %q", sc, want)
		}
	}
	if strings.Contains(sc, "Secure") {
		t.Errorf("Set-Cookie %q sets Secure; LAN HTTP needs it off", sc)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["csrf_token"] == "" {
		t.Fatalf("body %q err %v", rec.Body, err)
	}
	cookie := sessionCookie(t, rec)
	if body["csrf_token"] == cookie.Value {
		t.Fatal("csrf token equals session token")
	}
	logs := e.logs.String()
	for _, secret := range []string{"hunter22-long", cookie.Value, body["csrf_token"]} {
		if strings.Contains(logs, secret) {
			t.Fatal("secret leaked into logs")
		}
	}
}

func TestLoginWrongPassword(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	rec := e.do("POST", "/api/login", `{"password":"nope-nope-nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("cookie set on failed login")
	}
}

func TestLoginBeforeSetup(t *testing.T) {
	e := newTestEnv(t)
	rec := e.do("POST", "/api/login", `{"password":"hunter22-long"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
}

func TestLoginBadBody(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	if rec := e.do("POST", "/api/login", `{{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// TestCookieSecureOnlyWithTLS: the session cookie (set and cleared) carries Secure
// exactly when the request arrived over TLS; HttpOnly and SameSite=Strict always.
func TestCookieSecureOnlyWithTLS(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newServer  func(http.Handler) *httptest.Server
		wantSecure bool
	}{
		{"tls", httptest.NewTLSServer, true},
		{"plain", httptest.NewServer, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.setup("hunter22-long")
			ts := tc.newServer(e.h)
			defer ts.Close()

			res := postJSON(t, ts, "/api/login", `{"password":"hunter22-long"}`, nil, "")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("login status %d", res.StatusCode)
			}
			checkCookieAttrs(t, "login", res, tc.wantSecure)
			login := sessionFromResponse(t, res)
			csrf := csrfToken(e.store.Get().Settings.SessionSecret, login.Value)

			res = postJSON(t, ts, "/api/logout", "", login, csrf)
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("logout status %d", res.StatusCode)
			}
			checkCookieAttrs(t, "logout", res, tc.wantSecure)
		})
	}
}

func postJSON(t *testing.T, ts *httptest.Server, path, body string, c *http.Cookie, csrf string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c != nil {
		req.AddCookie(c)
		req.Header.Set(csrfHeader, csrf)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	res.Body.Close()
	return res
}

func sessionFromResponse(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie", sessionCookieName)
	return nil
}

func checkCookieAttrs(t *testing.T, what string, res *http.Response, wantSecure bool) {
	t.Helper()
	c := sessionFromResponse(t, res)
	if c.Secure != wantSecure {
		t.Errorf("%s cookie Secure=%v, want %v (raw %q)", what, c.Secure, wantSecure, c.Raw)
	}
	if !c.HttpOnly {
		t.Errorf("%s cookie missing HttpOnly (raw %q)", what, c.Raw)
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("%s cookie SameSite=%v, want Strict (raw %q)", what, c.SameSite, c.Raw)
	}
}
