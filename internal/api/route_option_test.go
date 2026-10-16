package api

import (
	"net/http"
	"strings"
	"testing"
)

// WithRoute mounts an extra route behind the normal auth gate: session cookie
// always, CSRF header for state-changing methods.
func TestWithRouteIsBehindAuthAndCSRF(t *testing.T) {
	e := newTestEnv(t)
	hits := 0
	WithRoute("POST /api/extra", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))(e.srv)
	e.setup("correct horse battery")
	c, tok := e.login("correct horse battery")

	if rec := e.do("POST", "/api/extra", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/extra", "", withCookie(c)); rec.Code != http.StatusForbidden {
		t.Errorf("no csrf: %d", rec.Code)
	}
	if hits != 0 {
		t.Fatalf("handler ran %d times without auth", hits)
	}
	if rec := e.do("POST", "/api/extra", "", withCookie(c), withCSRF(tok)); rec.Code != http.StatusNoContent {
		t.Errorf("authed: %d %s", rec.Code, rec.Body)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

// Extra routes must live under /api/ so the auth gate applies.
func TestWithRouteRejectsNonAPIPattern(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "/api/") {
			t.Errorf("recover = %v, want panic mentioning /api/", r)
		}
	}()
	WithRoute("POST /dev/rig/pause", http.NotFoundHandler())(newServer(nil, nil, testParams))
}
