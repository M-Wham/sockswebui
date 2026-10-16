package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// Canary secrets written into the test config; none may appear in any output line.
const (
	canaryHash     = "$argon2id$v=19$m=65536,t=3,p=4$Y2FuYXJ5LXNhbHQ$Y2FuYXJ5LWhhc2gtcmVzZXQtYWRtaW4"
	canarySecret   = "canary-session-secret-reset-admin"
	canaryProxyPW  = "canary-proxy-password-reset"
	canarySocksPW  = "canary-socks-password-reset"
	resetUIBind    = "127.0.0.1:8080"
	resetMsgPrefix = "Admin password cleared. Start the service and complete setup at http://"
)

// fakeKey returns a freshly generated base64 32-byte key (never a real key).
func fakeKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// resetConfig is a valid config holding every kind of secret, with canary values.
func resetConfig(t *testing.T) (config.Config, []string) {
	t.Helper()
	priv, psk := fakeKey(t), fakeKey(t)
	cfg := config.Default()
	cfg.Settings = config.Settings{UIBindAddr: resetUIBind, AdminPasswordHash: canaryHash, SessionSecret: canarySecret}
	cfg.Endpoints = []config.Endpoint{
		{ID: "ep-0000000000000001", Name: "wg", Kind: config.KindWireGuard, PrivateKey: priv, PresharedKey: psk,
			PeerPublicKey: fakeKey(t), Endpoint: "192.0.2.10:51820", Addresses: []string{"10.64.0.2/32"}},
		{ID: "ep-0000000000000002", Name: "px", Kind: config.KindSOCKS5, Host: "192.0.2.20", Port: 1080,
			Username: "u", Password: canaryProxyPW},
	}
	cfg.Slots = []config.Slot{{
		// Disabled: an autostarted slot would try to dial its (fake) WireGuard peer.
		ID: 1, Name: "s1", Enabled: false, BindAddr: "127.0.0.1", Port: 1081,
		SocksAuth: &config.SocksAuth{Username: "su", Password: canarySocksPW},
		Route:     []string{"ep-0000000000000001", "ep-0000000000000002"}, Killswitch: config.KillswitchBlock,
		Health: config.DefaultHealth(),
	}}
	return cfg, []string{canaryHash, canarySecret, canaryProxyPW, canarySocksPW, priv, psk}
}

// writeResetConfig saves resetConfig into dir through the store and returns its secrets.
func writeResetConfig(t *testing.T, dir string) []string {
	t.Helper()
	cfg, secrets := resetConfig(t)
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return secrets
}

