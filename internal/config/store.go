package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConfigFileName is the config file inside the data dir.
const ConfigFileName = "config.json"

// quarantineTimeFormat is the UTC timestamp suffix of config.json.bad-<ts>.
const quarantineTimeFormat = "20060102T150405Z"

// Store holds the current config in memory and persists it atomically to
// <dir>/config.json with mode 0600. It is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	dir     string
	cfg     Config
	loadErr string
	salvage bool
	now     func() time.Time

	// writeFile, when set, replaces writeAtomic (tests inject write failures).
	writeFile func(dir, name string, data []byte) error
	// replaceHook, when set, runs inside ReplaceWithBackup after the backup is written
	// and before the replace, with s.mu held (tests pause there).
	replaceHook func()
}

// Open loads <dir>/config.json. A missing file yields Default(), unless a quarantine file
// config.json.bad-* exists: then the newest one is a salvage source and LoadError names it,
// so a quarantine-only boot is never a fresh device. A file that fails to
// parse or validate is moved to config.json.bad-<ts>; the store then holds Default() and
// LoadError reports why. If the admin password hash and session secret can
// be salvaged from the bad bytes, they are carried into the default config, which is
// persisted so setup stays locked across restarts. Only unexpected I/O failures return
// an error.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, cfg: Default(), now: time.Now}
	raw, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		if err := s.bootFromQuarantine(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, perr := decodeConfig(raw)
	if perr == nil {
		perr = Validate(cfg)
	}
	if perr == nil {
		s.cfg = cfg
		return s, nil
	}
	bad, qerr := s.quarantine()
	if qerr != nil {
		return nil, fmt.Errorf("quarantine corrupt config: %w", qerr)
	}
	s.loadErr = fmt.Sprintf("config file was corrupt and has been moved to %s: %v", filepath.Base(bad), perr)
	s.salvageFrom(raw)
	s.pruneQuarantine(filepath.Base(bad))
	return s, nil
}

// salvageFrom carries salvageable admin credentials from raw into the default config and
// persists it. A failed write keeps the in-memory salvage and is noted in loadErr. It
// reports whether the salvaged config was persisted.
func (s *Store) salvageFrom(raw []byte) bool {
	hash, secret, ok := salvageCredentials(raw)
	if !ok {
		return false
	}
	c := Default()
	c.Settings.AdminPasswordHash = hash
	c.Settings.SessionSecret = secret
	s.salvage = true
	if err := s.commit(c); err != nil {
		s.cfg = c
		s.loadErr += fmt.Sprintf("; admin credentials were recovered but saving them failed: %v", err)
		return false
	}
	s.loadErr += "; admin credentials were recovered"
	return true
}

// CredentialsSalvaged reports whether Open recovered the admin password hash and session
// secret from a corrupt config. It is false when the config was not corrupt.
func (s *Store) CredentialsSalvaged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.salvage
}

// LoadError is non-empty when the config on disk was corrupt at Open time.
func (s *Store) LoadError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// Get returns a deep copy of the current config.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneConfig(s.cfg)
}

// Save validates c and, if valid, persists it atomically. On any error nothing changes.
func (s *Store) Save(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(cloneConfig(c))
}

// Update applies fn to a copy of the current config, validates and persists the result.
// If fn or validation fails, nothing changes and the error is returned.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := cloneConfig(s.cfg)
	if err := fn(&c); err != nil {
		return err
	}
	return s.commit(c)
}

// commit validates, writes and swaps in c. The caller holds s.mu and owns c.
func (s *Store) commit(c Config) error {
	if err := Validate(c); err != nil {
		return err
	}
	raw, err := marshalConfig(c)
	if err != nil {
		return err
	}
	if err := s.write(ConfigFileName, raw); err != nil {
		return err
	}
	s.cfg = c
	return nil
}

// write persists data to <dir>/name with the atomic writer (or the injected one).
// A dir-fsync failure after the rename succeeded counts as written: the new file is
// already visible, so memory must follow it. It is logged at ERROR once.
// Every other error means the old file is untouched and is returned.
func (s *Store) write(name string, data []byte) error {
	var err error
	if s.writeFile != nil {
		err = s.writeFile(s.dir, name, data)
	} else {
		err = writeAtomic(s.dir, name, data)
	}
	if errors.Is(err, errDirSyncAfterRename) {
		slog.Error("config written but not yet durable; keeping it as committed", "file", name, "err", err)
		return nil
	}
	return err
}

func (s *Store) path() string { return filepath.Join(s.dir, ConfigFileName) }

