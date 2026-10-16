package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestStoreSaveLoad(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	want := baseConfig(t)
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, ConfigFileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
	if names := listDir(t, dir); len(names) != 1 || names[0] != ConfigFileName {
		t.Fatalf("dir contents = %v, want only %s", names, ConfigFileName)
	}
	if !reflect.DeepEqual(s.Get(), want) {
		t.Fatalf("Get after Save differs")
	}

	s2 := openStore(t, dir)
	if le := s2.LoadError(); le != "" {
		t.Fatalf("LoadError = %q", le)
	}
	if got := s2.Get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// Overwrite keeps 0600 and leaves no temp file.
	if err := s2.Update(func(c *Config) error { c.Slots[0].Name = "renamed"; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	fi, _ = os.Stat(path)
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode after update = %o, want 600", mode)
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Fatalf("dir contents after update = %v", names)
	}
	if openStore(t, dir).Get().Slots[0].Name != "renamed" {
		t.Fatal("update not persisted")
	}
}

func TestStoreGetDeepCopy(t *testing.T) {
	s := openStore(t, t.TempDir())
	c := baseConfig(t)
	c.Slots[0].SocksAuth = &SocksAuth{Username: "u", Password: "p"}
	if err := s.Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Mutating the caller's value after Save must not affect the store.
	c.Slots[0].Route[0] = "mutated"
	got := s.Get()
	got.Slots[0].Route[0] = "x"
	got.Slots[0].SocksAuth.Username = "x"
	got.Endpoints[0].Addresses[0] = "x"
	got.Endpoints = append(got.Endpoints[:0], got.Endpoints[1:]...)
	again := s.Get()
	if again.Slots[0].Route[0] != "WG-SE" || again.Slots[0].SocksAuth.Username != "u" ||
		again.Endpoints[0].Addresses[0] != "10.64.0.2/32" || len(again.Endpoints) != 4 {
		t.Fatalf("store state leaked through Get/Save: %+v", again)
	}
}

func TestStoreSaveInvalid(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	good := baseConfig(t)
	if err := s.Save(good); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, ConfigFileName)
	before, _ := os.ReadFile(path)

	bad := baseConfig(t)
	bad.Slots[0].Route = []string{"GatewayProxy", "WG-SE"}
	var ve ValidationErrors
	if err := s.Save(bad); !errors.As(err, &ve) || len(ve) == 0 {
		t.Fatalf("Save(invalid) err = %v, want ValidationErrors", err)
	}
	err := s.Update(func(c *Config) error { c.Settings.UIBindAddr = "nope"; return nil })
	if !errors.As(err, &ve) {
		t.Fatalf("Update(invalid) err = %v, want ValidationErrors", err)
	}
	fnErr := errors.New("boom")
	if err := s.Update(func(c *Config) error { c.Slots = nil; return fnErr }); !errors.Is(err, fnErr) {
		t.Fatalf("Update fn error = %v, want boom", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("config.json changed after invalid save")
	}
	if !reflect.DeepEqual(s.Get(), good) {
		t.Fatal("in-memory config changed after invalid save")
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Fatalf("dir contents = %v", names)
	}
}

func TestStoreCorrupt(t *testing.T) {
	cases := map[string][]byte{
		"garbage": []byte("{not json at all"),
		"invalid": []byte(`{"endpoints":[],"slots":[],"settings":{"ui_bind_addr":"bogus"}}`),
	}
	tsRe := regexp.MustCompile(`^config\.json\.bad-\d{8}T\d{6}Z$`)
	for name, garbage := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ConfigFileName)
			if err := os.WriteFile(path, garbage, 0o600); err != nil {
				t.Fatal(err)
			}
			s := openStore(t, dir)
			if !reflect.DeepEqual(s.Get(), Default()) {
				t.Fatalf("Get = %+v, want Default()", s.Get())
			}
			if s.LoadError() == "" {
				t.Fatal("LoadError empty for corrupt config")
			}
			var bad string
			for _, n := range listDir(t, dir) {
				if strings.HasPrefix(n, ConfigFileName+".bad-") {
					bad = n
				}
			}
			if !tsRe.MatchString(bad) {
				t.Fatalf("quarantine file %q does not match %s", bad, tsRe)
			}
			got, _ := os.ReadFile(filepath.Join(dir, bad))
			if !bytes.Equal(got, garbage) {
				t.Fatal("quarantined bytes differ from original")
			}
			if cur, err := os.ReadFile(path); err == nil && !bytes.Equal(cur, garbage) {
				if s2 := openStore(t, dir); !reflect.DeepEqual(s2.Get(), Default()) {
					t.Fatal("config.json replaced by non-default content")
				}
			} else if err == nil {
				t.Fatal("corrupt config.json still in place")
			}
			// The store remains usable.
			if err := s.Save(baseConfig(t)); err != nil {
				t.Fatalf("Save after corrupt: %v", err)
			}
		})
	}
}

func TestStoreCorruptMessageHasParseError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("{not json"), 0o600)
	s := openStore(t, dir)
	if !strings.Contains(s.LoadError(), "invalid character") {
		t.Fatalf("LoadError = %q, want parse error text", s.LoadError())
	}
}

func TestStoreCorruptNoSecretInLoadError(t *testing.T) {
	dir := t.TempDir()
	c := baseConfig(t)
	secret := c.Endpoints[0].PrivateKey
	c.Settings.UIBindAddr = "bogus" // fails validation
	raw, _ := marshalConfig(c)
	os.WriteFile(filepath.Join(dir, ConfigFileName), raw, 0o600)
	s := openStore(t, dir)
	if s.LoadError() == "" || strings.Contains(s.LoadError(), secret) {
		t.Fatalf("LoadError = %q (empty or leaks secret)", s.LoadError())
	}
}

