package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var backupT0 = time.Date(2026, 10, 8, 12, 30, 45, 0, time.UTC)

// backupStore returns a store with a saved baseConfig and a fixed clock.
func backupStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Save(baseConfig(t)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s.now = func() time.Time { return backupT0 }
	return s, dir
}

func backupCandidate(t *testing.T, s *Store) Config {
	t.Helper()
	c := s.Get()
	c.Endpoints = []Endpoint{wgEndpoint(t, "WG-NEW")}
	c.Slots = []Slot{testSlot(2, 2080, "WG-NEW")}
	c.Slots[0].Enabled = false
	return c
}

func backupNames(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, n := range listDir(t, dir) {
		if strings.HasPrefix(n, ConfigFileName+".pre-import-") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func TestBackupReplaceWritesBackup(t *testing.T) {
	s, dir := backupStore(t)
	before, err := marshalConfig(s.Get())
	if err != nil {
		t.Fatal(err)
	}
	cand := backupCandidate(t, s)
	name, err := s.ReplaceWithBackup(cand)
	if err != nil {
		t.Fatalf("ReplaceWithBackup: %v", err)
	}
	want := "config.json.pre-import-20261008T123045Z"
	if name != want {
		t.Fatalf("backup name = %q, want %q", name, want)
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("backup mode = %o, want 600", mode)
	}
	got, _ := os.ReadFile(filepath.Join(dir, name))
	if !bytes.Equal(got, before) {
		t.Fatalf("backup bytes differ from pre-replace config")
	}
	if !reflect.DeepEqual(s.Get(), cand) {
		t.Fatal("in-memory config is not the candidate")
	}
	if !reflect.DeepEqual(openStore(t, dir).Get(), cand) {
		t.Fatal("config.json does not hold the candidate")
	}
}

func TestBackupSameSecondSuffix(t *testing.T) {
	s, _ := backupStore(t)
	n1, err := s.ReplaceWithBackup(backupCandidate(t, s))
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.ReplaceWithBackup(backupCandidate(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if n1 != "config.json.pre-import-20261008T123045Z" || n2 != n1+".1" {
		t.Fatalf("names = %q, %q", n1, n2)
	}
}

func TestBackupKeepsThreeNewest(t *testing.T) {
	s, dir := backupStore(t)
	pre := filepath.Join(dir, ConfigFileName+".pre-import-")
	existing := []string{
		"20261001T000000Z", "20261002T000000Z", "20261002T000000Z.1", "20261003T000000Z", "20261009T000000Z",
	}
	for _, e := range existing {
		if err := os.WriteFile(pre+e, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := []string{"config.json.bad-20260101T000000Z", "notes.txt", "config.json.pre-import-garbage"}
	for _, k := range keep {
		if err := os.WriteFile(filepath.Join(dir, k), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The new backup (20261008T123045Z) is newer than all but 20261009.
	name, err := s.ReplaceWithBackup(backupCandidate(t, s))
	if err != nil {
		t.Fatal(err)
	}
	got := backupNames(t, dir)
	want := []string{
		"config.json.pre-import-20261003T000000Z",
		name,
		"config.json.pre-import-20261009T000000Z",
		"config.json.pre-import-garbage",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backups = %v, want %v", got, want)
	}
	for _, k := range keep {
		if _, err := os.Stat(filepath.Join(dir, k)); err != nil {
			t.Fatalf("%s removed: %v", k, err)
		}
	}
}

func TestBackupSuffixSortsAfterBase(t *testing.T) {
	s, dir := backupStore(t)
	pre := filepath.Join(dir, ConfigFileName+".pre-import-")
	for _, e := range []string{"20261009T000000Z", "20261009T000000Z.1", "20261009T000000Z.2"} {
		if err := os.WriteFile(pre+e, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// New backup at 20261008 is the oldest of four and is pruned itself.
	if _, err := s.ReplaceWithBackup(backupCandidate(t, s)); err != nil {
		t.Fatal(err)
	}
	want := []string{pre + "20261009T000000Z", pre + "20261009T000000Z.1", pre + "20261009T000000Z.2"}
	for i := range want {
		want[i] = filepath.Base(want[i])
	}
	if got := backupNames(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("backups = %v, want %v", got, want)
	}

	// With base, .1 and .10 of one timestamp plus an older one, .10 is newest.
	dir2 := t.TempDir()
	for _, e := range []string{"20261001T000000Z", "20261002T000000Z.10", "20261002T000000Z", "20261002T000000Z.2"} {
		if err := os.WriteFile(filepath.Join(dir2, ConfigFileName+".pre-import-"+e), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(dir2, 2); err != nil {
		t.Fatal(err)
	}
	want2 := []string{"config.json.pre-import-20261002T000000Z.10", "config.json.pre-import-20261002T000000Z.2"}
	if got := backupNames(t, dir2); !reflect.DeepEqual(got, want2) {
		t.Fatalf("backups = %v, want %v", got, want2)
	}
}

func assertUnchanged(t *testing.T, s *Store, dir string, wantCfg Config, wantRaw []byte) {
	t.Helper()
	if !reflect.DeepEqual(s.Get(), wantCfg) {
		t.Fatal("in-memory config changed")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if !bytes.Equal(raw, wantRaw) {
		t.Fatal("config.json changed")
	}
	for _, n := range listDir(t, dir) {
		if n != ConfigFileName {
			t.Fatalf("unexpected file left in data dir: %s", n)
		}
	}
}

func TestBackupWriteFailureInjected(t *testing.T) {
	s, dir := backupStore(t)
	cfg := s.Get()
	raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	boom := errors.New("disk full")
	s.writeFile = func(d, name string, data []byte) error {
		if strings.Contains(name, "pre-import") {
			return boom
		}
		return writeAtomic(d, name, data)
	}
	name, err := s.ReplaceWithBackup(backupCandidate(t, s))
	if !errors.Is(err, boom) || name != "" {
		t.Fatalf("got (%q, %v), want backup error", name, err)
	}
	assertUnchanged(t, s, dir, cfg, raw)
}

// TestBackupWriteFailureUnwritableDir makes the real writeAtomic fail (no injected
// writeFile) by putting a regular file where the data dir was: creating the temp file
// then fails with ENOTDIR, which root cannot bypass, unlike directory permissions.
func TestBackupWriteFailureUnwritableDir(t *testing.T) {
	s, dir := backupStore(t)
	cfg := s.Get()
	raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	restore := replaceDirWithFile(t, dir)
	_, err := s.ReplaceWithBackup(backupCandidate(t, s))
	restore()
	if err == nil {
		t.Fatal("expected error for unwritable data dir")
	}
	assertUnchanged(t, s, dir, cfg, raw)
}

// replaceDirWithFile moves dir aside and puts a regular file at its path, so any write
// into dir fails even for root. The returned func puts dir back; it is also registered
// with t.Cleanup and is safe to call twice.
func replaceDirWithFile(t *testing.T, dir string) func() {
	t.Helper()
	aside := dir + ".aside"
	if err := os.Rename(dir, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := false
	restore := func() {
		if done {
			return
		}
		done = true
		if err := os.Remove(dir); err != nil {
			t.Errorf("remove placeholder: %v", err)
		}
		if err := os.Rename(aside, dir); err != nil {
			t.Errorf("restore data dir: %v", err)
		}
	}
	t.Cleanup(restore)
	return restore
}

func TestBackupConfigWriteFailureRemovesBackup(t *testing.T) {
	s, dir := backupStore(t)
	cfg := s.Get()
	raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	boom := errors.New("io error")
	s.writeFile = func(d, name string, data []byte) error {
		if name == ConfigFileName {
			return boom
		}
		return writeAtomic(d, name, data)
	}
	if _, err := s.ReplaceWithBackup(backupCandidate(t, s)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want config write error", err)
	}
	assertUnchanged(t, s, dir, cfg, raw)
}

func TestBackupInvalidCandidate(t *testing.T) {
	s, dir := backupStore(t)
	cfg := s.Get()
	raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	cand := backupCandidate(t, s)
	cand.Slots[0].Port = 0
	name, err := s.ReplaceWithBackup(cand)
	var ve ValidationErrors
	if !errors.As(err, &ve) || name != "" {
		t.Fatalf("got (%q, %v), want validation error", name, err)
	}
	assertUnchanged(t, s, dir, cfg, raw)
}

func TestBackupReplaceAtomicWithUpdate(t *testing.T) {
	s, dir := backupStore(t)
	before, _ := marshalConfig(s.Get())
	paused := make(chan struct{})
	release := make(chan struct{})
	s.replaceHook = func() {
		close(paused)
		<-release
	}
	type result struct {
		name string
		err  error
	}
	repDone := make(chan result, 1)
	cand := backupCandidate(t, s)
	go func() {
		n, err := s.ReplaceWithBackup(cand)
		repDone <- result{n, err}
	}()
	<-paused
	updDone := make(chan error, 1)
	go func() {
		updDone <- s.Update(func(c *Config) error { c.Slots[0].Name = "after"; return nil })
	}()
	select {
	case err := <-updDone:
		t.Fatalf("Update landed during a paused replace: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	r := <-repDone
	if r.err != nil {
		t.Fatalf("ReplaceWithBackup: %v", r.err)
	}
	if err := <-updDone; err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, r.name))
	if !bytes.Equal(got, before) {
		t.Fatal("backup does not hold the pre-replace config")
	}
	final := s.Get()
	if len(final.Slots) != 1 || final.Slots[0].ID != 2 || final.Slots[0].Name != "after" {
		t.Fatalf("Update did not land after the replace: %+v", final.Slots)
	}
}

// TestBackupOmitsAdminSecrets: a pre-import backup has admin_password_hash and
// session_secret empty but keeps every endpoint and slot secret.
func TestBackupOmitsAdminSecrets(t *testing.T) {
	s, dir := backupStore(t)
	if err := s.Update(func(c *Config) error {
		c.Settings.AdminPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$a2V5a2V5a2V5"
		c.Settings.SessionSecret = "session-secret-canary"
		c.Endpoints[0].PresharedKey = fakeKey(t)
		c.Endpoints[2].Username, c.Endpoints[2].Password = "u", "proxy-pass-canary"
		c.Slots[0].SocksAuth = &SocksAuth{Username: "su", Password: "socks-pass-canary"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	before := s.Get()
	name, err := s.ReplaceWithBackup(backupCandidate(t, s))
	if err != nil {
		t.Fatalf("ReplaceWithBackup: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("admin_password_hash")) || bytes.Contains(raw, []byte("session_secret")) ||
		bytes.Contains(raw, []byte("session-secret-canary")) || bytes.Contains(raw, []byte("argon2id")) {
		t.Fatal("backup holds the admin hash or session secret")
	}
	got, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if got.Settings.AdminPasswordHash != "" || got.Settings.SessionSecret != "" {
		t.Fatalf("backup settings hold admin creds")
	}
	want := cloneConfig(before)
	want.Settings.AdminPasswordHash, want.Settings.SessionSecret = "", ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backup differs from pre-replace config beyond admin creds:\n got %+v\nwant %+v", got, want)
	}
	if s.Get().Settings.SessionSecret != "session-secret-canary" {
		t.Fatal("live config lost its session secret")
	}
}
