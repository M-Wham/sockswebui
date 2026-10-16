package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// importEnv is an exportEnv (WG endpoint, SOCKS5 upstream, slot 1) with a login limiter
// wide enough for many requests from the default test client.
type importEnv struct {
	*exportEnv
}

func newImportEnv(t *testing.T) *importEnv {
	t.Helper()
	x := newExportEnv(t)
	x.srv.loginLimiter = newRateLimiter(1000, loginWindow, loginMaxClients)
	return &importEnv{x}
}

// importPart is one multipart part of a config import request.
type importPart struct{ name, filename, data string }

func importParts(file []byte, cur string, pass *string) []importPart {
	parts := []importPart{{"file", "config.json", string(file)}, {"current_password", "", cur}}
	if pass != nil {
		parts = append(parts, importPart{"passphrase", "", *pass})
	}
	return parts
}

func importMultipart(t *testing.T, parts []importPart) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		var err error
		var fw interface{ Write([]byte) (int, error) }
		if p.filename != "" {
			fw, err = mw.CreateFormFile(p.name, p.filename)
		} else {
			fw, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(p.data))
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), mw.FormDataContentType()
}

// postImport sends a raw body to POST /api/config/import with the session cookie and CSRF.
func (x *importEnv) postImport(body, ctype string, opts ...reqOpt) *httptest.ResponseRecorder {
	x.t.Helper()
	req := httptest.NewRequest("POST", "/api/config/import", strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.AddCookie(x.cookie)
	req.Header.Set(csrfHeader, x.csrf)
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	x.h.ServeHTTP(rec, req)
	return rec
}

func (x *importEnv) importParts(parts []importPart, opts ...reqOpt) *httptest.ResponseRecorder {
	x.t.Helper()
	body, ct := importMultipart(x.t, parts)
	return x.postImport(body, ct, opts...)
}

func (x *importEnv) importConfig(file []byte, cur string, pass *string, opts ...reqOpt) *httptest.ResponseRecorder {
	x.t.Helper()
	return x.importParts(importParts(file, cur, pass), opts...)
}

// payload returns the store's endpoints and slots as an export payload.
func (x *importEnv) payload() config.ExportPayload {
	c := x.store.Get()
	return config.ExportPayload{Endpoints: c.Endpoints, Slots: c.Slots}
}

// sealFile builds an export file of p directly (no server, no hash counter).
func sealFile(t *testing.T, p config.ExportPayload, mode string) []byte {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var raw []byte
	var err error
	if mode == config.ExportModeEncrypted {
		raw, err = config.SealEncrypted(rand.Reader, p, []byte(exportPassphrase), testKDF, now, "test")
	} else {
		raw, err = config.SealNoSecrets(p, now, "test")
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// mutateFile decodes an export file into generic JSON, applies fn and re-encodes it.
func mutateFile(t *testing.T, raw []byte, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func obj(m map[string]any, key string) map[string]any { return m[key].(map[string]any) }

func b64Len(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }

// importSnap is everything an import must leave untouched on failure.
type importSnap struct {
	raw []byte
	cfg config.Config
	dir []string
	seq []string
}

func (x *importEnv) snap() importSnap {
	x.t.Helper()
	raw, err := os.ReadFile(x.dir + "/" + config.ConfigFileName)
	if err != nil {
		x.t.Fatal(err)
	}
	ents, err := os.ReadDir(x.dir)
	if err != nil {
		x.t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return importSnap{raw: raw, cfg: x.store.Get(), dir: names, seq: x.ctrl.callSeq()}
}

func (x *importEnv) assertSame(t *testing.T, before importSnap) {
	t.Helper()
	after := x.snap()
	if !bytes.Equal(after.raw, before.raw) {
		t.Error("config.json bytes changed")
	}
	if !reflect.DeepEqual(after.cfg, before.cfg) {
		t.Error("store.Get() changed")
	}
	if !slices.Equal(after.dir, before.dir) {
		t.Errorf("data dir listing changed: %v -> %v", before.dir, after.dir)
	}
	if !slices.Equal(after.seq, before.seq) {
		t.Errorf("controller calls changed: %v -> %v", before.seq, after.seq)
	}
}

func TestImportAuth(t *testing.T) {
	x := newImportEnv(t)
	body, ct := importMultipart(t, importParts(sealFile(t, x.payload(), "no-secrets"), exportAdminPW, nil))
	for _, tc := range []struct {
		name string
		opts []reqOpt
		want int
	}{
		{"NoCookie", nil, http.StatusUnauthorized},
		{"NoCSRF", []reqOpt{withCookie(x.cookie)}, http.StatusForbidden},
	} {
		req := httptest.NewRequest("POST", "/api/config/import", strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		for _, o := range tc.opts {
			o(req)
		}
		rec := httptest.NewRecorder()
		x.h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	if n := x.frees.Load(); n != 0 {
		t.Errorf("hash ran %d times on rejected auth", n)
	}
}

func TestImportForm(t *testing.T) {
	x := newImportEnv(t)
	file := string(sealFile(t, x.payload(), "no-secrets"))
	cur := importPart{"current_password", "", exportAdminPW}
	cases := map[string][]importPart{
		"NoFile":        {cur},
		"TwoFiles":      {{"file", "a.json", file}, {"file", "b.json", file}, cur},
		"NoPassword":    {{"file", "a.json", file}},
		"TwoPasswords":  {{"file", "a.json", file}, cur, cur},
		"UnknownPart":   {{"file", "a.json", file}, cur, {"mode", "", "encrypted"}},
		"LongPassword":  {{"file", "a.json", file}, {"current_password", "", strings.Repeat("a", 1025)}},
		"LongPassphase": {{"file", "a.json", file}, cur, {"passphrase", "", strings.Repeat("a", 1025)}},
	}
	before := x.snap()
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := x.importParts(parts); rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", rec.Code, rec.Body)
			}
		})
	}
	if rec := x.postImport(`{"file":"x"}`, "application/json"); rec.Code != http.StatusBadRequest {
		t.Errorf("JSON body: %d, want 400", rec.Code)
	}
	if n := x.frees.Load(); n != 0 {
		t.Errorf("hash ran %d times on malformed forms", n)
	}
	x.assertSame(t, before)
}

func TestImportBodyTooLarge(t *testing.T) {
	x := newImportEnv(t)
	before := x.snap()
	file := sealFile(t, x.payload(), "no-secrets")
	parts := importParts(file, exportAdminPW, nil)
	parts = append(parts, importPart{"padding", "", strings.Repeat("a", 1<<20)})
	if rec := x.importParts(parts); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d %s, want 413", rec.Code, rec.Body)
	}
	big := importParts(append(file, bytes.Repeat([]byte(" "), 1<<20)...), exportAdminPW, nil)
	if rec := x.importParts(big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big file: got %d, want 413", rec.Code)
	}
	if n := x.frees.Load(); n != 0 {
		t.Errorf("hash ran %d times before the size check", n)
	}
	x.assertSame(t, before)
}

func TestImportWrongPassword(t *testing.T) {
	x := newImportEnv(t)
	before := x.snap()
	rec := x.importConfig(sealFile(t, x.payload(), "encrypted"), "wrong-password", ptr(exportPassphrase))
	want := []config.FieldError{{Field: "current_password", Message: "is incorrect"}}
	if got := decodeFieldErrors(t, rec); rec.Code != http.StatusBadRequest || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %d %s, want 400 %v", rec.Code, rec.Body, want)
	}
	if n := x.frees.Load(); n != 1 {
		t.Errorf("hash runs = %d, want 1 (verify only, no import KDF)", n)
	}
	x.assertSame(t, before)
}

// TestImportOrder proves limiter → size → password → steps: each failing stage leaves the
// later ones unrun (hash counter, controller and store untouched).
func TestImportOrder(t *testing.T) {
	x := newImportEnv(t)
	before := x.snap()
	file := sealFile(t, x.payload(), "encrypted")
	oversize := append(importParts(file, "wrong-password", ptr(exportPassphrase)),
		importPart{"padding", "", strings.Repeat("a", 1<<20)})

	// Limiter first: an exhausted bucket answers 429 even to an oversize body.
	x.srv.loginLimiter = newRateLimiter(loginLimit, loginWindow, loginMaxClients)
	ip := withRemote("198.51.100.20:1")
	for i := 0; i < loginLimit; i++ {
		x.importParts([]importPart{{"file", "f", "{}"}}, ip)
	}
	if rec := x.importParts(oversize, ip); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("limited oversize: %d, want 429", rec.Code)
	}
	// Size before password: oversize with a wrong password is 413 with no hash.
	if rec := x.importParts(oversize, withRemote("198.51.100.21:1")); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d, want 413", rec.Code)
	}
	if n := x.frees.Load(); n != 0 {
		t.Fatalf("hash ran %d times before the password stage", n)
	}
	// Password before steps: a wrong password with a broken file is the password error,
	// and no key derivation runs.
	broken := mutateFile(t, file, func(m map[string]any) { m["format"] = "nope" })
	rec := x.importConfig(broken, "wrong-password", ptr(exportPassphrase), withRemote("198.51.100.22:1"))
	if got := decodeFieldErrors(t, rec); len(got) != 1 || got[0].Field != "current_password" {
		t.Fatalf("broken file + wrong password: %d %s", rec.Code, rec.Body)
	}
	if n := x.frees.Load(); n != 1 {
		t.Fatalf("hash runs = %d, want 1", n)
	}
	x.assertSame(t, before)
}

