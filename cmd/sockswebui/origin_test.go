package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/api"
)

// TestOriginCheckFullStack proves the layer order over newHandler: Host guard,
// then Origin, then requireAuth, then the login limiter; and the reverse-proxy case.
func TestOriginCheckFullStack(t *testing.T) {
	base := startGuarded(t, api.NewNameList("pibox", "ui.example"))
	do := func(method, path, host, origin, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b strings.Builder
		_, _ = io.Copy(&b, res.Body)
		return res.StatusCode, b.String()
	}
	want := func(code int, b string, status int, sub, what string) {
		t.Helper()
		if code != status || !strings.Contains(b, sub) {
			t.Errorf("%s: %d %q, want %d containing %s", what, code, b, status, sub)
		}
	}
	const proxyHost = "10.0.0.5:8080"

	// 1. Host guard runs before Origin: a foreign Host is refused as a Host error.
	code, b := do("POST", "/api/login", "evil.example", "https://evil.example", testPW)
	want(code, b, 403, `"host not allowed"`, "foreign Host+Origin")
	// 2. Origin runs before requireAuth: no session, foreign Origin -> Origin 403, not 401.
	code, b = do("POST", "/api/slots/1/start", proxyHost, "https://evil.example", "")
	want(code, b, 403, `"cross-origin request refused"`, "foreign Origin, no session")
	code, b = do("POST", "/api/slots/1/start", proxyHost, "https://ui.example", "")
	want(code, b, 401, `"login required"`, "accepted Origin, no session")
	// Setup still open after a refused cross-origin setup.
	code, b = do("POST", "/api/setup", proxyHost, "https://evil.example", testPW)
	want(code, b, 403, `"cross-origin request refused"`, "foreign Origin setup")
	if code, b = do("POST", "/api/setup", proxyHost, "", testPW); code/100 != 2 {
		t.Fatalf("setup: %d %q", code, b)
	}
	// 3. Origin runs before the limiter (5/min): ten foreign logins, then the proxy login works.
	for i := 0; i < 10; i++ {
		code, b = do("POST", "/api/login", proxyHost, "https://evil.example", testPW)
		want(code, b, 403, `"cross-origin request refused"`, "foreign Origin login")
	}
	code, b = do("POST", "/api/login", proxyHost, "https://UI.example", testPW)
	want(code, b, 200, `csrf_token`, "proxy login with --ui-host")

	// Same proxy request without --ui-host -> 403.
	bare := startGuarded(t, api.NewNameList("pibox"))
	base = bare
	code, b = do("POST", "/api/login", proxyHost, "https://ui.example", testPW)
	want(code, b, 403, `"cross-origin request refused"`, "proxy login without --ui-host")
}
