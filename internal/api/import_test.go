package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// importConf renders a complete WireGuard .conf with the given keys (generated at test time).
func importConf(priv, psk, peer, endpoint string) string {
	return "[Interface]\nPrivateKey = " + priv + "\nAddress = 10.64.0.2/32\nDNS = 10.64.0.1\n\n" +
		"[Peer]\nPublicKey = " + peer + "\nPresharedKey = " + psk +
		"\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = " + endpoint + "\n"
}

type upload struct{ name, data string }

// multipartBody encodes uploads under field (normally "files").
func multipartBody(t *testing.T, field string, ups ...upload) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, u := range ups {
		fw, err := mw.CreateFormFile(field, u.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(u.data))
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), mw.FormDataContentType()
}

func (a *authed) importRaw(body, ctype string, opts ...reqOpt) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest("POST", "/api/endpoints/import", strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func (a *authed) importFiles(ups ...upload) *httptest.ResponseRecorder {
	a.t.Helper()
	body, ct := multipartBody(a.t, "files", ups...)
	return a.importRaw(body, ct, withCookie(a.cookie), withCSRF(a.csrf))
}

type importResultJSON struct {
	File     string   `json:"file"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason"`
	Warnings []string `json:"warnings"`
	ID       string   `json:"id"`
}

func decodeImport(t *testing.T, rec *httptest.ResponseRecorder) []importResultJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("import: status %d body %s", rec.Code, rec.Body)
	}
	var body struct {
		Results []importResultJSON `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return body.Results
}

func (a *authed) listEndpoints() []map[string]any {
	a.t.Helper()
	rec := a.call("GET", "/api/endpoints", "")
	var body struct {
		Endpoints []map[string]any `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		a.t.Fatalf("list %q: %v", rec.Body, err)
	}
	return body.Endpoints
}

func TestImportEndpoint(t *testing.T) {
	a := newAuthed(t)
	mull := importConf(fakeKey(t), fakeKey(t), fakeKey(t), "198.51.100.10:51820")
	other := importConf(fakeKey(t), fakeKey(t), fakeKey(t), "203.0.113.5:51820")
	res := decodeImport(t, a.importFiles(
		upload{"se-got-wg-001.conf", mull},
		upload{"C:\\Users\\me\\home.conf", other},
		upload{"broken.conf", "this is not a wireguard config"},
	))
	if len(res) != 3 {
		t.Fatalf("results = %+v, want 3", res)
	}
	if res[0].File != "se-got-wg-001.conf" || res[0].Status != "imported" || res[0].ID == "" || len(res[0].Warnings) != 0 {
		t.Errorf("mullvad result = %+v", res[0])
	}
	if res[1].File != "home.conf" || res[1].Status != "imported" || res[1].ID == "" || len(res[1].Warnings) != 0 {
		t.Errorf("non-mullvad result = %+v (file must be base name only)", res[1])
	}
	if res[2].File != "broken.conf" || res[2].Status != "error" || res[2].Reason == "" || res[2].ID != "" {
		t.Errorf("malformed result = %+v", res[2])
	}
	eps := a.listEndpoints()
	if len(eps) != 2 {
		t.Fatalf("endpoints = %v, want 2", eps)
	}
	byID := map[string]map[string]any{}
	for _, e := range eps {
		byID[e["id"].(string)] = e
		if e["private_key_set"] != true || e["kind"] != "wireguard" {
			t.Errorf("endpoint %v: want wireguard with private_key_set", e)
		}
	}
	m := byID[res[0].ID]
	if m == nil || m["mullvad_relay"] != "se-got-wg-001" || m["country"] != "se" {
		t.Errorf("mullvad endpoint = %v", m)
	}
	if o := byID[res[1].ID]; o == nil || o["name"] != "home" || o["mullvad_relay"] != nil {
		t.Errorf("other endpoint = %v", o)
	}
}

