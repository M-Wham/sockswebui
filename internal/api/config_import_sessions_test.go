package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestImportDropsOtherSessions: a successful config import ends every admin session except
// the caller's, without rotating the session secret. A failed import
// leaves other sessions alone.
func TestImportDropsOtherSessions(t *testing.T) {
	// setup returns the import env (caller A) and a second session B.
	setup := func(t *testing.T) (*importEnv, *authed) {
		t.Helper()
		x := newImportEnv(t)
		cb, tb := x.login(exportAdminPW)
		return x, &authed{testEnv: x.testEnv, cookie: cb, csrf: tb}
	}
	alive := func(t *testing.T, who string, a *authed, want int) {
		t.Helper()
		if rec := a.call("GET", "/api/settings", ""); rec.Code != want {
			t.Errorf("%s GET /api/settings: %d, want %d", who, rec.Code, want)
		}
	}

	t.Run("Success", func(t *testing.T) {
		x, b := setup(t)
		secret := x.store.Get().Settings.SessionSecret
		rec := x.importConfig(sealFile(t, x.payload(), "encrypted"), exportAdminPW, ptr(exportPassphrase))
		if rec.Code != http.StatusOK {
			t.Fatalf("import: %d %s", rec.Code, rec.Body)
		}
		alive(t, "other session", b, http.StatusUnauthorized)
		alive(t, "caller", x.authed, http.StatusOK)
		// The caller's existing CSRF token still works for a state-changing request.
		addr := x.store.Get().Settings.UIBindAddr
		if rec := x.call("PUT", "/api/settings", `{"ui_bind_addr":"`+addr+`"}`); rec.Code != http.StatusOK {
			t.Errorf("caller PUT with CSRF: %d %s", rec.Code, rec.Body)
		}
		if got := x.store.Get().Settings.SessionSecret; got != secret {
			t.Error("session secret rotated")
		}
	})

	t.Run("WrongPassphrase", func(t *testing.T) {
		x, b := setup(t)
		rec := x.importConfig(sealFile(t, x.payload(), "encrypted"), exportAdminPW, ptr("wrong-passphrase-x"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "wrong passphrase") {
			t.Fatalf("import: %d %s, want 400 wrong passphrase", rec.Code, rec.Body)
		}
		alive(t, "other session", b, http.StatusOK)
		alive(t, "caller", x.authed, http.StatusOK)
	})
	t.Run("ValidationError", func(t *testing.T) {
		x, b := setup(t)
		p := x.payload()
		p.Slots[0].Port = 0
		rec := x.importConfig(sealFile(t, p, "no-secrets"), exportAdminPW, nil)
		if rec.Code != http.StatusBadRequest || !hasField(decodeFieldErrors(t, rec), "slots[0].port") {
			t.Fatalf("import: %d %s, want 400 slots[0].port", rec.Code, rec.Body)
		}
		alive(t, "other session", b, http.StatusOK)
	})
	t.Run("CommitFailure", func(t *testing.T) {
		x, file := newCommitFailEnv(t)
		cb, tb := x.login(exportAdminPW)
		b := &authed{testEnv: x.testEnv, cookie: cb, csrf: tb}
		x.failCommit(t, nil)
		rec := x.importConfig(file, exportAdminPW, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("import: %d %s, want 500", rec.Code, rec.Body)
		}
		alive(t, "other session", b, http.StatusOK)
	})
}
