package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// quarantineKeep is how many config.json.bad-* files are kept, newest by name.
const quarantineKeep = 3

// nameParser splits a managed file name into its timestamp and .N suffix; ok is false for
// names the caller does not manage, which are then never touched.
type nameParser func(name string) (ts string, n int, ok bool)

type pruneEntry struct {
	name string
	ts   string
	n    int
}

// pruneNewest removes all but the keep newest regular files in dir that parse accepts,
// ordered by timestamp and then by .N (base sorts before .1). Names in protect are never
// removed and do not count towards keep unless they are among the keep newest. Files
// whose names parse rejects (e.g. .lock) are never touched.
func pruneNewest(dir string, keep int, parse nameParser, protect ...string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var es []pruneEntry
	for _, e := range ents {
		ts, n, ok := parse(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		es = append(es, pruneEntry{name: e.Name(), ts: ts, n: n})
	}
	if len(es) <= keep {
		return nil
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].ts != es[j].ts {
			return es[i].ts > es[j].ts
		}
		return es[i].n > es[j].n
	})
	var firstErr error
	removed := false
	for _, e := range es[keep:] {
		if isProtected(e.name, protect) {
			continue
		}
		removed = true
		if err := os.Remove(filepath.Join(dir, e.name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if !removed {
		return firstErr
	}
	if err := syncDirFn(dir); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func isProtected(name string, protect []string) bool {
	for _, p := range protect {
		if name == p {
			return true
		}
	}
	return false
}

// pruneQuarantine keeps the quarantineKeep newest config.json.bad-* files plus keepName
// (the file just quarantined or the salvage source). File contents are
// never read or changed. A failure is logged and does not fail the boot.
func (s *Store) pruneQuarantine(keepName string) {
	if err := pruneNewest(s.dir, quarantineKeep, parseQuarantineName, keepName); err != nil {
		slog.Warn("could not prune old quarantined configs", "err", err)
	}
}