func TestImportEndpointSanitizesFileName(t *testing.T) {
	a := newAuthed(t)
	res := decodeImport(t, a.importFiles(upload{"dir/" + strings.Repeat("x", 300) + ".txt", "x"}))
	if len(res) != 1 || res[0].Status != "error" {
		t.Fatalf("results = %+v", res)
	}
	if strings.Contains(res[0].File, "/") || len([]rune(res[0].File)) > maxDisplayNameRunes {
		t.Errorf("file not sanitized: %q", res[0].File)
	}
	// Zip entry names reach displayName unfiltered; control characters must not be echoed.
	for in, want := range map[string]string{"a\x1b[31mb\x00.conf": "a[31mb.conf", "\n\t": "upload", "z.zip/c\rd": "z.zip/cd"} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportEndpointLimits(t *testing.T) {
	a := newAuthed(t)
	conf := importConf(fakeKey(t), fakeKey(t), fakeKey(t), "198.51.100.10:51820")
	before := a.store.Get().Endpoints

	big, ct := multipartBody(t, "files", upload{"big.conf", strings.Repeat("#", 5<<20+1)})
	if rec := a.importRaw(big, ct, withCookie(a.cookie), withCSRF(a.csrf)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: status %d, want 413", rec.Code)
	}
	if !reflect.DeepEqual(a.store.Get().Endpoints, before) {
		t.Error("store changed after oversize upload")
	}
	wrong, ct := multipartBody(t, "other", upload{"se-got-wg-001.conf", conf})
	if rec := a.importRaw(wrong, ct, withCookie(a.cookie), withCSRF(a.csrf)); rec.Code != http.StatusBadRequest {
		t.Errorf("no files parts: status %d, want 400", rec.Code)
	}
	if rec := a.importRaw(`{"files":[]}`, "application/json", withCookie(a.cookie), withCSRF(a.csrf)); rec.Code != http.StatusBadRequest {
		t.Errorf("non-multipart: status %d, want 400", rec.Code)
	}

	if res := decodeImport(t, a.importFiles(upload{"se-got-wg-001.conf", conf})); res[0].Status != "imported" {
		t.Fatalf("first import = %+v", res)
	}
	res := decodeImport(t, a.importFiles(upload{"se-got-wg-001.conf", conf}))
	if len(res) != 1 || res[0].Status != "skipped-duplicate" || res[0].ID != "" {
		t.Errorf("second import = %+v, want skipped-duplicate", res)
	}
	if n := len(a.listEndpoints()); n != 1 {
		t.Errorf("endpoint count = %d, want 1", n)
	}
}

func TestImportEndpointAuth(t *testing.T) {
	a := newAuthed(t)
	body, ct := multipartBody(t, "files", upload{"x.conf", "x"})
	if rec := a.importRaw(body, ct); rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: status %d, want 401", rec.Code)
	}
	if rec := a.importRaw(body, ct, withCookie(a.cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("no csrf: status %d, want 403", rec.Code)
	}
	if n := len(a.store.Get().Endpoints); n != 0 {
		t.Errorf("endpoints = %d, want 0", n)
	}
}

func TestImportNoKeyLeak(t *testing.T) {
	a := newAuthed(t)
	priv, psk := fakeKey(t), fakeKey(t)
	priv2, psk2 := fakeKey(t), fakeKey(t)
	rec := a.importFiles(
		upload{"se-got-wg-001.conf", importConf(priv, psk, fakeKey(t), "198.51.100.10:51820")},
		upload{"weird.conf", importConf(priv2, psk2, "not-a-key", "198.51.100.11:51820")},
	)
	var all bytes.Buffer
	all.Write(rec.Body.Bytes())
	res := decodeImport(t, rec)
	all.Write(a.call("GET", "/api/endpoints", "").Body.Bytes())
	for _, r := range res {
		if r.ID != "" {
			all.Write(a.call("GET", "/api/endpoints/"+r.ID, "").Body.Bytes())
		}
	}
	if res[0].ID == "" {
		t.Fatalf("expected an import: %+v", res)
	}
	all.Write(a.logs.Bytes())
	for _, k := range []string{priv, psk, priv2, psk2} {
		if strings.Contains(all.String(), k) {
			t.Errorf("secret %q leaked", k)
		}
	}
	if !strings.Contains(a.logs.String(), "imported=1") {
		t.Errorf("expected count log, got %s", a.logs)
	}
}
