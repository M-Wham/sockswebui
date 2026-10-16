//go:build unix

package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const lockHelperEnv = "SOCKSWEBUI_LOCK_HELPER_DIR"

// mustLock takes the lock on dir or fails the test; release runs at cleanup.
func mustLock(t *testing.T, dir string) func() error {
	t.Helper()
	release, _, err := LockDir(dir)
	if err != nil {
		t.Fatalf("LockDir: %v", err)
	}
	var once bool
	rel := func() error {
		if once {
			return nil
		}
		once = true
		return release()
	}
	t.Cleanup(func() { _ = rel() })
	return rel
}

func TestLockDirSecondHolderRefused(t *testing.T) {
	dir := t.TempDir()
	release := mustLock(t, dir)
	_, holder, err := LockDir(dir)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second LockDir err = %v, want ErrLocked", err)
	}
	if want := fmt.Sprintf("pid=%d", os.Getpid()); !strings.Contains(holder, want) {
		t.Errorf("holder = %q, want it to contain %q", holder, want)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	mustLock(t, dir)
}

func TestLockDirSurvivesGC(t *testing.T) {
	dir := t.TempDir()
	mustLock(t, dir)
	runtime.GC()
	runtime.GC()
	if _, _, err := LockDir(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("LockDir after GC err = %v, want ErrLocked", err)
	}
}

// TestLockDirReleasedOnKill re-execs the test binary as a helper that holds the lock,
// SIGKILLs it, and expects the lock to be free afterwards (crash simulation).
func TestLockDirReleasedOnKill(t *testing.T) {
	if d := os.Getenv(lockHelperEnv); d != "" {
		if _, _, err := LockDir(d); err != nil {
			fmt.Println("error:", err)
			os.Exit(3)
		}
		fmt.Println("locked")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockDirReleasedOnKill$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper did not lock: line=%q err=%v", line, err)
	}
	_, holder, err := LockDir(dir)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("LockDir while helper holds it: err = %v, want ErrLocked", err)
	}
	if want := fmt.Sprintf("pid=%d", cmd.Process.Pid); holder != want {
		t.Errorf("holder = %q, want %q", holder, want)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	mustLock(t, dir)
}

func TestLockDirStaleFile(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":  "\x00\xffnot a pid\n\x1b[31m",
		"dead pid": "pid=999999\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, LockFileName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			release, holder, err := LockDir(dir)
			if err != nil {
				t.Fatalf("LockDir on stale file: %v", err)
			}
			defer release()
			if want := fmt.Sprintf("pid=%d", os.Getpid()); holder != want {
				t.Errorf("holder on success = %q, want %q", holder, want)
			}
			b, _ := os.ReadFile(filepath.Join(dir, LockFileName))
			if string(b) != fmt.Sprintf("pid=%d\n", os.Getpid()) {
				t.Errorf(".lock content = %q, want our pid record", b)
			}
		})
	}
}

func TestLockDirStaleHolderSanitised(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFileName)
	release := mustLock(t, dir)
	defer release()
	// Another writer scribbles over the record; the refusal must not echo it.
	if err := os.WriteFile(path, []byte("\x1b[31mevil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, holder, err := LockDir(dir)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if strings.Contains(holder, "evil") || strings.Contains(holder, "\x1b") {
		t.Errorf("holder = %q echoes untrusted file content", holder)
	}
}

func TestLockDirUnsupported(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOLCK, syscall.EINVAL, syscall.EOPNOTSUPP} {
		t.Run(errno.Error(), func(t *testing.T) {
			orig := flockFn
			flockFn = func(int, int) error { return errno }
			t.Cleanup(func() { flockFn = orig })
			dir := t.TempDir()
			release, _, err := LockDir(dir)
			if err == nil {
				release()
				t.Fatal("LockDir succeeded on an unsupported filesystem")
			}
			if errors.Is(err, ErrLocked) {
				t.Fatalf("err = %v, must not be ErrLocked", err)
			}
			if !errors.Is(err, errno) || !strings.Contains(err.Error(), "local filesystem") {
				t.Errorf("err = %v, want it to wrap %v and mention a local filesystem", err, errno)
			}
		})
	}
}

func TestLockDirChownToDirOwner(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs root on linux")
	}
	dir := t.TempDir()
	if err := os.Chown(dir, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	mustLock(t, dir)
	fi, err := os.Stat(filepath.Join(dir, LockFileName))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 65534 || st.Gid != 65534 {
		t.Errorf(".lock owner = %d:%d, want 65534:65534", st.Uid, st.Gid)
	}
}

// TestLockRefusesSymlink: a planted .lock that is a symlink or not a regular file is
// refused (not ErrLocked), and a symlink target is never opened, created or truncated
// (appsec review, CWE-59).
func TestLockRefusesSymlink(t *testing.T) {
	const victimContent = "root:secret-victim-bytes\n"
	cases := map[string]func(t *testing.T, lock string) (victim string){
		"symlink to victim": func(t *testing.T, lock string) string {
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte(victimContent), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, lock); err != nil {
				t.Fatal(err)
			}
			return victim
		},
		"dangling symlink": func(t *testing.T, lock string) string {
			victim := filepath.Join(t.TempDir(), "absent")
			if err := os.Symlink(victim, lock); err != nil {
				t.Fatal(err)
			}
			return victim
		},
		"directory": func(t *testing.T, lock string) string {
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Fatal(err)
			}
			return ""
		},
		"fifo": func(t *testing.T, lock string) string {
			if err := syscall.Mkfifo(lock, 0o600); err != nil {
				t.Fatal(err)
			}
			return ""
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			victim := plant(t, filepath.Join(dir, LockFileName))
			var before []byte
			if victim != "" {
				before, _ = os.ReadFile(victim)
			}
			release, _, err := LockDir(dir)
			if err == nil {
				release()
				t.Fatal("LockDir accepted a non-regular .lock")
			}
			if errors.Is(err, ErrLocked) {
				t.Fatalf("err = %v, want a non-ErrLocked refusal", err)
			}
			if strings.Contains(err.Error(), "secret-victim") {
				t.Errorf("err %q echoes file content", err)
			}
			if victim == "" {
				return
			}
			after, aerr := os.ReadFile(victim)
			if before == nil {
				if aerr == nil {
					t.Fatalf("symlink target %s was created", victim)
				}
				return
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("victim changed: %q -> %q", before, after)
			}
		})
	}
}
