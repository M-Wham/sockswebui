package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestSlotBlockLANRoundTrip: block_lan_targets survives save/load, cloneConfig and both
// export modes.
func TestSlotBlockLANRoundTrip(t *testing.T) {
	c := baseConfig(t)
	c.Slots = append(c.Slots, testSlot(2, 1081))
	c.Slots[1].Killswitch = KillswitchAllowDirect
	c.Slots[1].BlockLANTargets = true

	if got := cloneConfig(c); !got.Slots[1].BlockLANTargets || !reflect.DeepEqual(got, c) {
		t.Fatalf("cloneConfig lost block_lan_targets: %+v", got.Slots)
	}

	dir := t.TempDir()
	if err := openStore(t, dir).Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if !bytes.Contains(raw, []byte(`"block_lan_targets": true`)) {
		t.Fatalf("config.json lacks block_lan_targets: %s", raw)
	}
	if got := openStore(t, dir).Get(); !got.Slots[1].BlockLANTargets || got.Slots[0].BlockLANTargets {
		t.Fatalf("load: %+v", got.Slots)
	}

	p := exportFixture(t)
	p.Slots[1].BlockLANTargets = true
	k := &countingKDF{}
	got, _, err := OpenExport(sealFixture(t, p, k), []byte(exportPass), k.fn)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("encrypted round trip: err %v\n got %+v\nwant %+v", err, got, p)
	}
	ns, err := SealNoSecrets(p, time.Now(), "1.2.3")
	if err != nil {
		t.Fatalf("SealNoSecrets: %v", err)
	}
	got, _, err = OpenExport(ns, nil, k.fn)
	if err != nil || !got.Slots[1].BlockLANTargets || got.Slots[0].BlockLANTargets {
		t.Fatalf("no-secrets round trip: err %v slots %+v", err, got.Slots)
	}
}

// TestStoreLoadOldConfigBlockLANDefault: a config.json written before block_lan_targets existed
// loads with false, is not rewritten or quarantined; an unknown extra field still loads.
func TestStoreLoadOldConfigBlockLANDefault(t *testing.T) {
	for name, extra := range map[string]string{"old": "", "unknown-field": `,"future_field":{"x":1}`} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			raw := []byte(`{"endpoints":[{"id":"P","name":"P","kind":"socks5","host":"proxy.example.test","port":1080}],
"slots":[{"id":1,"name":"s","enabled":true,"bind_addr":"0.0.0.0","port":1080,"route":["P"],"killswitch":"block"` + extra + `}],
"settings":{"ui_bind_addr":"0.0.0.0:8787"}}`)
			path := filepath.Join(dir, ConfigFileName)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			s := openStore(t, dir)
			if le := s.LoadError(); le != "" {
				t.Fatalf("LoadError = %q", le)
			}
			if sl := s.Get().Slots; len(sl) != 1 || sl[0].BlockLANTargets {
				t.Fatalf("slots = %+v", sl)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, raw) {
				t.Fatalf("config.json rewritten:\n%s", after)
			}
			if names := listDir(t, dir); len(names) != 1 || names[0] != ConfigFileName {
				t.Fatalf("dir = %v, want only %s", names, ConfigFileName)
			}
		})
	}
}
