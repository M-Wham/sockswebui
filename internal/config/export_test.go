package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var exportNow = time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)

const exportPass = "correct horse battery staple"

// countingKDF is a cheap deterministic stand-in for argon2id. It records how often it ran.
type countingKDF struct{ calls int }

func (k *countingKDF) fn(passphrase, salt []byte, t, mKiB uint32, p uint8) ([]byte, error) {
	k.calls++
	h := sha256.New()
	h.Write(passphrase)
	h.Write(salt)
	h.Write([]byte{byte(t), byte(mKiB >> 8), byte(mKiB), p})
	return h.Sum(nil), nil
}

func exportID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "ep-" + hex.EncodeToString(b)
}

// exportFixture has a WG endpoint (private + preshared key), a socks5 endpoint with a password
// and a session (current_id), an http endpoint without secrets, and slots with health and
// enabled, one with SOCKS auth.
func exportFixture(t *testing.T) ExportPayload {
	t.Helper()
	wg := wgEndpoint(t, exportID(t))
	wg.PresharedKey = fakeKey(t)
	px := proxyEndpoint(exportID(t), KindSOCKS5)
	px.Username = "user-session-{session}"
	px.Password = "fixture-proxy-password-" + exportID(t)
	px.Session = Session{Charset: SessionAlnum, Length: 10, CurrentID: "Ab3dE6gH9k"}
	hp := proxyEndpoint(exportID(t), KindHTTP)
	s1 := testSlot(1, 1080, wg.ID, px.ID)
	s1.Health = Health{IntervalS: 45, URL: "https://example.test/ip", FailThreshold: 3}
	s1.SocksAuth = &SocksAuth{Username: "lan", Password: "fixture-socks-password-" + exportID(t)}
	s2 := testSlot(2, 1081, hp.ID)
	s2.Enabled = false
	s2.Killswitch = KillswitchAllowDirect
	return ExportPayload{Endpoints: []Endpoint{wg, px, hp}, Slots: []Slot{s1, s2}}
}

func sealFixture(t *testing.T, p ExportPayload, k *countingKDF) []byte {
	t.Helper()
	raw, err := SealEncrypted(rand.Reader, p, []byte(exportPass), k.fn, exportNow, "1.2.3")
	if err != nil {
		t.Fatalf("SealEncrypted: %v", err)
	}
	return raw
}

func parseFile(t *testing.T, raw []byte) ExportFile {
	t.Helper()
	var f ExportFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal file: %v", err)
	}
	return f
}

