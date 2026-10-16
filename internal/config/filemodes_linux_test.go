//go:build linux

package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Every write path leaves 0600 files and a 0700 data dir,
// independent of the process umask. These tests set a process-wide umask of 0, so they
// must never call t.Parallel.

// permissiveUmask sets umask 0 for the rest of the test and restores it in cleanup.
func permissiveUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

// modeDataDir creates a fresh data dir the way ensureDataDir does (0700).
func modeDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %#o, want %#o", filepath.Base(path), got, want)
	}
}

// assertDirModes checks dir is 0700 and every entry in it is a regular 0600 file, and that
// no temp file was left behind.
func assertDirModes(t *testing.T, dir string) {
	t.Helper()
	assertPerm(t, dir, 0o700)
	for _, name := range listDir(t, dir) {
		if strings.Contains(name, ".tmp-") {
			t.Errorf("leftover temp file %s", name)
		}
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !fi.Mode().IsRegular() {
			t.Errorf("%s is not a regular file (%v)", name, fi.Mode())
			continue
		}
		assertPerm(t, filepath.Join(dir, name), 0o600)
	}
}

func TestFileModes(t *testing.T) {
	permissiveUmask(t)
	dir := modeDataDir(t)
	mustLock(t, dir)
	cfgPath := filepath.Join(dir, ConfigFileName)

	// Save.
	s := openStore(t, dir)
	if err := s.Save(baseConfig(t)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertPerm(t, cfgPath, 0o600)

	// Pre-import backup (and the replace that follows it).
	backup, err := s.ReplaceWithBackup(baseConfig(t))
	if err != nil {
		t.Fatalf("ReplaceWithBackup: %v", err)
	}
	assertPerm(t, filepath.Join(dir, backup), 0o600)
	assertPerm(t, cfgPath, 0o600)

	// Quarantine of a corrupt config.json (written 0600 as Save would) and the salvage write.
	hash, secret := testCreds(t)
	if err := os.Remove(cfgPath); err != nil { // os.WriteFile keeps an existing file's mode
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{ConfigFileName: credsJSON(hash, secret)})
	s = openStore(t, dir)
	if !s.CredentialsSalvaged() {
		t.Fatalf("expected salvage; LoadError=%q", s.LoadError())
	}
	bad := quarantined(t, dir)
	if len(bad) != 1 {
		t.Fatalf("quarantine files = %v, want 1", bad)
	}
	assertPerm(t, bad[0], 0o600) // quarantined returns full paths
	assertPerm(t, cfgPath, 0o600)

	// Salvage write on a quarantine-only boot (no config.json).
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if s = openStore(t, dir); !s.CredentialsSalvaged() {
		t.Fatalf("expected salvage from quarantine; LoadError=%q", s.LoadError())
	}
	assertPerm(t, cfgPath, 0o600)

	assertDirModes(t, dir)
}

func TestLockDirMode(t *testing.T) {
	permissiveUmask(t)
	dir := modeDataDir(t)
	mustLock(t, dir)
	assertPerm(t, filepath.Join(dir, LockFileName), 0o600)
	assertDirModes(t, dir)
}
