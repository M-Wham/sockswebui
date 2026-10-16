package api

import (
	"net/http"
	"testing"
	"time"
)

func loggedIn(t *testing.T) (*testEnv, *http.Cookie, string) {
	t.Helper()
	e := newTestEnv(t)
	e.setup("hunter22-long")
	c, csrf := e.login("hunter22-long")
	return e, c, csrf
}

func TestAuthMiddlewareNoCookie(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
		if rec := e.do(m, "/api/test", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without cookie: %d, want 401", m, rec.Code)
		}
	}
	// Unknown API paths are also gated (no unauthenticated route probing).
	if rec := e.do("GET", "/api/nope", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown path without cookie: %d, want 401", rec.Code)
	}
	if rec := e.do("POST", "/api/logout", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("logout without cookie: %d, want 401", rec.Code)
	}
}

func TestAuthMiddlewareBogusCookie(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	c := &http.Cookie{Name: sessionCookieName, Value: "forged"}
	if rec := e.do("GET", "/api/test", "", withCookie(c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie: %d, want 401", rec.Code)
	}
}

func TestAuthMiddlewareCSRF(t *testing.T) {
	e, c, csrf := loggedIn(t)
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		if rec := e.do(m, "/api/test", "", withCookie(c)); rec.Code != http.StatusForbidden {
			t.Errorf("%s missing csrf: %d, want 403", m, rec.Code)
		}
		if rec := e.do(m, "/api/test", "", withCookie(c), withCSRF(csrf+"x")); rec.Code != http.StatusForbidden {
			t.Errorf("%s wrong csrf: %d, want 403", m, rec.Code)
		}
		if rec := e.do(m, "/api/test", "", withCookie(c), withCSRF(csrf)); rec.Code != http.StatusOK {
			t.Errorf("%s with cookie+csrf: %d, want 200", m, rec.Code)
		}
	}
	if rec := e.do("GET", "/api/test", "", withCookie(c)); rec.Code != http.StatusOK {
		t.Errorf("GET without csrf: %d, want 200", rec.Code)
	}
}

func TestAuthMiddlewareCSRFBoundToSession(t *testing.T) {
	e, c1, _ := loggedIn(t)
	_, csrf2 := e.login("hunter22-long")
	if rec := e.do("POST", "/api/test", "", withCookie(c1), withCSRF(csrf2)); rec.Code != http.StatusForbidden {
		t.Fatalf("other session's csrf accepted: %d", rec.Code)
	}
}

func TestAuthMiddlewareLogout(t *testing.T) {
	e, c, csrf := loggedIn(t)
	rec := e.do("POST", "/api/logout", "", withCookie(c), withCSRF(csrf))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d, want 204", rec.Code)
	}
	cleared := sessionCookie(t, rec)
	if cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("logout cookie not cleared: %+v", cleared)
	}
	if rec := e.do("GET", "/api/test", "", withCookie(c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie after logout: %d, want 401", rec.Code)
	}
}

func TestAuthMiddlewareSessionExpiry(t *testing.T) {
	e, c, _ := loggedIn(t)
	now := time.Now()
	e.srv.sessions.now = func() time.Time { return now.Add(sessionTTL + time.Second) }
	if rec := e.do("GET", "/api/test", "", withCookie(c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session: %d, want 401", rec.Code)
	}
}

func TestAuthMiddlewareExemptRoutesOnlyPOST(t *testing.T) {
	e := newTestEnv(t)
	// GET /api/setup is not an exempt route; it must hit the auth gate.
	if rec := e.do("GET", "/api/setup", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/setup: %d, want 401", rec.Code)
	}
}