func TestStoreMissing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if le := s.LoadError(); le != "" {
		t.Fatalf("LoadError = %q, want empty", le)
	}
	if !reflect.DeepEqual(s.Get(), Default()) {
		t.Fatalf("Get = %+v, want Default()", s.Get())
	}
	if names := listDir(t, dir); len(names) != 0 {
		t.Fatalf("dir contents = %v, want empty", names)
	}
}

func TestStoreLoadAppliesHealthDefaults(t *testing.T) {
	dir := t.TempDir()
	raw := `{"endpoints":[{"id":"P","name":"P","kind":"socks5","host":"proxy.example.test","port":1080}],
"slots":[{"id":1,"name":"s","enabled":true,"bind_addr":"0.0.0.0","port":1080,"route":["P"],"killswitch":"block"}],
"settings":{"ui_bind_addr":"0.0.0.0:8787"}}`
	os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(raw), 0o600)
	s := openStore(t, dir)
	if le := s.LoadError(); le != "" {
		t.Fatalf("LoadError = %q", le)
	}
	if h := s.Get().Slots[0].Health; h != DefaultHealth() {
		t.Fatalf("health = %+v, want defaults", h)
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := openStore(t, t.TempDir())
	if err := s.Save(baseConfig(t)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.Get() }()
		go func(i int) {
			defer wg.Done()
			_ = s.Update(func(c *Config) error { c.Slots[0].Port = 2000 + i; return nil })
		}(i)
	}
	wg.Wait()
}

// TestQuarantinePrune: only the 3 newest
// config.json.bad-* survive a corrupt or quarantine-only boot. The file just created and
// the file salvage read are never pruned; pre-import backups and .lock never match.
func TestQuarantinePrune(t *testing.T) {
	const bad = quarantinePrefix
	older := []string{
		bad + "20261001T000000Z", bad + "20261002T000000Z", bad + "20261003T000000Z",
		bad + "20261004T000000Z", bad + "20261004T000000Z.1",
	}
	untouched := []string{
		backupPrefix + "20261001T000000Z", backupPrefix + "20261002T000000Z",
		backupPrefix + "20261003T000000Z", backupPrefix + "20261004T000000Z",
		backupPrefix + "20261005T000000Z", ".lock", bad + "zzz",
	}
	seed := func(t *testing.T, dir string, names []string) {
		t.Helper()
		files := map[string]string{}
		for _, n := range names {
			files[n] = credsJSON(testCreds(t))
		}
		writeFiles(t, dir, files)
	}
	assertPresent := func(t *testing.T, dir string, names ...string) {
		t.Helper()
		for _, n := range names {
			if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
				t.Fatalf("%s must survive prune: %v", n, err)
			}
		}
	}
	quarantineNames := func(t *testing.T, dir string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, n := range listDir(t, dir) {
			if _, _, ok := parseQuarantineName(n); ok {
				out[n] = true
			}
		}
		return out
	}

	t.Run("CorruptBoot", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir, append(append([]string{}, older...), untouched...))
		h, sec := testCreds(t)
		raw := credsJSON(h, sec)
		writeFiles(t, dir, map[string]string{ConfigFileName: raw})
		before := quarantineNames(t, dir)
		s := openStore(t, dir)
		got := quarantineNames(t, dir)
		var created string
		for n := range got {
			if !before[n] {
				created = n
			}
		}
		if created == "" {
			t.Fatal("no new quarantine file")
		}
		want := map[string]bool{created: true, older[4]: true, older[3]: true}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("quarantine files = %v, want %v", got, want)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, created)); string(b) != raw {
			t.Fatal("quarantined bytes changed")
		}
		if s.Get().Settings.AdminPasswordHash != h || !s.CredentialsSalvaged() {
			t.Fatal("salvage did not recover the hash from the new quarantine file")
		}
		assertPresent(t, dir, untouched...)
	})

	t.Run("CorruptBootClockBehind", func(t *testing.T) {
		dir := t.TempDir()
		future := []string{
			bad + "20991001T000000Z", bad + "20991002T000000Z", bad + "20991003T000000Z",
			bad + "20991004T000000Z", bad + "20991005T000000Z",
		}
		seed(t, dir, future)
		writeFiles(t, dir, map[string]string{ConfigFileName: "{not json"})
		before := quarantineNames(t, dir)
		openStore(t, dir)
		got := quarantineNames(t, dir)
		var created string
		for n := range got {
			if !before[n] {
				created = n
			}
		}
		if created == "" {
			t.Fatal("the file just created was pruned")
		}
		want := map[string]bool{created: true, future[4]: true, future[3]: true, future[2]: true}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("quarantine files = %v, want %v", got, want)
		}
	})

	t.Run("QuarantineOnlyBoot", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir, append([]string{}, untouched...))
		files := map[string]string{}
		for _, n := range older[:4] {
			files[n] = credsJSON(testCreds(t))
		}
		h, sec := testCreds(t)
		src := older[4]
		files[src] = credsJSON(h, sec)
		writeFiles(t, dir, files)
		s := openStore(t, dir)
		if s.Get().Settings.AdminPasswordHash != h {
			t.Fatal("salvage did not recover the hash")
		}
		want := map[string]bool{src: true, older[3]: true, older[2]: true}
		if got := quarantineNames(t, dir); !reflect.DeepEqual(got, want) {
			t.Fatalf("quarantine files = %v, want %v", got, want)
		}
		assertPresent(t, dir, untouched...)
	})
}
