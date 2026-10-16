package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Fuzz targets for the two decoders that read untrusted bytes from disk or upload:
// decodeConfig (boot) and OpenExport (import). Seeds come from
// f.Add (keys generated fresh on every run) and testdata/fuzz/<Name>/ (fake keys generated once
// for the corpus). Both targets are hermetic: no network, a stub KDF, a temp data dir.

// fuzzKey returns a freshly generated random base64 32-byte key. It is never a real key.
func fuzzKey(tb testing.TB) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		tb.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// fuzzEndpointID returns a random id of the shape an export file must carry (ep-<16 hex>).
func fuzzEndpointID(tb testing.TB) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		tb.Fatalf("rand: %v", err)
	}
	return "ep-" + hex.EncodeToString(b)
}

// fuzzConfig returns a valid config with one fake-keyed WireGuard endpoint and one slot.
func fuzzConfig(tb testing.TB) Config {
	id := fuzzEndpointID(tb)
	c := Default()
	c.Endpoints = []Endpoint{{
		ID: id, Name: "wg", Kind: KindWireGuard, PrivateKey: fuzzKey(tb),
		Addresses: []string{"10.64.0.2/32"}, DNS: []string{"10.64.0.1"},
		PeerPublicKey: fuzzKey(tb), Endpoint: "198.51.100.10:51820",
		AllowedIPs: []string{"0.0.0.0/0", "::/0"},
	}}
	c.Slots = []Slot{testSlot(1, 1080, id)}
	return c
}

// newFuzzStore opens a store in a fresh dir holding a valid config, and returns the config
// and file bytes that every failed write must leave in place.
func newFuzzStore(f *testing.F) (*Store, Config, []byte) {
	dir := f.TempDir()
	s, err := Open(dir)
	if err != nil {
		f.Fatal(err)
	}
	if err := s.Save(fuzzConfig(f)); err != nil {
		f.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		f.Fatal(err)
	}
	return s, s.Get(), file
}

// checkStoreWrite asserts the outcome of a store write fed by a decoder: on error memory and
// disk are unchanged; on success the original config is restored for the next input.
func checkStoreWrite(t *testing.T, s *Store, werr error, want Config, wantFile []byte) {
	t.Helper()
	if werr == nil {
		if err := s.Save(want); err != nil {
			t.Fatalf("restore store: %v", err)
		}
		return
	}
	if got := s.Get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("store memory changed after failed write (%v)", werr)
	}
	file, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file, wantFile) {
		t.Fatalf("config.json changed after failed write (%v)", werr)
	}
}

