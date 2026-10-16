package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestAuditLog: every security-relevant action logs exactly one line carrying
// remote=<clientKey(r)>, keeps its message text, and never logs a secret.
func TestAuditLog(t *testing.T) {
	const wantPW = "AuditCanary-NewAdminPW-77"
	x := newImportEnv(t)
	x.srv.rotateLimiter.now = x.srv.now
	rot := x.create(sessBody("ru", "PW-{session}-AuditCanary-ProxyPW", sess10))["id"].(string)

	n := 0
	next := func() (string, reqOpt) { // a fresh client per step, with a hostile XFF header
		n++
		ip := fmt.Sprintf("198.51.100.%d", n)
		return "remote=" + ip, func(r *http.Request) {
			r.RemoteAddr = ip + ":4444"
			r.Header.Set("X-Forwarded-For", "10.9.8.7")
		}
	}
	login := func(pw string) (*http.Cookie, string) {
		x.t.Helper()
		c, tok := x.login(pw)
		return c, tok
	}
	// check clears the log, runs fn and wants exactly one line with msg and remote.
	check := func(name, msg string, fn func(remote reqOpt) int, wantCode int) {
		t.Helper()
		remote, opt := next()
		x.logs.Reset()
		if got := fn(opt); got != wantCode {
			t.Fatalf("%s: status %d, want %d", name, got, wantCode)
		}
		var hits []string
		for _, l := range strings.Split(x.logs.String(), "\n") {
			if strings.Contains(l, msg) {
				hits = append(hits, l)
			}
		}
		if len(hits) != 1 || !strings.Contains(hits[0], remote+" ") && !strings.HasSuffix(hits[0], remote) {
			t.Errorf("%s: want one %q line with %s, got %q", name, msg, remote, hits)
		}
		if strings.Contains(x.logs.String(), "10.9.8.7") {
			t.Errorf("%s: X-Forwarded-For value logged", name)
		}
	}
	authCall := func(method, path, body string) func(reqOpt) int {
		return func(o reqOpt) int {
			return x.do(method, path, body, withCookie(x.cookie), withCSRF(x.csrf), o).Code
		}
	}

	fresh := newTestEnv(t)
	remote, opt := next()
	fresh.logs.Reset()
	if rec := fresh.do("POST", "/api/setup", `{"password":"`+exportAdminPW+`"}`, opt); rec.Code != http.StatusNoContent {
		t.Fatalf("setup: %d", rec.Code)
	}
	if l := fresh.logs.String(); strings.Count(l, "admin password set") != 1 || !strings.Contains(l, remote) {
		t.Errorf("setup: want one line with %s, got %q", remote, l)
	}

	check("login fail", `msg="login failed"`, func(o reqOpt) int {
		return x.do("POST", "/api/login", `{"password":"wrong-password-AuditCanary"}`, o).Code
	}, http.StatusUnauthorized)
	check("login ok", `msg="login succeeded"`, func(o reqOpt) int {
		return x.do("POST", "/api/login", `{"password":"`+exportAdminPW+`"}`, o).Code
	}, http.StatusOK)
	check("export", `msg="config exported" mode=no-secrets`,
		authCall("POST", "/api/config/export", exportBody(exportAdminPW, "no-secrets", nil)), http.StatusOK)
	check("export encrypted", `msg="config exported" mode=encrypted`,
		authCall("POST", "/api/config/export", exportBody(exportAdminPW, "encrypted", ptr(exportPassphrase))), http.StatusOK)

	file := sealFile(t, x.payload(), "encrypted")
	check("import", `msg="config imported" endpoints=`, func(o reqOpt) int {
		return x.importConfig(file, exportAdminPW, ptr(exportPassphrase), o).Code
	}, http.StatusOK)

	x.cookie, x.csrf = login(exportAdminPW)
	check("slot killswitch", `msg="slot updated"`, authCall("PUT", "/api/slots/1",
		fmt.Sprintf(`{"name":"s","enabled":false,"bind_addr":"127.0.0.1","port":1081,"route":[%q],"killswitch":"allow-direct","socks_auth":{"username":"u","password":%q}}`,
			x.store.Get().Slots[0].Route[0], testSocksPass)), http.StatusOK)
	check("rotate", `msg="session rotated"`, authCall("POST", "/api/endpoints/"+rot+"/rotate", ""), http.StatusOK)
	check("logout", "msg=logout", authCall("POST", "/api/logout", ""), http.StatusNoContent)

	x.cookie, x.csrf = login(exportAdminPW)
	check("password change", `msg="admin password changed"`, authCall("PUT", "/api/settings",
		`{"ui_bind_addr":"127.0.0.1:9999","current_password":"`+exportAdminPW+`","new_password":"`+wantPW+`"}`), http.StatusOK)
}

// TestAuditLogNoSecrets: canaries from every request body stay out of the capture.
func TestAuditLogNoSecrets(t *testing.T) {
	x := newImportEnv(t)
	x.logs.Reset()
	x.export(exportBody(exportAdminPW, "encrypted", ptr(exportPassphrase)))
	x.do("POST", "/api/login", `{"password":"wrong-AuditCanary"}`)
	logs := x.logs.String()
	for _, s := range append([]string{exportAdminPW, exportPassphrase, "wrong-AuditCanary"}, x.secrets...) {
		if strings.Contains(logs, s) {
			t.Errorf("log contains %q", s)
		}
	}
}
