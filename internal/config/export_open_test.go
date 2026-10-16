package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const decryptMsg = "could not decrypt: wrong passphrase or damaged file"

func assertDecryptErr(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrDecrypt) {
		t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		return
	}
	if err.Error() != decryptMsg {
		t.Errorf("%s: message = %q, want %q", name, err.Error(), decryptMsg)
	}
}

func TestExportOpenErrors(t *testing.T) {
	k := &countingKDF{}
	small := ExportPayload{Endpoints: []Endpoint{}, Slots: []Slot{}}
	enc, err := SealEncrypted(rand.Reader, small, []byte(exportPass), k.fn, exportNow, "dev")
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = OpenExport(enc, []byte("a different passphrase"), k.fn)
	assertDecryptErr(t, "wrong passphrase", err)

	f := parseFile(t, enc)
	var ct64 string
	if err := json.Unmarshal(f.Payload, &ct64); err != nil {
		t.Fatal(err)
	}
	ct, _ := base64.StdEncoding.DecodeString(ct64)
	for i := range ct {
		flipped := append([]byte(nil), ct...)
		flipped[i] ^= 0x01
		raw := editFile(t, enc, func(m map[string]any) { m["payload"] = base64.StdEncoding.EncodeToString(flipped) })
		_, _, err := OpenExport(raw, []byte(exportPass), k.fn)
		assertDecryptErr(t, "flipped byte", err)
	}

	other := func(n int) string {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return base64.StdEncoding.EncodeToString(b)
	}
	salt := editFile(t, enc, func(m map[string]any) { m["kdf"].(map[string]any)["salt"] = other(16) })
	_, _, err = OpenExport(salt, []byte(exportPass), k.fn)
	assertDecryptErr(t, "edited salt", err)
	nonce := editFile(t, enc, func(m map[string]any) { m["cipher"].(map[string]any)["nonce"] = other(12) })
	_, _, err = OpenExport(nonce, []byte(exportPass), k.fn)
	assertDecryptErr(t, "edited nonce", err)

	// mode, format_version and kdf.t have only one accepted value each, so OpenExport rejects
	// edits to them as format errors before decryption (TestExportRejectsBadHeader,
	// TestExportKDFParams). Here we prove they are bound by the AAD: decrypting with the edited
	// header (and the original key) fails with the same sentinel.
	saltB, _ := base64.StdEncoding.DecodeString(f.KDF.Salt)
	key, _ := k.fn([]byte(exportPass), saltB, 3, 16384, 2)
	if _, err := decryptPayload(f, key); err != nil {
		t.Fatalf("unedited header must decrypt: %v", err)
	}
	edits := map[string]func(g *ExportFile){
		"mode":           func(g *ExportFile) { g.Mode = ExportModeNoSecrets },
		"format_version": func(g *ExportFile) { g.FormatVersion = 2 },
		"kdf.t":          func(g *ExportFile) { kd := *g.KDF; kd.T = 4; g.KDF = &kd },
		"kdf.m_kib":      func(g *ExportFile) { kd := *g.KDF; kd.MKiB = 8; g.KDF = &kd },
		"kdf.p":          func(g *ExportFile) { kd := *g.KDF; kd.P = 1; g.KDF = &kd },
	}
	for name, fn := range edits {
		g := f
		fn(&g)
		_, err := decryptPayload(g, key)
		assertDecryptErr(t, "edited "+name, err)
	}

	// app_version and created_at are informational and not authenticated.
	info := editFile(t, enc, func(m map[string]any) {
		m["app_version"] = "9.9.9"
		m["created_at"] = "2001-01-01T00:00:00Z"
	})
	if _, _, err := OpenExport(info, []byte(exportPass), k.fn); err != nil {
		t.Errorf("app_version/created_at edit must not fail decryption: %v", err)
	}
}

