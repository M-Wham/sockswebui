package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

func TestResetAdminListsHashFiles(t *testing.T) {
	dir := t.TempDir()
	writeResetConfig(t, dir)
	withHash := readFile(t, filepath.Join(dir, config.ConfigFileName))
	stripped := `{"endpoints":[],"slots":[],"settings":{"ui_bind_addr":"127.0.0.1:8080"}}`
	files := map[string]string{
		"config.json.bad-20260101T000000Z":          "{corrupt " + canaryHash,
		"config.json.pre-import-20260101T000000Z":   string(withHash),
		"config.json.pre-import-20260102T000000Z.1": stripped,
		"notes.txt": string(withHash),
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := runReset(t, dir)
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%q", code, stderr)
	}
	for _, n := range []string{"config.json.bad-20260101T000000Z", "config.json.pre-import-20260101T000000Z"} {
		if !strings.Contains(stdout, n) {
			t.Errorf("stdout lacks hash-bearing %s:\n%s", n, stdout)
		}
	}
	for _, n := range []string{"config.json.pre-import-20260102T000000Z.1", "notes.txt"} {
		if strings.Contains(stdout, n) {
			t.Errorf("stdout lists %s, which is not a hash-bearing leftover:\n%s", n, stdout)
		}
	}
	for n, body := range files {
		if got, err := os.ReadFile(filepath.Join(dir, n)); err != nil || string(got) != body {
			t.Errorf("%s changed or deleted (err=%v)", n, err)
		}
	}
}

func TestResetAdminKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	const uid, gid = 65534, 65534
	dir := t.TempDir()
	writeResetConfig(t, dir)
	path := filepath.Join(dir, config.ConfigFileName)
	for _, p := range []string{dir, path} {
		if err := os.Chown(p, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, stderr := runReset(t, dir); code != 0 {
		t.Fatalf("exit code = %d; stderr=%q", code, stderr)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != uid || st.Gid != gid || fi.Mode().Perm() != 0o600 {
		t.Errorf("config.json = %d:%d %v, want %d:%d 0600", st.Uid, st.Gid, fi.Mode().Perm(), uid, gid)
	}
}

func TestParseFlagsResetAdmin(t *testing.T) {
	o, err := parseFlags([]string{"--reset-admin", "--data-dir", "/tmp/x"}, io.Discard)
	if err != nil || !o.ResetAdmin {
		t.Fatalf("parseFlags = %+v, %v; want ResetAdmin", o, err)
	}
	if o, _ := parseFlags(nil, io.Discard); o.ResetAdmin {
		t.Error("ResetAdmin set by default")
	}
	// Combined with --version or positional args → exit 2.
	for _, args := range [][]string{{"--reset-admin", "--version"}, {"--reset-admin", "extra"}} {
		if _, err := parseFlags(args, io.Discard); err == nil {
			t.Errorf("parseFlags(%q) accepted", args)
		}
		code, _, _ := runResetArgs(t, args)
		if code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
}

func runResetArgs(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := run(t.Context(), args, &out, &errb, testFS())
	return code, out.String(), errb.String()
}
