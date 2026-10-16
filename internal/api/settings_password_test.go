package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const cpOld = "hunter22-long"

// cpSnap is the persisted state a rejected change-password call must not touch.
type cpSnap struct{ hash, secret, addr string }

func snap(e *testEnv) cpSnap {
	s := e.store.Get().Settings
	return cpSnap{s.AdminPasswordHash, s.SessionSecret, s.UIBindAddr}
}

// twoSessions logs in twice; A is the caller, B is the other session.
func twoSessions(t *testing.T) (*testEnv, *authed, *authed) {
	t.Helper()
	e := newTestEnv(t)
	e.setup(cpOld)
	ca, ta := e.login(cpOld)
	cb, tb := e.login(cpOld)
	return e, &authed{testEnv: e, cookie: ca, csrf: ta}, &authed{testEnv: e, cookie: cb, csrf: tb}
}

func TestSettingsChangePassword(t *testing.T) {
	body := func(cur, nw string) string {
		return `{"ui_bind_addr":"127.0.0.1:9999","current_password":"` + cur + `","new_password":"` + nw + `"}`
	}
	rejected := func(t *testing.T, payload, field string) {
		t.Helper()
		e, a, b := twoSessions(t)
		before := snap(e)
		rec := a.call("PUT", "/api/settings", payload)
		if rec.Code != http.StatusBadRequest || !hasField(decodeFieldErrors(t, rec), field) {
			t.Fatalf("want 400 field %q, got %d %s", field, rec.Code, rec.Body)
		}
		if snap(e) != before {
			t.Error("stored settings changed")
		}
		if rec := a.call("GET", "/api/settings", ""); rec.Code != http.StatusOK {
			t.Errorf("caller GET: %d", rec.Code)
		}
		if rec := a.call("PUT", "/api/settings", `{"ui_bind_addr":"`+before.addr+`"}`); rec.Code != http.StatusOK {
			t.Errorf("caller PUT with CSRF: %d", rec.Code)
		}
		if rec := b.call("GET", "/api/settings", ""); rec.Code != http.StatusOK {
			t.Errorf("other session GET: %d", rec.Code)
		}
	}

	t.Run("WrongCurrent", func(t *testing.T) { rejected(t, body("wrong-password", "brand-new-pw"), "current_password") })
	t.Run("ShortNew", func(t *testing.T) {
		e, a, _ := twoSessions(t)
		before := snap(e)
		rec := a.call("PUT", "/api/settings", body(cpOld, "1234567"))
		fe := decodeFieldErrors(t, rec)
		if rec.Code != http.StatusBadRequest || !hasField(fe, "new_password") ||
			!strings.Contains(rec.Body.String(), "must be at least 8 characters") {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		if snap(e) != before {
			t.Error("stored settings changed")
		}
	})
	t.Run("OnlyOneField", func(t *testing.T) {
		rejected(t, `{"ui_bind_addr":"127.0.0.1:9999","current_password":"`+cpOld+`"}`, "new_password")
		rejected(t, `{"ui_bind_addr":"127.0.0.1:9999","new_password":"brand-new-pw"}`, "current_password")
	})
	t.Run("NoCSRF", func(t *testing.T) {
		e, a, _ := twoSessions(t)
		before := snap(e)
		rec := a.do("PUT", "/api/settings", body(cpOld, "brand-new-pw"), withCookie(a.cookie))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d", rec.Code)
		}
		if snap(e) != before {
			t.Error("stored settings changed")
		}
	})
	t.Run("Success", func(t *testing.T) {
		e, a, b := twoSessions(t)
		before := snap(e)
		rec := a.call("PUT", "/api/settings", body(cpOld, "brand-new-pw"))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		m := decodeMap(t, rec)
		newCSRF, _ := m["csrf_token"].(string)
		if newCSRF == "" || newCSRF == a.csrf {
			t.Fatalf("csrf_token = %q (old %q)", newCSRF, a.csrf)
		}
		after := snap(e)
		if !strings.HasPrefix(after.hash, "$argon2id$") || after.hash == before.hash {
			t.Error("hash not replaced")
		}
		if after.secret == before.secret || after.secret == "" {
			t.Error("session secret not rotated")
		}
		if after.addr != "127.0.0.1:9999" {
			t.Errorf("ui_bind_addr = %q", after.addr)
		}
		if rec := b.call("GET", "/api/settings", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("other session: %d", rec.Code)
		}
		put := `{"ui_bind_addr":"127.0.0.1:9999"}`
		if rec := a.call("PUT", "/api/settings", put); rec.Code != http.StatusForbidden {
			t.Errorf("old CSRF: %d", rec.Code)
		}
		if rec := a.do("PUT", "/api/settings", put, withCookie(a.cookie), withCSRF(newCSRF)); rec.Code != http.StatusOK {
			t.Errorf("new CSRF: %d %s", rec.Code, rec.Body)
		}
		if rec := e.do("POST", "/api/login", `{"password":"`+cpOld+`"}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("old password login: %d", rec.Code)
		}
		if rec := e.do("POST", "/api/login", `{"password":"brand-new-pw"}`); rec.Code != http.StatusOK {
			t.Errorf("new password login: %d", rec.Code)
		}
	})
}

// TestChangePasswordRateLimited: password change draws on the shared auth limit
// before any argon2 work; plain settings updates do not.
func TestChangePasswordRateLimited(t *testing.T) {
	const ip = "198.51.100.9:4321" // separate from the IP the logins used
	body := func(cur string) string {
		return `{"ui_bind_addr":"127.0.0.1:9999","current_password":"` + cur + `","new_password":"brand-new-pw"}`
	}
	e, a, _ := twoSessions(t)
	var frees atomic.Int32
	e.srv.freeOSMemory = func() { frees.Add(1) }
	put := func(b string) *httptest.ResponseRecorder {
		return a.do("PUT", "/api/settings", b, withCookie(a.cookie), withCSRF(a.csrf), withRemote(ip))
	}
	for i := 0; i < 6; i++ { // plain updates never consume the budget
		if rec := put(`{"ui_bind_addr":"127.0.0.1:8080"}`); rec.Code != http.StatusOK {
			t.Fatalf("plain update %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	before := snap(e)
	for i := 0; i < loginLimit; i++ {
		if rec := put(body("wrong-password")); rec.Code != http.StatusBadRequest {
			t.Fatalf("wrong attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	verifies := frees.Load()
	if verifies != loginLimit {
		t.Fatalf("argon2 verifies = %d, want %d", verifies, loginLimit)
	}
	rec := put(body(cpOld))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("6th attempt: %d Retry-After=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if frees.Load() != verifies {
		t.Error("argon2 ran for a rate-limited attempt")
	}
	if snap(e) != before {
		t.Error("stored settings changed")
	}
	if rec := put(`{"ui_bind_addr":"127.0.0.1:8080"}`); rec.Code != http.StatusOK {
		t.Errorf("plain update after limit: %d %s", rec.Code, rec.Body)
	}
}