// editFile decodes raw into a generic map, applies fn and re-encodes it.
func editFile(t *testing.T, raw []byte, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func TestExportEncryptedRoundTrip(t *testing.T) {
	in := exportFixture(t)
	k := &countingKDF{}
	raw := sealFixture(t, in, k)
	for _, banned := range []string{"admin_password_hash", "session_secret", "ui_bind_addr"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Errorf("export file contains %q", banned)
		}
	}
	got, mode, err := OpenExport(raw, []byte(exportPass), k.fn)
	if err != nil {
		t.Fatalf("OpenExport: %v", err)
	}
	if mode != ExportModeEncrypted {
		t.Errorf("mode = %q", mode)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
	if got.Endpoints[1].Session.CurrentID != "Ab3dE6gH9k" {
		t.Errorf("current_id lost: %+v", got.Endpoints[1].Session)
	}
	if k.calls != 2 {
		t.Errorf("kdf calls = %d, want 2 (seal + open)", k.calls)
	}
}

func TestExportFileHeader(t *testing.T) {
	raw := sealFixture(t, exportFixture(t), &countingKDF{})
	f := parseFile(t, raw)
	if f.Format != ExportFormat || f.FormatVersion != 1 || f.Mode != ExportModeEncrypted ||
		f.AppVersion != "1.2.3" || f.CreatedAt != "2026-10-08T12:30:00Z" {
		t.Errorf("header = %+v", f)
	}
	if f.KDF == nil || *f.KDF != (KDFParams{Alg: "argon2id", T: 3, MKiB: 16384, P: 2, Salt: f.KDF.Salt}) {
		t.Errorf("kdf = %+v", f.KDF)
	}
	if f.Cipher == nil || f.Cipher.Alg != "AES-256-GCM" {
		t.Errorf("cipher = %+v", f.Cipher)
	}
	salt, err := base64.StdEncoding.DecodeString(f.KDF.Salt)
	if err != nil || len(salt) != 16 {
		t.Errorf("salt len = %d err %v, want 16", len(salt), err)
	}
	nonce, err := base64.StdEncoding.DecodeString(f.Cipher.Nonce)
	if err != nil || len(nonce) != 12 {
		t.Errorf("nonce len = %d err %v, want 12", len(nonce), err)
	}
	var s string
	if err := json.Unmarshal(f.Payload, &s); err != nil {
		t.Errorf("encrypted payload is not a string: %v", err)
	}
}

func TestExportFreshSaltAndNoncePerCall(t *testing.T) {
	in := exportFixture(t)
	a := parseFile(t, sealFixture(t, in, &countingKDF{}))
	b := parseFile(t, sealFixture(t, in, &countingKDF{}))
	if a.KDF.Salt == b.KDF.Salt {
		t.Error("salt reused across exports")
	}
	if a.Cipher.Nonce == b.Cipher.Nonce {
		t.Error("nonce reused across exports")
	}
	if bytes.Equal(a.Payload, b.Payload) {
		t.Error("ciphertext identical across exports")
	}
}

func TestExportAADGolden(t *testing.T) {
	k := &countingKDF{}
	seq := make([]byte, 28) // salt 0..15, nonce 16..27
	for i := range seq {
		seq[i] = byte(i)
	}
	raw, err := SealEncrypted(seqReader(seq...), exportFixture(t), []byte(exportPass), k.fn, exportNow, "dev")
	if err != nil {
		t.Fatalf("SealEncrypted: %v", err)
	}
	f := parseFile(t, raw)
	const want = "sockswebui-config\x001\x00encrypted\x00argon2id\x003\x0016384\x002\x00" +
		"AAECAwQFBgcICQoLDA0ODw==\x00AES-256-GCM\x00EBESExQVFhcYGRob"
	if got := string(exportAAD(f)); got != want {
		t.Fatalf("AAD = %q\nwant %q", got, want)
	}
	// The ciphertext must authenticate exactly these AAD bytes.
	var ct64 string
	if err := json.Unmarshal(f.Payload, &ct64); err != nil {
		t.Fatal(err)
	}
	ct, _ := base64.StdEncoding.DecodeString(ct64)
	salt, _ := base64.StdEncoding.DecodeString(f.KDF.Salt)
	nonce, _ := base64.StdEncoding.DecodeString(f.Cipher.Nonce)
	key, _ := k.fn([]byte(exportPass), salt, 3, 16384, 2)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if _, err := gcm.Open(nil, nonce, ct, []byte(want)); err != nil {
		t.Errorf("ciphertext does not open with golden AAD: %v", err)
	}
}

func TestExportRejectsUnknownFields(t *testing.T) {
	k := &countingKDF{}
	enc := sealFixture(t, exportFixture(t), k)
	ns, err := SealNoSecrets(exportFixture(t), exportNow, "dev")
	if err != nil {
		t.Fatalf("SealNoSecrets: %v", err)
	}
	cases := map[string][]byte{
		"outer": editFile(t, enc, func(m map[string]any) { m["extra"] = 1 }),
		"kdf":   editFile(t, enc, func(m map[string]any) { m["kdf"].(map[string]any)["extra"] = 1 }),
		"cipher": editFile(t, enc, func(m map[string]any) {
			m["cipher"].(map[string]any)["extra"] = 1
		}),
		"payload": editFile(t, ns, func(m map[string]any) { m["payload"].(map[string]any)["extra"] = 1 }),
		"endpoint": editFile(t, ns, func(m map[string]any) {
			m["payload"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)["extra"] = 1
		}),
		"slot": editFile(t, ns, func(m map[string]any) {
			m["payload"].(map[string]any)["slots"].([]any)[0].(map[string]any)["extra"] = 1
		}),
		"secrets_omitted": editFile(t, ns, func(m map[string]any) {
			m["payload"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)["secrets_omitted"] = []string{}
		}),
	}
	for name, raw := range cases {
		var pass []byte
		if name == "outer" || name == "kdf" || name == "cipher" {
			pass = []byte(exportPass)
		}
		if _, _, err := OpenExport(raw, pass, k.fn); err == nil || !errors.Is(err, ErrExportFormat) {
			t.Errorf("%s: err = %v, want ErrExportFormat", name, err)
		}
	}
	// Unknown field inside the encrypted payload.
	plain := []byte(`{"endpoints":[],"slots":[],"extra":1}`)
	raw, err := sealPlaintext(rand.Reader, plain, []byte(exportPass), k.fn, exportNow, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenExport(raw, []byte(exportPass), k.fn); !errors.Is(err, ErrExportFormat) {
		t.Errorf("encrypted payload unknown field: err = %v", err)
	}
}

func TestExportRejectsBadHeader(t *testing.T) {
	k := &countingKDF{}
	enc := sealFixture(t, exportFixture(t), k)
	b64 := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }
	cases := map[string]func(m map[string]any){
		"format":           func(m map[string]any) { m["format"] = "other" },
		"mode":             func(m map[string]any) { m["mode"] = "plaintext" },
		"format_version 2": func(m map[string]any) { m["format_version"] = 2 },
		"format_version 0": func(m map[string]any) { m["format_version"] = 0 },
		"salt 15":          func(m map[string]any) { m["kdf"].(map[string]any)["salt"] = b64(15) },
		"salt 17":          func(m map[string]any) { m["kdf"].(map[string]any)["salt"] = b64(17) },
		"salt not b64":     func(m map[string]any) { m["kdf"].(map[string]any)["salt"] = "!!!" },
		"nonce 11":         func(m map[string]any) { m["cipher"].(map[string]any)["nonce"] = b64(11) },
		"nonce 16":         func(m map[string]any) { m["cipher"].(map[string]any)["nonce"] = b64(16) },
		"cipher alg":       func(m map[string]any) { m["cipher"].(map[string]any)["alg"] = "AES-128-GCM" },
		"no kdf":           func(m map[string]any) { delete(m, "kdf") },
		"no cipher":        func(m map[string]any) { delete(m, "cipher") },
		"object payload":   func(m map[string]any) { m["payload"] = map[string]any{} },
		"mode no-secrets":  func(m map[string]any) { m["mode"] = ExportModeNoSecrets },
	}
	for name, fn := range cases {
		k.calls = 0
		_, _, err := OpenExport(editFile(t, enc, fn), []byte(exportPass), k.fn)
		if !errors.Is(err, ErrExportFormat) {
			t.Errorf("%s: err = %v, want ErrExportFormat", name, err)
		}
		if k.calls != 0 {
			t.Errorf("%s: kdf ran %d times before rejection", name, k.calls)
		}
	}
	if _, _, err := OpenExport([]byte(`not json`), nil, k.fn); !errors.Is(err, ErrExportFormat) {
		t.Errorf("not json: %v", err)
	}
	if _, _, err := OpenExport(append(enc, []byte(` {}`)...), []byte(exportPass), k.fn); !errors.Is(err, ErrExportFormat) {
		t.Errorf("trailing data: %v", err)
	}
}

func TestExportKDFParams(t *testing.T) {
	enc := sealFixture(t, exportFixture(t), &countingKDF{})
	cases := map[string]func(kdf map[string]any){
		"m_kib": func(kdf map[string]any) { kdf["m_kib"] = 65536 },
		"t":     func(kdf map[string]any) { kdf["t"] = 1 },
		"p":     func(kdf map[string]any) { kdf["p"] = 4 },
		"p big": func(kdf map[string]any) { kdf["p"] = 300 },
		"alg":   func(kdf map[string]any) { kdf["alg"] = "argon2i" },
	}
	for name, fn := range cases {
		k := &countingKDF{}
		raw := editFile(t, enc, func(m map[string]any) { fn(m["kdf"].(map[string]any)) })
		_, _, err := OpenExport(raw, []byte(exportPass), k.fn)
		if !errors.Is(err, ErrExportFormat) {
			t.Errorf("%s: err = %v, want ErrExportFormat", name, err)
		}
		if k.calls != 0 {
			t.Errorf("%s: kdf called %d times, want 0", name, k.calls)
		}
	}
}

func TestExportOmitsDeviceFieldsInNoSecrets(t *testing.T) {
	raw, err := SealNoSecrets(exportFixture(t), exportNow, "dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"admin_password_hash", "session_secret", "ui_bind_addr"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("no-secrets file contains %q", banned)
		}
	}
}