// quarantine renames config.json to config.json.bad-<ts> without overwriting an
// existing quarantine file, sets it to 0600, and returns the new path. Once the rename succeeded the
// quarantine has happened: a failed dir fsync is logged at ERROR and not returned.
func (s *Store) quarantine() (string, error) {
	dst, err := uniquePath(filepath.Join(s.dir, quarantinePrefix) + s.now().UTC().Format(quarantineTimeFormat))
	if err != nil {
		return "", err
	}
	if err := renameFn(s.path(), dst); err != nil {
		return "", err
	}
	// The quarantined bytes may hold the admin hash and WG keys: never keep a looser
	// source mode. A failure only warns; boot goes on.
	if err := os.Chmod(dst, 0o600); err != nil {
		slog.Warn("could not restrict quarantined config to 0600", "file", filepath.Base(dst), "err", err)
	}
	if err := syncDirFn(s.dir); err != nil {
		slog.Error("corrupt config quarantined but not yet durable", "file", filepath.Base(dst), "err", err)
	}
	return dst, nil
}

// uniquePath returns base, or base.N with the lowest N >= 1, whichever does not exist yet.
// Any Lstat error other than "not exist" (e.g. the directory is unreadable or is not a
// directory) is returned, so a broken data dir fails fast instead of looping forever.
func uniquePath(base string) (string, error) {
	dst := base
	for i := 1; ; i++ {
		_, err := os.Lstat(dst)
		if errors.Is(err, fs.ErrNotExist) {
			return dst, nil
		}
		if err != nil {
			return "", err
		}
		dst = fmt.Sprintf("%s.%d", base, i)
	}
}

func marshalConfig(c Config) ([]byte, error) {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append(raw, '\n'), nil
}

// decodeConfig parses raw JSON and applies defaults for omitted optional fields
// (nil lists, per-slot health fields).
func decodeConfig(raw []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if c.Endpoints == nil {
		c.Endpoints = []Endpoint{}
	}
	if c.Slots == nil {
		c.Slots = []Slot{}
	}
	def := DefaultHealth()
	for i := range c.Slots {
		h := &c.Slots[i].Health
		if h.IntervalS == 0 {
			h.IntervalS = def.IntervalS
		}
		if h.URL == "" {
			h.URL = def.URL
		}
		if h.FailThreshold == 0 {
			h.FailThreshold = def.FailThreshold
		}
	}
	return c, nil
}

// writeAtomic writes data to dir/name via a 0600 temp file in the same dir:
// write, fsync, close, rename, fsync dir. The temp file is removed on failure. A failure
// of the final dir fsync wraps errDirSyncAfterRename.
// errDirSyncAfterRename marks a writeAtomic failure that happened after the rename
// succeeded: the new file is in place, only the directory fsync failed. It is the only
// write error the store treats as committed.
var errDirSyncAfterRename = errors.New("config renamed into place but the dir fsync failed")

// I/O seams for writeAtomic and quarantine; internal tests swap them to inject failures.
var (
	writeTempFn = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	syncFileFn  = (*os.File).Sync
	renameFn    = os.Rename
	syncDirFn   = syncDir
)

func writeAtomic(dir, name string, data []byte) error { return writeAtomicWith(dir, name, data, nil) }

// writeAtomicWith is writeAtomic with prep, when non-nil, run on the temp file right after
// its chmod and before any data is written or it is renamed (e.g. Fchown).
func writeAtomicWith(dir, name string, data []byte, prep func(*os.File) error) (err error) {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if prep != nil {
		if err = prep(f); err != nil {
			_ = f.Close()
			return fmt.Errorf("prepare temp config: %w", err)
		}
	}
	if _, err = writeTempFn(f, data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err = syncFileFn(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync temp config: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err = renameFn(tmp, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	if serr := syncDirFn(dir); serr != nil {
		return fmt.Errorf("%w: %w", errDirSyncAfterRename, serr)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open config dir: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync config dir: %w", err)
	}
	return nil
}

func cloneConfig(c Config) Config {
	out := Config{Settings: c.Settings}
	if c.Endpoints != nil {
		out.Endpoints = make([]Endpoint, len(c.Endpoints))
		for i, e := range c.Endpoints {
			e.Addresses = cloneStrings(e.Addresses)
			e.DNS = cloneStrings(e.DNS)
			e.AllowedIPs = cloneStrings(e.AllowedIPs)
			e.SecretsOmitted = cloneStrings(e.SecretsOmitted)
			out.Endpoints[i] = e
		}
	}
	if c.Slots != nil {
		out.Slots = make([]Slot, len(c.Slots))
		for i, sl := range c.Slots {
			sl.Route = cloneStrings(sl.Route)
			sl.SecretsOmitted = cloneStrings(sl.SecretsOmitted)
			if sl.SocksAuth != nil {
				a := *sl.SocksAuth
				sl.SocksAuth = &a
			}
			out.Slots[i] = sl
		}
	}
	return out
}

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	return append(make([]string, 0, len(s)), s...)
}
