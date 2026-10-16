package config

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swap sets *p to v for the duration of the test. The seams are package globals, so
// tests using it must not run in parallel.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// captureLog routes slog.Default into a buffer for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

var errInjected = errors.New("injected I/O error")

func failDirSync(string) error { return errInjected }

// diskConfig reads and decodes <dir>/config.json.
func diskConfig(t *testing.T, dir string) Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	c, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return c
}

func TestCommitRenameThenFsyncFail(t *testing.T) {
	t.Run("UpdateCommitted", func(t *testing.T) {
		s, dir := backupStore(t)
		logs := captureLog(t)
		swap(t, &syncDirFn, failDirSync)
		err := s.Update(func(c *Config) error { c.Endpoints[0].Name = "renamed"; return nil })
		if err != nil {
			t.Fatalf("Update = %v, want nil (rename reached disk)", err)
		}
		if got := s.Get().Endpoints[0].Name; got != "renamed" {
			t.Fatalf("Get() name = %q, want renamed", got)
		}
		if got := diskConfig(t, dir).Endpoints[0].Name; got != "renamed" {
			t.Fatalf("disk name = %q, want renamed", got)
		}
		if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
			t.Fatalf("ERROR lines = %d, want 1; logs:\n%s", n, logs)
		}
	})

	t.Run("ReplaceKeepsBackup", func(t *testing.T) {
		s, dir := backupStore(t)
		captureLog(t)
		swap(t, &syncDirFn, failDirSync)
		name, err := s.ReplaceWithBackupFunc(func(cur Config) (Config, error) {
			return backupCandidate(t, s2(cur)), nil
		}, nil)
		if err != nil {
			t.Fatalf("ReplaceWithBackupFunc = %v, want nil", err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("backup %q not kept: %v", name, err)
		}
		if got := s.Get().Endpoints[0].Name; got != "WG-NEW" {
			t.Fatalf("Get() name = %q, want WG-NEW", got)
		}
	})

	// Only a dir-fsync failure after a successful rename counts as committed.
	nothing := map[string]func(t *testing.T){
		"TempWrite": func(t *testing.T) {
			swap(t, &writeTempFn, func(*os.File, []byte) (int, error) { return 0, errInjected })
		},
		"FileFsync": func(t *testing.T) {
			swap(t, &syncFileFn, func(*os.File) error { return errInjected })
		},
		"Rename": func(t *testing.T) {
			swap(t, &renameFn, func(string, string) error { return errInjected })
		},
	}
	for name, inject := range nothing {
		t.Run(name+"NothingChanges", func(t *testing.T) {
			s, dir := backupStore(t)
			captureLog(t)
			before := diskConfig(t, dir).Endpoints[0].Name
			inject(t)
			err := s.Update(func(c *Config) error { c.Endpoints[0].Name = "renamed"; return nil })
			if !errors.Is(err, errInjected) {
				t.Fatalf("Update = %v, want the injected error", err)
			}
			if got := s.Get().Endpoints[0].Name; got != before {
				t.Fatalf("Get() name = %q, want unchanged %q", got, before)
			}
			if got := diskConfig(t, dir).Endpoints[0].Name; got != before {
				t.Fatalf("disk name = %q, want unchanged %q", got, before)
			}
			for _, n := range listDir(t, dir) {
				if strings.Contains(n, ".tmp-") {
					t.Fatalf("temp file left behind: %s", n)
				}
			}
		})
	}

	t.Run("QuarantineFsyncFail", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		logs := captureLog(t)
		swap(t, &syncDirFn, failDirSync)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open = %v, want success", err)
		}
		if !strings.Contains(s.LoadError(), ConfigFileName+".bad-") {
			t.Fatalf("LoadError = %q, want it to name the quarantine file", s.LoadError())
		}
		if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("config.json still present after quarantine: %v", err)
		}
		if !strings.Contains(logs.String(), "level=ERROR") {
			t.Fatalf("no ERROR logged for the quarantine fsync failure; logs:\n%s", logs)
		}
	})
}
