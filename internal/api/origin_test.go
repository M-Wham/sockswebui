package api

import (
	"net/http"
	"strings"
	"testing"
)

func withOrigin(o string) reqOpt { return func(r *http.Request) { r.Header.Set("Origin", o) } }
func withHost(h string) reqOpt   { return func(r *http.Request) { r.Host = h } }

const crossOriginBody = `"cross-origin request refused"`

func newOriginEnv(t *testing.T, names NameList) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	WithNameList(names)(e.srv)
	return e
}

func TestOriginCheckRefused(t *testing.T) {
	e := newOriginEnv(t, NewNameList("pibox", "ui.example"))
	e.setup("correct horse battery")
	for _, o := range []string{
		"https://evil.example", "null", "http://[::1", "not a url", "", " https://ui.example",
		"https://ui.example.evil.test", "https://evilui.example", "https://ui.example@evil.test",
		"https://evil.test#@ui.example", "https://pibox.local.evil.test",
	} {
		for _, m := range []string{"POST", "PUT", "DELETE"} {
			req := []reqOpt{withHost("10.0.0.5:8080")}
			if o != "" {
				req = append(req, withOrigin(o))
			} else {
				// An explicitly empty Origin header is present and malformed.
				req = append(req, func(r *http.Request) { r.Header["Origin"] = []string{""} })
			}
			rec := e.do(m, "/api/test", "", req...)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), crossOriginBody) {
				t.Errorf("%s Origin %q: %d %q, want 403 cross-origin", m, o, rec.Code, rec.Body)
			}
		}
	}
	// Foreign Origin on login: 403 and the limiter never counts it (setup's key aside).
	keys := e.srv.loginLimiter.size()
	for i := 0; i < 10; i++ {
		rec := e.do("POST", "/api/login", `{"password":"correct horse battery"}`, withOrigin("https://evil.example"))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), crossOriginBody) {
			t.Fatalf("foreign login: %d %q", rec.Code, rec.Body)
		}
	}
	if n := e.srv.loginLimiter.size(); n != keys {
		t.Errorf("limiter keys after foreign logins = %d, want %d", n, keys)
	}
	e.login("correct horse battery")
}

func TestOriginCheckAllowed(t *testing.T) {
	e := newOriginEnv(t, NewNameList("pibox", "ui.example"))
	e.setup("correct horse battery")
	c, tok := e.login("correct horse battery")
	for _, o := range []string{
		"https://ui.example", "https://UI.EXAMPLE", "http://ui.example:8443", "https://ui.example.",
		"http://pibox.local:8080", "http://localhost:5173", "http://127.0.0.1:8080", "http://[::1]:8080",
		"http://10.0.0.5",
	} {
		rec := e.do("POST", "/api/test", "", withHost("10.0.0.5:8080"), withOrigin(o), withCookie(c), withCSRF(tok))
		if rec.Code != http.StatusOK {
			t.Errorf("Origin %q: %d %q, want 200", o, rec.Code, rec.Body)
		}
	}
	// Absent Origin passes (curl, CLI).
	if rec := e.do("POST", "/api/test", "", withCookie(c), withCSRF(tok)); rec.Code != http.StatusOK {
		t.Errorf("absent Origin: %d %q", rec.Code, rec.Body)
	}
	// Safe methods ignore Origin, including null and foreign ones.
	for _, o := range []string{"https://evil.example", "null"} {
		if rec := e.do("GET", "/api/test", "", withOrigin(o), withCookie(c)); rec.Code != http.StatusOK {
			t.Errorf("GET Origin %q: %d %q", o, rec.Code, rec.Body)
		}
	}
}

func TestOriginCheckProxyNeedsUIHost(t *testing.T) {
	body := `{"password":"correct horse battery"}`
	opts := []reqOpt{withHost("10.0.0.5:8080"), withOrigin("https://ui.example")}

	with := newOriginEnv(t, NewNameList("pibox", "ui.example"))
	with.setup("correct horse battery")
	if rec := with.do("POST", "/api/login", body, opts...); rec.Code != http.StatusOK {
		t.Errorf("proxy login with --ui-host: %d %q, want 200", rec.Code, rec.Body)
	}

	without := newOriginEnv(t, NewNameList("pibox"))
	without.setup("correct horse battery")
	rec := without.do("POST", "/api/login", body, opts...)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), crossOriginBody) {
		t.Errorf("proxy login without --ui-host: %d %q, want 403", rec.Code, rec.Body)
	}
}

func TestOriginCheckBeforeAuth(t *testing.T) {
	e := newOriginEnv(t, NewNameList("pibox"))
	// No session: a foreign Origin gets the Origin 403, not the auth 401.
	rec := e.do("POST", "/api/test", "", withOrigin("https://evil.example"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), crossOriginBody) {
		t.Errorf("no session, foreign Origin: %d %q", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/test", "", withOrigin("http://pibox:8080")); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session, accepted Origin: %d %q, want 401", rec.Code, rec.Body)
	}
}

func TestOriginCheckDefaultListIsIPOnly(t *testing.T) {
	e := newTestEnv(t) // no WithNameList: zero list, IP literals only
	// localhost is only on lists built with NewNameList; the zero list refuses it.
	rec := e.do("POST", "/api/test", "", withOrigin("http://localhost"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), crossOriginBody) {
		t.Errorf("zero list, Origin localhost: %d %q, want 403", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/test", "", withOrigin("http://192.168.1.2")); rec.Code != http.StatusUnauthorized {
		t.Errorf("zero list, IP Origin: %d %q, want 401", rec.Code, rec.Body)
	}
}