// runReset runs `--reset-admin` on dir and fails the test if it listens or starts slots.
func runReset(t *testing.T, dir string, extra ...string) (code int, stdout, stderr string) {
	t.Helper()
	listened := make(chan string, 1)
	onListen = func(addr string) { listened <- addr }
	started := make(chan struct{}, 1)
	onAutostart = func(*supervisor.Manager) { started <- struct{}{} }
	t.Cleanup(func() { onListen, onAutostart = nil, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	args := append([]string{"--reset-admin", "--data-dir", dir}, extra...)
	code = run(ctx, args, &out, &errb, testFS())
	select {
	case addr := <-listened:
		t.Errorf("--reset-admin listened on %s", addr)
	case <-started:
		t.Error("--reset-admin started slots")
	default:
	}
	return code, out.String(), errb.String()
}

// topLevel splits a config file into its top-level raw JSON values.
func topLevel(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return m
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestResetAdmin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.ConfigFileName)
	writeResetConfig(t, dir)
	before := readFile(t, path)

	code, stdout, stderr := runReset(t, dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	after := readFile(t, path)
	b, a := topLevel(t, before), topLevel(t, after)
	for _, k := range []string{"endpoints", "slots"} {
		if !bytes.Equal(b[k], a[k]) {
			t.Errorf("%s changed:\nbefore %s\nafter  %s", k, b[k], a[k])
		}
	}
	var s config.Settings
	if err := json.Unmarshal(a["settings"], &s); err != nil {
		t.Fatal(err)
	}
	if s != (config.Settings{UIBindAddr: resetUIBind}) {
		t.Errorf("settings after reset = %+v, want only ui_bind_addr %q", s, resetUIBind)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	want := "Admin password cleared. Start the service and complete setup at http://" + resetUIBind +
		"/ immediately; anyone who reaches the UI first can claim it.\n"
	if !strings.HasPrefix(stdout, want) {
		t.Errorf("stdout = %q, want it to start with %q", stdout, want)
	}
	if n := strings.Count(stderr, `msg="admin reset"`); n != 1 {
		t.Errorf("stderr has %d admin reset lines, want 1: %q", n, stderr)
	}
	if want := "uid=" + itoa(os.Geteuid()); !strings.Contains(auditLine(stderr), want) {
		t.Errorf("audit line %q lacks %q", auditLine(stderr), want)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".config.json.tmp-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}

	// The next boot opens first-run setup.
	base := startRun(t, dir, testFS())
	if resp, body := doReq(t, "POST", base+"/api/setup", `{"password":"correct horse battery"}`); resp.StatusCode != 204 {
		t.Errorf("setup after reset = %d %s, want 204", resp.StatusCode, body)
	}
}

func auditLine(stderr string) string {
	for l := range strings.Lines(stderr) {
		if strings.Contains(l, `msg="admin reset"`) {
			return l
		}
	}
	return ""
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestResetAdminRefusesWhileLocked(t *testing.T) {
	dir := t.TempDir()
	writeResetConfig(t, dir)
	startRun(t, dir, testFS())
	before := readFile(t, filepath.Join(dir, config.ConfigFileName))
	code, _, stderr := runReset(t, dir)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if want := "pid=" + itoa(os.Getpid()); !strings.Contains(stderr, "locked by another sockswebui process") ||
		!strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to name the holder (%s)", stderr, want)
	}
	if after := readFile(t, filepath.Join(dir, config.ConfigFileName)); !bytes.Equal(before, after) {
		t.Error("config.json changed by a refused reset")
	}
	if strings.Contains(stderr, `msg="admin reset"`) {
		t.Errorf("refused reset logged the audit line: %q", stderr)
	}
}

func TestResetAdminNoConfig(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runReset(t, dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*config.json*")); len(m) != 0 {
		t.Errorf("files created: %v", m)
	}
	if !strings.Contains(stdout, "nothing to reset") {
		t.Errorf("stdout = %q, want it to say there is nothing to reset", stdout)
	}
	if strings.Contains(stdout, "Admin password cleared") || strings.Contains(stderr, `msg="admin reset"`) {
		t.Errorf("no-config run claimed a reset: stdout=%q stderr=%q", stdout, stderr)
	}
}

// assertUntouched runs a reset that must fail and checks dir is unchanged (no quarantine).
func assertUntouched(t *testing.T, dir, wantErr string) {
	t.Helper()
	before := dirSnapshot(t, dir)
	code, stdout, stderr := runReset(t, dir)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if after := dirSnapshot(t, dir); after != before {
		t.Errorf("data dir changed:\nbefore %q\nafter  %q", before, after)
	}
	if !strings.Contains(stderr, wantErr) {
		t.Errorf("stderr = %q, want %q", stderr, wantErr)
	}
	if strings.Contains(stdout, "Admin password cleared") || strings.Contains(stderr, `msg="admin reset"`) {
		t.Errorf("failed reset claimed success: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestResetAdminCorrupt(t *testing.T) {
	for name, body := range map[string]string{
		"syntax":  `{"endpoints": [`,
		"invalid": `{"endpoints":[],"slots":[],"settings":{"ui_bind_addr":"nope","admin_password_hash":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			lockOnce(t, dir)
			assertUntouched(t, dir, "config.json")
		})
	}
}

func TestResetAdminUnreadable(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, config.ConfigFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		lockOnce(t, dir)
		assertUntouched(t, dir, "config.json")
	})
	t.Run("permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file")
		}
		dir := t.TempDir()
		writeResetConfig(t, dir)
		path := filepath.Join(dir, config.ConfigFileName)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		lockOnce(t, dir)
		code, _, stderr := runReset(t, dir)
		if code != 1 || !strings.Contains(stderr, "permission denied") {
			t.Fatalf("exit code = %d, stderr=%q; want 1 and permission denied", code, stderr)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0 {
			t.Errorf("mode changed to %v", fi.Mode().Perm())
		}
	})
}

// lockOnce creates dir/.lock ahead of a snapshot comparison.
func lockOnce(t *testing.T, dir string) {
	t.Helper()
	release, _, err := config.LockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = release()
}

func TestResetAdminNoSecretLeakCanary(t *testing.T) {
	dir := t.TempDir()
	secrets := writeResetConfig(t, dir)
	raw := readFile(t, filepath.Join(dir, config.ConfigFileName))
	// Leftovers holding every secret must be named, never echoed.
	for _, n := range []string{"config.json.bad-20260101T000000Z", "config.json.pre-import-20260101T000000Z"} {
		if err := os.WriteFile(filepath.Join(dir, n), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := runReset(t, dir)
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%q", code, stderr)
	}
	// Also cover the failure path's error lines.
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), append(raw[:len(raw)/2:len(raw)/2], '!'), 0o600); err != nil {
		t.Fatal(err)
	}
	code2, stdout2, stderr2 := runReset(t, dir)
	if code2 != 1 {
		t.Fatalf("corrupt exit code = %d, want 1", code2)
	}
	all := stdout + stderr + stdout2 + stderr2
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("secret %q appears in output:\n%s", s, all)
		}
	}
}
