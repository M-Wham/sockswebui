package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

type importConfigResult struct {
	Endpoints      int `json:"endpoints"`
	Slots          int `json:"slots"`
	SecretsMissing struct {
		Endpoints []string `json:"endpoints"`
		Slots     []int    `json:"slots"`
	} `json:"secrets_missing"`
	Backup string `json:"backup"`
}

func decodeImportResult(t *testing.T, rec *httptest.ResponseRecorder) importConfigResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	var r importConfigResult
	dec := json.NewDecoder(strings.NewReader(rec.Body.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if r.SecretsMissing.Endpoints == nil || r.SecretsMissing.Slots == nil {
		t.Errorf("secrets_missing lists must be arrays, not null: %s", rec.Body)
	}
	return r
}

func TestImportRunning(t *testing.T) {
	x := newImportEnv(t)
	slotFixture(t, x.authed, 2, false)
	if err := x.store.Update(func(c *config.Config) error { c.Slots[0].Enabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	x.ctrl.setState(1, supervisor.StateUp)
	x.ctrl.setState(2, supervisor.StateDegraded)
	oldRaw, err := os.ReadFile(filepath.Join(x.dir, config.ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	seq0 := len(x.ctrl.callSeq())
	file := sealFile(t, x.payload(), "encrypted")

	resp := decodeImportResult(t, x.importConfig(file, exportAdminPW, ptr(exportPassphrase)))

	if got := x.ctrl.callSeq()[seq0:]; !slices.Equal(got, []string{"stop 1", "stop 2", "apply"}) {
		t.Errorf("controller calls = %v, want stop 1, stop 2, apply", got)
	}
	x.ctrl.mu.Lock()
	applied := x.ctrl.applies[len(x.ctrl.applies)-1]
	x.ctrl.mu.Unlock()
	for _, sl := range append(applied.Slots, x.store.Get().Slots...) {
		if sl.Enabled {
			t.Errorf("slot %d enabled after import", sl.ID)
		}
	}
	if resp.Endpoints != 3 || resp.Slots != 2 || len(resp.SecretsMissing.Endpoints)+len(resp.SecretsMissing.Slots) != 0 {
		t.Errorf("response = %+v", resp)
	}
	if !strings.HasPrefix(resp.Backup, "config.json.pre-import-") || strings.Contains(resp.Backup, "/") {
		t.Fatalf("backup name %q", resp.Backup)
	}
	backup, err := os.ReadFile(filepath.Join(x.dir, resp.Backup))
	if err != nil {
		t.Fatalf("backup file: %v", err)
	}
	// The backup is the old config with the admin hash and session secret emptied.
	if want := withoutAdminCreds(t, oldRaw); !strings.Contains(string(oldRaw), "admin_password_hash") ||
		string(withoutAdminCreds(t, backup)) != string(want) || string(backup) == string(oldRaw) {
		t.Error("backup is not the old config minus admin creds")
	}
}

// withoutAdminCreds re-encodes a config.json with settings.admin_password_hash and
// settings.session_secret removed.
func withoutAdminCreds(t *testing.T, raw []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if st, ok := m["settings"].(map[string]any); ok {
		delete(st, "admin_password_hash")
		delete(st, "session_secret")
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImportNoSecrets(t *testing.T) {
	x := newImportEnv(t)
	pwTmpl := x.create(sessBody("u", "SECRETPW-{session}", sess10))["id"].(string)
	userTmpl := x.create(sessBody("u-{session}", "", sess10))["id"].(string)
	before := x.store.Get()
	ns := mutateFile(t, sealFile(t, x.payload(), "no-secrets"), func(m map[string]any) {
		for _, e := range obj(m, "payload")["endpoints"].([]any) {
			if e.(map[string]any)["id"] == userTmpl {
				delete(e.(map[string]any)["session"].(map[string]any), "current_id")
			}
		}
	})

	t.Run("ReaderFailure", func(t *testing.T) {
		snap := x.snap()
		x.srv.rand = failReader{}
		defer func() { x.srv.rand = nil }()
		if rec := x.importConfig(ns, exportAdminPW, nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s, want 500", rec.Code, rec.Body)
		}
		x.assertSame(t, snap)
	})

	seed := strings.Repeat("\x05\x21\x3a", 40)
	wantID, err := config.NewSessionID(strings.NewReader(seed), config.SessionAlnum, 10)
	if err != nil {
		t.Fatal(err)
	}
	x.srv.rand = strings.NewReader(seed)
	resp := decodeImportResult(t, x.importConfig(ns, exportAdminPW, nil))
	x.srv.rand = nil

	wantOmitted := map[string][]string{}
	var wantMissing []string
	for _, e := range before.Endpoints {
		var names []string
		if e.PrivateKey != "" {
			names = append(names, config.OmittedPrivateKey)
		}
		if e.PresharedKey != "" {
			names = append(names, config.OmittedPresharedKey)
		}
		if e.Password != "" {
			names = append(names, config.OmittedPassword)
		}
		wantOmitted[e.ID] = names
		if len(names) > 0 {
			wantMissing = append(wantMissing, e.ID)
		}
	}
	if len(wantMissing) != 3 || !slices.Equal(resp.SecretsMissing.Endpoints, wantMissing) || !slices.Equal(resp.SecretsMissing.Slots, []int{1}) {
		t.Errorf("secrets_missing = %+v, want endpoints %v slots [1]", resp.SecretsMissing, wantMissing)
	}
	after := x.store.Get()
	for _, e := range after.Endpoints {
		if !slices.Equal(e.SecretsOmitted, wantOmitted[e.ID]) {
			t.Errorf("endpoint %s secrets_omitted = %v, want %v", e.ID, e.SecretsOmitted, wantOmitted[e.ID])
		}
		switch e.ID {
		case pwTmpl:
			if e.Session != before.Endpoints[slices.IndexFunc(before.Endpoints, func(b config.Endpoint) bool { return b.ID == pwTmpl })].Session {
				t.Errorf("password template session changed: %+v", e.Session)
			}
		case userTmpl:
			if e.Session.CurrentID != wantID {
				t.Errorf("generated current_id = %q, want %q", e.Session.CurrentID, wantID)
			}
		}
	}
	if !slices.Equal(after.Slots[0].SecretsOmitted, []string{config.OmittedSocksPassword}) {
		t.Errorf("slot secrets_omitted = %v", after.Slots[0].SecretsOmitted)
	}
	if m := decodeEndpoint(t, x.call("GET", "/api/endpoints/"+pwTmpl, "")); m["password_has_session"] != true || m["session"] == nil {
		t.Errorf("password template view: %v", m)
	}

	if rec := x.call("POST", "/api/slots/1/start", ""); rec.Code != http.StatusConflict {
		t.Fatalf("start with missing secrets: %d %s", rec.Code, rec.Body)
	}
	wg := after.Endpoints[0]
	putEP(x.authed, wg.ID, wgBody(wg.Name, fakeKey(t), fakeKey(t), wg.PeerPublicKey))
	slotBody := fmt.Sprintf(`{"name":"s1","enabled":false,"bind_addr":"127.0.0.1","port":1081,"route":[%q],`+
		`"killswitch":"block","socks_auth":{"username":"u","password":"re-entered-pw"}}`, wg.ID)
	if rec := x.call("PUT", "/api/slots/1", slotBody); rec.Code != http.StatusOK {
		t.Fatalf("PUT slot: %d %s", rec.Code, rec.Body)
	}
	if rec := x.call("POST", "/api/slots/1/start", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("start after re-entry: %d %s", rec.Code, rec.Body)
	}
}

func TestImportLimiter(t *testing.T) {
	x := newImportEnv(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	x.srv.loginLimiter = newRateLimiter(loginLimit, loginWindow, loginMaxClients)
	x.srv.loginLimiter.now = clk.now
	ip := withRemote("198.51.100.30:1")
	file := sealFile(t, x.payload(), "no-secrets")
	for i := 0; i < loginLimit; i++ {
		var rec *httptest.ResponseRecorder
		if i%2 == 0 {
			rec = x.importConfig(file, "wrong-password", nil, ip)
		} else {
			rec = x.export(exportBody("wrong-password", "no-secrets", nil), ip)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	x.frees.Store(0)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"import": x.importConfig(file, exportAdminPW, nil, ip),
		"export": x.export(exportBody(exportAdminPW, "no-secrets", nil), ip),
	} {
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Errorf("6th %s: %d Retry-After %q, want 429", name, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	if n := x.frees.Load(); n != 0 {
		t.Errorf("hash ran %d times on a 429", n)
	}
}

func TestImportLogs(t *testing.T) {
	const wrongAdmin = "ImportCanary-WrongAdmin-51"
	const wrongPass = "ImportCanary-WrongPassphrase-62"
	for _, mode := range []string{"encrypted", "no-secrets"} {
		t.Run(mode, func(t *testing.T) {
			x := newImportEnv(t)
			file := sealFile(t, x.payload(), mode)
			var pass *string
			if mode == "encrypted" {
				pass = ptr(exportPassphrase)
			}
			var f struct {
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(file, &f); err != nil {
				t.Fatal(err)
			}
			contents := []string{string(f.Payload), x.store.Get().Endpoints[0].PeerPublicKey}
			uiPort := mutateFile(t, sealFile(t, x.payload(), "no-secrets"), func(m map[string]any) {
				obj(m, "payload")["slots"].([]any)[0].(map[string]any)["port"] = 8787
			})
			for i, rec := range []*httptest.ResponseRecorder{
				x.importConfig(file, wrongAdmin, pass),
				x.importConfig(sealFile(t, x.payload(), "encrypted"), exportAdminPW, ptr(wrongPass)),
				x.importConfig(mutateFile(t, file, func(m map[string]any) { m["format"] = "x" }), exportAdminPW, pass),
				x.importConfig(uiPort, exportAdminPW, nil),
			} {
				if rec.Code == http.StatusOK {
					t.Fatalf("failure case %d succeeded", i)
				}
			}
			decodeImportResult(t, x.importConfig(file, exportAdminPW, pass))
			logs := x.logs.String()
			if n := strings.Count(logs, `msg="config imported"`); n != 1 {
				t.Errorf("config imported lines = %d, want 1:\n%s", n, logs)
			}
			if want := `msg="config imported" endpoints=2 slots=1 mode=` + mode + " remote=192.0.2.1\n"; !strings.Contains(logs, want) {
				t.Errorf("missing %q:\n%s", want, logs)
			}
			for _, s := range append(append([]string{exportPassphrase, exportAdminPW, wrongAdmin, wrongPass}, x.secrets...), contents...) {
				if strings.Contains(logs, s) {
					t.Errorf("log contains %q", s)
				}
			}
		})
	}
}

// TestImportPasswordRaced: a password change landing between the verify and the replace
// wins; the import fails with 409 and changes nothing (the candidate is built under the
// store lock from the current settings, never from a stale copy).
func TestImportPasswordRaced(t *testing.T) {
	x := newImportEnv(t)
	x.ctrl.setState(1, supervisor.StateUp)
	x.srv.replaceConfig = func(build func(config.Config) (config.Config, error), beforeCommit func()) (string, error) {
		if err := x.store.Update(func(c *config.Config) error { c.Settings.AdminPasswordHash += "x"; return nil }); err != nil {
			t.Fatal(err)
		}
		return x.store.ReplaceWithBackupFunc(build, beforeCommit)
	}
	file := sealFile(t, x.payload(), "no-secrets")
	var snap importSnap
	wrapped := x.srv.replaceConfig
	x.srv.replaceConfig = func(build func(config.Config) (config.Config, error), beforeCommit func()) (string, error) {
		name, err := wrapped(build, beforeCommit)
		snap = x.snap()
		return name, err
	}
	if rec := x.importConfig(file, exportAdminPW, nil); rec.Code != http.StatusConflict {
		t.Fatalf("got %d %s, want 409", rec.Code, rec.Body)
	}
	x.assertSame(t, snap)
	if got := x.ctrl.callSeq(); slices.Contains(got, "stop 1") {
		t.Errorf("slot stopped on a raced import: %v", got)
	}
}