// TestImportFileRejected covers step 1: every header problem is a 400 before any key
// derivation (hash counter = 1, the admin password verify only).
func TestImportFileRejected(t *testing.T) {
	x := newImportEnv(t)
	enc := sealFile(t, x.payload(), "encrypted")
	ns := sealFile(t, x.payload(), "no-secrets")
	cases := []struct {
		name string
		file []byte
		pass *string
	}{
		{"Format", mutateFile(t, enc, func(m map[string]any) { m["format"] = "other-config" }), ptr(exportPassphrase)},
		{"Mode", mutateFile(t, enc, func(m map[string]any) { m["mode"] = "plain" }), ptr(exportPassphrase)},
		{"Version2", mutateFile(t, enc, func(m map[string]any) { m["format_version"] = 2 }), ptr(exportPassphrase)},
		{"Salt15", mutateFile(t, enc, func(m map[string]any) { obj(m, "kdf")["salt"] = b64Len(15) }), ptr(exportPassphrase)},
		{"Nonce13", mutateFile(t, enc, func(m map[string]any) { obj(m, "cipher")["nonce"] = b64Len(13) }), ptr(exportPassphrase)},
		{"M8192", mutateFile(t, enc, func(m map[string]any) { obj(m, "kdf")["m_kib"] = 8192 }), ptr(exportPassphrase)},
		{"T4", mutateFile(t, enc, func(m map[string]any) { obj(m, "kdf")["t"] = 4 }), ptr(exportPassphrase)},
		{"P1", mutateFile(t, enc, func(m map[string]any) { obj(m, "kdf")["p"] = 1 }), ptr(exportPassphrase)},
		{"PassphraseWithNoSecrets", ns, ptr(exportPassphrase)},
		{"UnknownTopField", mutateFile(t, enc, func(m map[string]any) { m["extra"] = true }), ptr(exportPassphrase)},
		{"UnknownKDFField", mutateFile(t, enc, func(m map[string]any) { obj(m, "kdf")["x"] = 1 }), ptr(exportPassphrase)},
		{"UnknownPayloadField", mutateFile(t, ns, func(m map[string]any) {
			obj(m, "payload")["endpoints"].([]any)[0].(map[string]any)["colour"] = "red"
		}), nil},
		{"SecretsOmittedInFile", mutateFile(t, ns, func(m map[string]any) {
			obj(m, "payload")["slots"].([]any)[0].(map[string]any)["secrets_omitted"] = []string{"socks_auth.password"}
		}), nil},
		{"NotJSON", []byte("not json"), nil},
		{"Empty", nil, nil},
	}
	before := x.snap()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x.frees.Store(0)
			rec := x.importConfig(tc.file, exportAdminPW, tc.pass)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", rec.Code, rec.Body)
			}
			if n := x.frees.Load(); n != 1 {
				t.Errorf("hash runs = %d, want 1 (no key derivation)", n)
			}
		})
	}
	x.assertSame(t, before)
}