func FuzzDecodeConfig(f *testing.F) {
	valid, err := marshalConfig(fuzzConfig(f))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"slots":[{"id":1,"health":{}}]}`))
	f.Add([]byte(`{"settings":{"admin_password_hash":"x"`))
	f.Add([]byte(`null`))
	s, orig, origFile := newFuzzStore(f)

	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := decodeConfig(raw)
		if err == nil && Validate(c) == nil {
			// A config that boots must survive a save and reload byte for byte.
			once, err := marshalConfig(c)
			if err != nil {
				t.Fatalf("marshal decoded config: %v", err)
			}
			back, err := decodeConfig(once)
			if err != nil {
				t.Fatalf("re-decode saved config: %v", err)
			}
			twice, _ := marshalConfig(back)
			if !bytes.Equal(once, twice) {
				t.Fatalf("save/reload not stable:\n%s\n%s", once, twice)
			}
		}

		werr := s.Update(func(dst *Config) error {
			got, err := decodeConfig(raw)
			if err != nil {
				return err
			}
			*dst = got
			return nil
		})
		checkStoreWrite(t, s, werr, orig, origFile)

		checkBootFrom(t, raw)
	})
}

// checkBootFrom writes raw as config.json and boots: Open never fails on bad content, and a
// rejected file is quarantined with LoadError set.
func checkBootFrom(t *testing.T, raw []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open on bad content returned an error: %v", err)
	}
	c, derr := decodeConfig(raw)
	if derr == nil {
		derr = Validate(c)
	}
	bad, _ := filepath.Glob(filepath.Join(dir, quarantinePrefix+"*"))
	if derr == nil {
		if s.LoadError() != "" || len(bad) != 0 {
			t.Fatalf("valid config was quarantined: %q", s.LoadError())
		}
		return
	}
	if s.LoadError() == "" || len(bad) != 1 {
		t.Fatalf("rejected config not quarantined: loadErr=%q bad=%v", s.LoadError(), bad)
	}
}

// fuzzExportPayload returns a payload whose endpoint ids pass the import id rule and whose
// config passes Validate, so the success path reaches the store write.
func fuzzExportPayload(tb testing.TB) ExportPayload {
	c := fuzzConfig(tb)
	return ExportPayload{Endpoints: c.Endpoints, Slots: c.Slots}
}

func FuzzOpenExport(f *testing.F) {
	k := &countingKDF{}
	p := fuzzExportPayload(f)
	enc, err := SealEncrypted(rand.Reader, p, []byte(exportPass), k.fn, exportNow, "dev")
	if err != nil {
		f.Fatal(err)
	}
	plain, err := SealNoSecrets(p, exportNow, "dev")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(enc, []byte(exportPass))
	f.Add(enc, []byte("wrong passphrase"))
	f.Add(plain, []byte(nil))
	f.Add(plain, []byte(exportPass))
	f.Add([]byte(`{"format":"sockswebui-config","format_version":1,"mode":"no-secrets","payload":{}}`), []byte(nil))
	f.Add([]byte(`{"format":"sockswebui-config"`), []byte(nil))
	s, orig, origFile := newFuzzStore(f)

	f.Fuzz(func(t *testing.T, raw, pass []byte) {
		k := &countingKDF{}
		_, mode, err := OpenExport(raw, pass, k.fn)
		checkExportOutcome(t, mode, err, k.calls)

		werr := s.Update(func(c *Config) error {
			got, _, err := OpenExport(raw, pass, k.fn)
			if err != nil {
				return err
			}
			c.Endpoints, c.Slots = got.Endpoints, got.Slots
			return nil
		})
		checkStoreWrite(t, s, werr, orig, origFile)
	})
}

// checkExportOutcome asserts OpenExport fails only with its documented errors, keeps the
// decrypt error opaque, and never runs the KDF for a no-secrets file.
func checkExportOutcome(t *testing.T, mode string, err error, kdfCalls int) {
	t.Helper()
	var ve ValidationErrors
	switch {
	case err == nil:
		if mode != ExportModeEncrypted && mode != ExportModeNoSecrets {
			t.Fatalf("success with mode %q", mode)
		}
		if mode == ExportModeNoSecrets && kdfCalls != 0 {
			t.Fatalf("KDF ran %d times for a no-secrets file", kdfCalls)
		}
	case errors.Is(err, ErrDecrypt):
		if err.Error() != ErrDecrypt.Error() {
			t.Fatalf("decrypt error not opaque: %q", err)
		}
	case errors.Is(err, ErrExportFormat), errors.As(err, &ve):
	default:
		t.Fatalf("undocumented error type %T: %v", err, err)
	}
	if err != nil && strings.Contains(err.Error(), exportPass) {
		t.Fatalf("error echoes the passphrase: %v", err)
	}
}

// TestFuzzSeedCorpus pins the checked-in corpus: each target has seeds under testdata/fuzz
// (go test runs them with the f.Add seeds on every plain test run).
func TestFuzzSeedCorpus(t *testing.T) {
	for _, name := range []string{"FuzzDecodeConfig", "FuzzOpenExport"} {
		files, _ := filepath.Glob(filepath.Join("testdata", "fuzz", name, "*"))
		if len(files) < 2 {
			t.Errorf("%s: %d seed files under testdata/fuzz, want >= 2", name, len(files))
		}
	}
}
