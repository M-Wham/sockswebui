//go:build unix

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

// LockFileName is the data-dir lock file. It is never deleted, exported or pruned.
const LockFileName = ".lock"

// ErrLocked means another open lock (this or another process) holds the data dir.
var ErrLocked = errors.New("data dir is locked by another sockswebui process")

// flockFn is the flock(2) seam; only internal tests replace it.
var flockFn = syscall.Flock

var holderRe = regexp.MustCompile(`^pid=[0-9]{1,10}$`)

// LockDir takes an exclusive, non-blocking flock on <dir>/.lock for the caller's lifetime
// . The lock lives in the kernel, so exit, SIGKILL or power loss
// release it; a leftover file is inert. release closes the fd; callers keep it until exit.
//
// On ErrLocked, holder is the PID record of the current holder ("pid=N", or "unknown");
// it is informational only. On success, holder is the record written for this process,
// or "" if it could not be written (the lock is still held; the caller should warn).
func LockDir(dir string) (release func() error, holder string, err error) {
	path := filepath.Join(dir, LockFileName)
	f, created, err := openLockFile(path)
	if err != nil {
		return nil, "", err
	}
	if err := flockFn(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		switch {
		case errors.Is(err, syscall.EWOULDBLOCK):
			return nil, readHolder(f), ErrLocked
		case errors.Is(err, syscall.ENOLCK), errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.EOPNOTSUPP):
			return nil, "", fmt.Errorf("cannot lock data dir %s: %w; the data dir must be on a local filesystem", dir, err)
		default:
			return nil, "", fmt.Errorf("cannot lock data dir %s: %w", dir, err)
		}
	}
	if created {
		if err := chownToDirOwner(f, dir); err != nil {
			f.Close()
			return nil, "", fmt.Errorf("chown %s: %w", path, err)
		}
	}
	// release closes over f, keeping it reachable so no finalizer drops the lock (rule 1).
	release = func() error { return f.Close() }
	return release, writeHolder(f), nil
}

// openLockFile opens or creates the lock file (0600, close-on-exec). created reports
// whether this call created it. Both opens use O_NOFOLLOW, and the opened fd must be a
// regular file, so a planted symlink, directory or FIFO is refused and its target is
// never created, truncated or written. O_NONBLOCK keeps a FIFO
// open from blocking before the check. EACCES names the file's owner as a hint.
func openLockFile(path string) (*os.File, bool, error) {
	const flags = os.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, err := os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		f, err = os.OpenFile(path, flags, 0)
	}
	if err == nil {
		if err := requireRegular(f, path); err != nil {
			f.Close()
			return nil, false, err
		}
		return f, created, nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return nil, false, fmt.Errorf("open %s: refusing a symlinked lock file; remove it", path)
	}
	if errors.Is(err, os.ErrPermission) {
		if fi, serr := os.Lstat(path); serr == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				return nil, false, fmt.Errorf("open %s: %w (owned by uid %d; run as that user, e.g. sudo -u <owner> …)",
					path, err, st.Uid)
			}
		}
	}
	return nil, false, fmt.Errorf("open %s: %w", path, err)
}

// requireRegular fails unless the opened fd is a regular file (Fstat, no path lookup).
func requireRegular(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("open %s: lock file is not a regular file (%s); remove it", path, fi.Mode().Type())
	}
	return nil
}

// chownToDirOwner gives a root-created lock file to the data dir's owner, so the service
// user can open it later. It is a no-op unless euid is 0.
func chownToDirOwner(f *os.File, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return nil
	}
	return f.Chown(int(st.Uid), int(st.Gid))
}

// writeHolder records "pid=N\n" (truncate then write, no fsync). It returns the record
// without the newline, or "" if the write failed.
func writeHolder(f *os.File) string {
	rec := "pid=" + strconv.Itoa(os.Getpid())
	if err := f.Truncate(0); err != nil {
		return ""
	}
	if _, err := f.WriteAt([]byte(rec+"\n"), 0); err != nil {
		return ""
	}
	return rec
}

// readHolder returns the holder's PID record, or "unknown" if the file holds anything
// else (the content is untrusted and never echoed).
func readHolder(f *os.File) string {
	buf := make([]byte, 32)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "unknown"
	}
	s := string(buf[:n])
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	if !holderRe.MatchString(s) {
		return "unknown"
	}
	return s
}
