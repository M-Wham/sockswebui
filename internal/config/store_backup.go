package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// backupPrefix starts every pre-import backup name.
const backupPrefix = ConfigFileName + ".pre-import-"

// backupKeep is how many pre-import backups are kept, newest by name timestamp.
const backupKeep = 3

var backupNameRe = regexp.MustCompile(`^` + regexp.QuoteMeta(backupPrefix) + `(\d{8}T\d{6}Z)(?:\.(\d+))?$`)

// ReplaceWithBackup validates candidate and, under one hold of the store lock, writes the
// current config to <dir>/config.json.pre-import-<UTC ts>[.N] and then replaces the config
// with candidate. Both writes go through s.write, i.e. writeAtomic (0600, fsync, rename).
// It returns the backup file name. If validation, the backup write or the config write
// fails, nothing changes and no backup is kept. Afterwards only the backupKeep newest
// backups remain; a pruning failure does not fail the replace. Backups hold every endpoint
// and slot secret in plaintext but not the admin hash or session secret (see backupCopy).
func (s *Store) ReplaceWithBackup(candidate Config) (backupName string, err error) {
	return s.ReplaceWithBackupFunc(func(Config) (Config, error) { return candidate, nil }, nil)
}

// ReplaceWithBackupFunc is ReplaceWithBackup with the candidate built under the same lock
// hold: build receives a deep copy of the current config, so device-owned fields can be
// copied from it and preconditions checked without a race. A build error aborts with
// nothing changed. beforeCommit, if non-nil, runs (with the lock held) after the backup
// is written and before the replace; it must not call back into the store.
func (s *Store) ReplaceWithBackupFunc(build func(current Config) (Config, error), beforeCommit func()) (backupName string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, err := build(cloneConfig(s.cfg))
	if err != nil {
		return "", err
	}
	c := cloneConfig(candidate)
	if err := Validate(c); err != nil {
		return "", err
	}
	raw, err := marshalConfig(backupCopy(s.cfg))
	if err != nil {
		return "", err
	}
	path, err := uniquePath(filepath.Join(s.dir, backupPrefix+s.now().UTC().Format(quarantineTimeFormat)))
	if err != nil {
		return "", fmt.Errorf("write pre-import backup: %w", err)
	}
	name := filepath.Base(path)
	if err := s.write(name, raw); err != nil {
		return "", fmt.Errorf("write pre-import backup: %w", err)
	}
	if s.replaceHook != nil {
		s.replaceHook()
	}
	if beforeCommit != nil {
		beforeCommit()
	}
	if err := s.commit(c); err != nil {
		_ = os.Remove(filepath.Join(s.dir, name))
		_ = syncDirFn(s.dir)
		return "", err
	}
	_ = pruneBackups(s.dir, backupKeep)
	return name, nil
}

// backupCopy returns a deep copy of c with AdminPasswordHash and SessionSecret emptied,
// so a restored backup boots into open first-run setup.
func backupCopy(c Config) Config {
	out := cloneConfig(c)
	out.Settings.AdminPasswordHash = ""
	out.Settings.SessionSecret = ""
	return out
}

// parseBackupName splits config.json.pre-import-<ts>[.N] into ts and N (0 when absent).
func parseBackupName(name string) (ts string, n int, ok bool) {
	m := backupNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	if m[2] != "" {
		var err error
		if n, err = strconv.Atoi(m[2]); err != nil {
			return "", 0, false
		}
	}
	return m[1], n, true
}

// pruneBackups removes all but the keep newest pre-import backups in dir (see pruneNewest).
func pruneBackups(dir string, keep int) error {
	return pruneNewest(dir, keep, parseBackupName)
}
