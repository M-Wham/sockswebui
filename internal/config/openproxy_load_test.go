package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestOpenProxyConfigLoads is a permanent regression guard. OPEN-SOCKS = C:
// an open proxy (bind 0.0.0.0, no socks_auth) is warn-only, so an existing config with one
// must keep loading at boot. A future Validate rule that rejects this fixture would
// quarantine such configs on upgrade; changing this test must be a deliberate decision.
func TestOpenProxyConfigLoads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "open-proxy-slot.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var want Config
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(want.Slots) != 1 || want.Slots[0].BindAddr != "0.0.0.0" || want.Slots[0].SocksAuth != nil {
		t.Fatalf("fixture must hold one slot bound to 0.0.0.0 without socks_auth, got %+v", want.Slots)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if le := s.LoadError(); le != "" {
		t.Fatalf("LoadError = %q, want empty (open-proxy config must not be quarantined)", le)
	}
	if got := s.Get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded config differs from fixture:\n got %+v\nwant %+v", got, want)
	}
	bad, err := filepath.Glob(filepath.Join(dir, ConfigFileName+".bad-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("quarantine files created: %v", bad)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if !bytes.Equal(onDisk, raw) {
		t.Fatal("config.json was rewritten by Open")
	}
}
