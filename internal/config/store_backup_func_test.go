package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReplaceWithBackupFuncBuildsFromCurrent(t *testing.T) {
	s, dir := backupStore(t)
	before := s.Get()
	var seen Config
	var hookRan bool
	name, err := s.ReplaceWithBackupFunc(func(cur Config) (Config, error) {
		seen = cur
		c := backupCandidate(t, s2(cur))
		c.Settings = cur.Settings
		return c, nil
	}, func() {
		hookRan = true
		if _, err := os.Stat(filepath.Join(dir, backupPrefix+backupT0.UTC().Format(quarantineTimeFormat))); err != nil {
			t.Errorf("beforeCommit ran before the backup was written: %v", err)
		}
		if !reflect.DeepEqual(s.cfg, before) {
			t.Error("beforeCommit ran after the replace")
		}
	})
	if err != nil {
		t.Fatalf("ReplaceWithBackupFunc: %v", err)
	}
	if !hookRan {
		t.Error("beforeCommit not called")
	}
	if !reflect.DeepEqual(seen, before) {
		t.Error("build did not receive the current config")
	}
	if !strings.HasPrefix(name, backupPrefix) || s.Get().Endpoints[0].Name != "WG-NEW" {
		t.Fatalf("replace not applied: name %q cfg %+v", name, s.Get())
	}
}

// s2 lets backupCandidate run on a config already in hand (it only needs Get()).
func s2(c Config) *Store { return &Store{cfg: c} }

func TestReplaceWithBackupFuncAborts(t *testing.T) {
	boom := errors.New("raced")
	cases := map[string]func(*Store) (func(Config) (Config, error), func()){
		"BuildError": func(*Store) (func(Config) (Config, error), func()) {
			return func(Config) (Config, error) { return Config{}, boom }, nil
		},
		"Invalid": func(s *Store) (func(Config) (Config, error), func()) {
			return func(cur Config) (Config, error) {
				c := backupCandidate(t, s2(cur))
				c.Slots[0].Port = 0
				return c, nil
			}, nil
		},
		"BackupWrite": func(s *Store) (func(Config) (Config, error), func()) {
			s.writeFile = func(d, name string, data []byte) error {
				if strings.Contains(name, "pre-import") {
					return boom
				}
				return writeAtomic(d, name, data)
			}
			return func(cur Config) (Config, error) { return backupCandidate(t, s2(cur)), nil }, nil
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			s, dir := backupStore(t)
			cfg := s.Get()
			raw, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
			build, _ := mk(s)
			hook := false
			got, err := s.ReplaceWithBackupFunc(build, func() { hook = true })
			if err == nil || got != "" {
				t.Fatalf("got (%q, %v), want error", got, err)
			}
			if hook {
				t.Error("beforeCommit ran on an aborted replace")
			}
			assertUnchanged(t, s, dir, cfg, raw)
		})
	}
}
