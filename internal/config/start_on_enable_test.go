package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An old config file has no start_on_enable field; it loads as true.
func TestSettingsStartOnEnableDefault(t *testing.T) {
	dir := t.TempDir()
	old := `{"endpoints":[],"slots":[],"settings":{"ui_bind_addr":"127.0.0.1:8787"}}`
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	if le := s.LoadError(); le != "" {
		t.Fatalf("load error: %s", le)
	}
	if !s.Get().Settings.StartOnEnable {
		t.Error("missing start_on_enable loaded as false, want true")
	}
	if !Default().Settings.StartOnEnable {
		t.Error("Default() start_on_enable = false, want true")
	}

	// An explicit false survives a save and a reload.
	if err := s.Update(func(c *Config) error { c.Settings.StartOnEnable = false; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"start_on_enable": false`) {
		t.Errorf("false not written to file:\n%s", raw)
	}
	if openStore(t, dir).Get().Settings.StartOnEnable {
		t.Error("explicit false reloaded as true")
	}
}