func TestExportNoSecrets(t *testing.T) {
	in := exportFixture(t)
	secrets := []string{
		in.Endpoints[0].PrivateKey, in.Endpoints[0].PresharedKey,
		in.Endpoints[1].Password, in.Slots[0].SocksAuth.Password,
	}
	raw, err := SealNoSecrets(in, exportNow, "dev")
	if err != nil {
		t.Fatalf("SealNoSecrets: %v", err)
	}
	for i, s := range secrets {
		if s == "" || strings.Contains(string(raw), s) {
			t.Errorf("secret %d present in no-secrets file (or empty fixture)", i)
		}
	}
	if in.Slots[0].SocksAuth.Password != secrets[3] || in.Endpoints[0].PrivateKey != secrets[0] {
		t.Error("SealNoSecrets mutated its input")
	}

	var file struct {
		Mode    string `json:"mode"`
		Payload struct {
			Endpoints []map[string]any `json:"endpoints"`
			Slots     []map[string]any `json:"slots"`
		} `json:"payload"`
		KDF    any `json:"kdf"`
		Cipher any `json:"cipher"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Mode != ExportModeNoSecrets || file.KDF != nil || file.Cipher != nil {
		t.Errorf("header mode=%q kdf=%v cipher=%v", file.Mode, file.KDF, file.Cipher)
	}
	omitted := func(m map[string]any) []string {
		var out []string
		if l, ok := m["omitted"].([]any); ok {
			for _, v := range l {
				out = append(out, v.(string))
			}
		}
		return out
	}
	wantEp := [][]string{{OmittedPrivateKey, OmittedPresharedKey}, {OmittedPassword}, nil}
	for i, w := range wantEp {
		if got := omitted(file.Payload.Endpoints[i]); !slices.Equal(got, w) {
			t.Errorf("endpoints[%d].omitted = %v, want %v", i, got, w)
		}
	}
	wantSl := [][]string{{OmittedSocksPassword}, nil}
	for i, w := range wantSl {
		if got := omitted(file.Payload.Slots[i]); !slices.Equal(got, w) {
			t.Errorf("slots[%d].omitted = %v, want %v", i, got, w)
		}
	}

	k := &countingKDF{}
	if _, _, err := OpenExport(raw, []byte(exportPass), k.fn); !errors.Is(err, ErrExportFormat) {
		t.Errorf("passphrase with no-secrets: err = %v, want ErrExportFormat", err)
	}
	got, mode, err := OpenExport(raw, nil, k.fn)
	if err != nil {
		t.Fatalf("OpenExport no-secrets: %v", err)
	}
	if mode != ExportModeNoSecrets || k.calls != 0 {
		t.Errorf("mode = %q kdf calls = %d", mode, k.calls)
	}
	want := StripSecrets(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded payload:\n got %+v\nwant %+v", got, want)
	}
	if !slices.Equal(got.Endpoints[0].SecretsOmitted, []string{OmittedPrivateKey, OmittedPresharedKey}) ||
		!slices.Equal(got.Endpoints[1].SecretsOmitted, []string{OmittedPassword}) ||
		got.Endpoints[2].SecretsOmitted != nil ||
		!slices.Equal(got.Slots[0].SecretsOmitted, []string{OmittedSocksPassword}) {
		t.Errorf("SecretsOmitted not mapped: %+v / %+v", got.Endpoints, got.Slots)
	}
	if got.Endpoints[1].Session.CurrentID != "Ab3dE6gH9k" {
		t.Errorf("session lost: %+v", got.Endpoints[1].Session)
	}
}

func TestExportNoSecretsRejectsBadOmitted(t *testing.T) {
	raw, err := SealNoSecrets(exportFixture(t), exportNow, "dev")
	if err != nil {
		t.Fatal(err)
	}
	ep := func(m map[string]any, i int) map[string]any {
		return m["payload"].(map[string]any)["endpoints"].([]any)[i].(map[string]any)
	}
	cases := map[string]func(m map[string]any){
		"unknown name": func(m map[string]any) { ep(m, 0)["omitted"] = []string{"username"} },
		"slot name on endpoint": func(m map[string]any) {
			ep(m, 0)["omitted"] = []string{OmittedSocksPassword}
		},
		"listed but present": func(m map[string]any) {
			ep(m, 1)["password"] = "back-again"
		},
	}
	for name, fn := range cases {
		if _, _, err := OpenExport(editFile(t, raw, fn), nil, (&countingKDF{}).fn); !errors.Is(err, ErrExportFormat) {
			t.Errorf("%s: err = %v, want ErrExportFormat", name, err)
		}
	}
}

func TestExportNoSecretsEndpointIDCheck(t *testing.T) {
	k := &countingKDF{}
	for _, bad := range []string{"WG-SE", "ep-0123", "ep-0123456789ABCDEF", "ep-0123456789abcdef0", "xep-0123456789abcdef"} {
		in := exportFixture(t)
		in.Endpoints[2].ID = bad
		in.Slots[1].Route = []string{bad}
		for _, mode := range []string{ExportModeNoSecrets, ExportModeEncrypted} {
			var raw []byte
			var err error
			var pass []byte
			if mode == ExportModeNoSecrets {
				raw, err = SealNoSecrets(in, exportNow, "dev")
			} else {
				pass = []byte(exportPass)
				raw, err = SealEncrypted(rand.Reader, in, pass, k.fn, exportNow, "dev")
			}
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = OpenExport(raw, pass, k.fn)
			var ve ValidationErrors
			if !errors.As(err, &ve) || !hasField(ve, "endpoints[2].id") {
				t.Errorf("%s id %q: err = %v, want field error endpoints[2].id", mode, bad, err)
			}
		}
	}
}

func TestExportStripSecretsKeepsExistingOmitted(t *testing.T) {
	in := exportFixture(t)
	in.Endpoints[1].Password = ""
	in.Endpoints[1].SecretsOmitted = []string{OmittedPassword}
	out := StripSecrets(in)
	if !slices.Equal(out.Endpoints[1].SecretsOmitted, []string{OmittedPassword}) {
		t.Errorf("omitted = %v", out.Endpoints[1].SecretsOmitted)
	}
	if in.Endpoints[0].SecretsOmitted != nil {
		t.Error("StripSecrets mutated input")
	}
}
