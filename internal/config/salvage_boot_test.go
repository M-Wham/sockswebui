package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A boot with no config.json but a config.json.bad-* present must
// salvage from the newest quarantine file, never come up as a fresh device.

// credsJSON is a corrupt config ("slots" has the wrong type) whose settings are salvageable.
func credsJSON(hash, secret string) string {
	return fmt.Sprintf(`{"slots":"oops","settings":{"admin_password_hash":%q,"session_secret":%q}}`, hash, secret)
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// assertSalvagedBoot checks that s holds exactly hash/secret, LoadError names src, and the
// salvaged config was written to config.json (so setup → 409 and the next boot is clean).
func assertSalvagedBoot(t *testing.T, s *Store, dir, src, hash, secret string) {
	t.Helper()
	got := s.Get()
	if got.Settings.AdminPasswordHash != hash || got.Settings.SessionSecret != secret {
		t.Fatal("store does not hold the credentials salvaged from the newest quarantine file")
	}
	if !s.CredentialsSalvaged() {
		t.Fatal("CredentialsSalvaged = false")
	}
	le := s.LoadError()
	if !strings.Contains(le, src) {
		t.Fatalf("LoadError = %q, want it to name %s", le, src)
	}
	if strings.Contains(le, hash) || strings.Contains(le, secret) {
		t.Fatal("LoadError leaks a secret")
	}
	disk := diskConfig(t, dir)
	if disk.Settings.AdminPasswordHash != hash || disk.Settings.SessionSecret != secret {
		t.Fatal("config.json not written with the salvaged credentials")
	}
	if _, err := os.Stat(filepath.Join(dir, src)); err != nil {
		t.Fatalf("salvage source %s must be kept: %v", src, err)
	}
	if s2 := openStore(t, dir); s2.LoadError() != "" || s2.Get().Settings.AdminPasswordHash != hash {
		t.Fatalf("third boot not clean: LoadError=%q", s2.LoadError())
	}
}

func TestQuarantineFsyncFailNextBoot(t *testing.T) {
	const bad = ConfigFileName + ".bad-"
	t.Run("NewestOfTwoWins", func(t *testing.T) {
		dir := t.TempDir()
		h1, s1 := testCreds(t)
		h2, s2 := testCreds(t)
		t1, t2 := bad+"20261001T000000Z", bad+"20261002T000000Z"
		writeFiles(t, dir, map[string]string{t1: credsJSON(h1, s1), t2: credsJSON(h2, s2)})
		assertSalvagedBoot(t, openStore(t, dir), dir, t2, h2, s2)
	})

	t.Run("DotNOrdering", func(t *testing.T) {
		dir := t.TempDir()
		h, s := testCreds(t)
		ts := bad + "20261002T000000Z"
		files := map[string]string{
			bad + "20261001T000000Z.99": credsJSON(testCreds(t)),
			ts:                          credsJSON(testCreds(t)),
			ts + ".2":                   credsJSON(testCreds(t)),
			ts + ".9":                   credsJSON(testCreds(t)),
			ts + ".10":                  credsJSON(h, s),
			// Not a quarantine name: never a salvage source even though it sorts last.
			bad + "zzz": credsJSON(testCreds(t)),
		}
		writeFiles(t, dir, files)
		assertSalvagedBoot(t, openStore(t, dir), dir, ts+".10", h, s)
	})

	t.Run("PowerLossAfterQuarantine", func(t *testing.T) {
		dir := t.TempDir()
		h, sec := testCreds(t)
		writeFiles(t, dir, map[string]string{ConfigFileName: credsJSON(h, sec)})
		captureLog(t)
		swap(t, &syncDirFn, failDirSync)
		if _, err := Open(dir); err != nil {
			t.Fatalf("first boot Open = %v", err)
		}
		// Neither the quarantine rename nor the salvage write was made durable; the power
		// fails and only the rename survives.
		if err := os.Remove(filepath.Join(dir, ConfigFileName)); err != nil {
			t.Fatal(err)
		}
		swap(t, &syncDirFn, syncDir)
		src := filepath.Base(quarantined(t, dir)[0])
		assertSalvagedBoot(t, openStore(t, dir), dir, src, h, sec)
	})

	t.Run("UnsalvageableNotFresh", func(t *testing.T) {
		dir := t.TempDir()
		src := bad + "20261002T000000Z"
		writeFiles(t, dir, map[string]string{
			// The older file is salvageable, but only the newest is a source.
			bad + "20261001T000000Z": credsJSON(testCreds(t)),
			src:                      "{not json",
		})
		s := openStore(t, dir)
		if s.Get().Settings.AdminPasswordHash != "" {
			t.Fatal("hash set from a non-newest quarantine file")
		}
		if s.CredentialsSalvaged() {
			t.Fatal("CredentialsSalvaged = true")
		}
		if !strings.Contains(s.LoadError(), src) {
			t.Fatalf("LoadError = %q, want it to name %s (cmd then logs the setup-open WARN)", s.LoadError(), src)
		}
		if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("config.json written although nothing was salvaged: %v", err)
		}
		if len(quarantined(t, dir)) != 2 {
			t.Fatal("quarantine files must be left in place")
		}
	})

	t.Run("PreImportNeverASource", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			backupPrefix + "20261002T000000Z":           credsJSON(testCreds(t)),
			ConfigFileName + ".pre-import-x.bad-202610": credsJSON(testCreds(t)),
		})
		s := openStore(t, dir)
		if s.LoadError() != "" || s.CredentialsSalvaged() || s.Get().Settings.AdminPasswordHash != "" {
			t.Fatalf("pre-import backup used as salvage source: LoadError=%q", s.LoadError())
		}
	})

	t.Run("UnreadableNewestFailsOpen", func(t *testing.T) {
		dir := t.TempDir()
		// A directory with a quarantine name cannot be read: refuse to start (Q2 → A)
		// rather than come up with setup open.
		if err := os.Mkdir(filepath.Join(dir, bad+"20261002T000000Z"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Fatal("Open succeeded with an unreadable newest quarantine file")
		}
	})
}

func TestSalvageWriteFailNextBoot(t *testing.T) {
	dir := t.TempDir()
	h, sec := testCreds(t)
	writeFiles(t, dir, map[string]string{ConfigFileName: credsJSON(h, sec)})
	// Boot 1: the quarantine rename works, the salvage write's rename into config.json fails.
	swap(t, &renameFn, func(from, to string) error {
		if filepath.Base(to) == ConfigFileName {
			return errInjected
		}
		return os.Rename(from, to)
	})
	s1, err := Open(dir)
	if err != nil {
		t.Fatalf("boot 1 Open = %v", err)
	}
	if s1.Get().Settings.AdminPasswordHash != h || !strings.Contains(s1.LoadError(), "saving them failed") {
		t.Fatalf("boot 1: want in-memory salvage with a save failure, LoadError=%q", s1.LoadError())
	}
	if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("boot 1 left config.json: %v", err)
	}
	swap(t, &renameFn, os.Rename)
	src := filepath.Base(quarantined(t, dir)[0])
	assertSalvagedBoot(t, openStore(t, dir), dir, src, h, sec)
}
