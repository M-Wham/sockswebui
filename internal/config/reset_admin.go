//go:build unix

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// ErrNoConfig means <dir>/config.json does not exist; ResetAdmin wrote nothing.
var ErrNoConfig = errors.New("no config.json")

// ResetAdmin clears settings.admin_password_hash and settings.session_secret in
// <dir>/config.json and keeps everything else (--reset-admin).
// The caller must hold LockDir. It never calls Open, so a corrupt file is never
// quarantined or salvaged: an absent file returns ErrNoConfig, an unreadable, corrupt or
// invalid one returns an error, and in both cases nothing is written. The new file is
// written atomically with mode 0600; when run as root it keeps the original uid/gid
// (Fchown on the temp file before the rename), so the service user can still open it.
// It returns the stored ui_bind_addr. Errors never contain config values.
func ResetAdmin(dir string) (uiBindAddr string, err error) {
	path := filepath.Join(dir, ConfigFileName)
	raw, st, err := readRegular(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoConfig
	}
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	cfg, err := decodeConfig(raw)
	if err == nil {
		err = Validate(cfg)
	}
	if err != nil {
		return "", fmt.Errorf("%s is corrupt or invalid: %w", ConfigFileName, err)
	}
	cfg.Settings.AdminPasswordHash, cfg.Settings.SessionSecret = "", ""
	data, err := marshalConfig(cfg)
	if err != nil {
		return "", err
	}
	keepOwner := func(f *os.File) error {
		if os.Geteuid() != 0 {
			return nil
		}
		return f.Chown(int(st.Uid), int(st.Gid))
	}
	err = writeAtomicWith(dir, ConfigFileName, data, keepOwner)
	if errors.Is(err, errDirSyncAfterRename) {
		// The new file is in place; only its durability is in doubt.
		slog.Error("config written but not yet durable; keeping it as committed", "file", ConfigFileName, "err", err)
		err = nil
	}
	if err != nil {
		return "", err
	}
	return cfg.Settings.UIBindAddr, nil
}

// readRegular reads path without following a final symlink and only if it is a regular
// file, returning its contents and owner.
func readRegular(path string) ([]byte, *syscall.Stat_t, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok {
		return nil, nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return raw, st, nil
}

// HashBearingFiles lists the leftovers in dir that may still hold an admin password hash:
// every quarantined config.json.bad-* file (raw copies, salvage reads the
// hash from them) and every config.json.pre-import-* backup whose settings carry a
// non-empty admin_password_hash, or that cannot be read or parsed (listed to be safe).
// Names are sorted; nothing is changed or deleted, and file contents are never returned.
func HashBearingFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list data dir: %w", err)
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if _, _, ok := parseQuarantineName(name); ok {
			out = append(out, name)
			continue
		}
		if _, _, ok := parseBackupName(name); ok && backupHoldsHash(filepath.Join(dir, name)) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// backupHoldsHash reports whether a pre-import backup holds an admin hash; an unreadable
// or unparsable backup counts as holding one.
func backupHoldsHash(path string) bool {
	raw, _, err := readRegular(path)
	if err != nil {
		return true
	}
	var b struct {
		Settings struct {
			Hash string `json:"admin_password_hash"`
		} `json:"settings"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return true
	}
	return b.Settings.Hash != ""
}
