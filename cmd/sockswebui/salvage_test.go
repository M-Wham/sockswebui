package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startRunLog is startRun with stderr captured into log.
func startRunLog(t *testing.T, dataDir string, log io.Writer) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--data-dir", dataDir, "--ui-addr", "127.0.0.1:0"}, io.Discard, log, testFS())
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("run did not return after cancel")
		}
		onListen = nil
	})
	select {
	case addr := <-ready:
		return "http://" + addr
	case code := <-done:
		t.Fatalf("run exited early with code %d", code)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return ""
}

// corruptSlots rewrites config.json so its "slots" value has the wrong type while
// "settings" stays intact.
func corruptSlots(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["slots"] = json.RawMessage(`"oops"`)
	out, _ := json.Marshal(m)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// flipEndpointByte gives config.json a non-empty "endpoints" array ahead of an intact
// "settings" object, then changes one ',' inside that array to ';'.
func flipEndpointByte(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["endpoints"] = json.RawMessage(`[{"id":"ep-1","name":"se-got-wg-001","kind":"wireguard"}]`)
	out, _ := json.Marshal(m) // keys sorted: endpoints, settings, slots
	start := bytes.Index(out, []byte(`"endpoints":[`))
	i := bytes.IndexByte(out[start:], ',')
	if start < 0 || i < 0 || start+i > bytes.Index(out, []byte(`"settings"`)) {
		t.Fatalf("unexpected layout: %s", out)
	}
	out[start+i] = ';'
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupLockedAfterSalvage(t *testing.T) {
	for name, corrupt := range map[string]func(*testing.T, string){
		"slots wrong type":          corruptSlots,
		"syntax error in endpoints": flipEndpointByte,
	} {
		t.Run(name, func(t *testing.T) { setupLockedAfter(t, corrupt) })
	}
}

func setupLockedAfter(t *testing.T, corrupt func(*testing.T, string)) {
	dir := t.TempDir()
	t.Run("first boot", func(t *testing.T) {
		setupAndLogin(t, startRun(t, dir, testFS()))
	})
	corrupt(t, dir)

	base := startRun(t, dir, testFS())
	if bad, _ := filepath.Glob(filepath.Join(dir, "config.json.bad-*")); len(bad) != 1 {
		t.Fatalf("quarantined files = %v", bad)
	}
	if res, body := doReq(t, "POST", base+"/api/setup", `{"password":"attacker password!"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("setup after salvage: status %d body %q, want 409", res.StatusCode, body)
	}
	if res, body := doReq(t, "POST", base+"/api/login", `{"password":"correct horse battery"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("login with original password: status %d body %q", res.StatusCode, body)
	}
}

func TestCorruptBootUnsalvageableWarns(t *testing.T) {
	dir := t.TempDir()
	t.Run("first boot", func(t *testing.T) {
		setupAndLogin(t, startRun(t, dir, testFS()))
	})
	path := filepath.Join(dir, "config.json")
	raw, _ := os.ReadFile(path)
	var c struct {
		Settings struct {
			Hash   string `json:"admin_password_hash"`
			Secret string `json:"session_secret"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Settings.Hash == "" || c.Settings.Secret == "" {
		t.Fatalf("first boot config lacks creds (err=%v)", err)
	}
	// Truncate inside the settings object itself: the one object salvage reads is
	// damaged, so nothing is recoverable.
	cut := bytes.Index(raw, []byte(`"session_secret"`))
	if cut < 0 {
		t.Fatal("no session_secret in config")
	}
	if err := os.WriteFile(path, raw[:cut+5], 0o600); err != nil {
		t.Fatal(err)
	}

	var logBuf syncBuffer
	base := startRunLog(t, dir, &logBuf)
	logs := logBuf.String()
	var warn string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "admin credentials could not be recovered") &&
			strings.Contains(line, "setup is open") {
			warn = line
		}
	}
	if warn == "" {
		t.Fatalf("no WARN about unrecoverable credentials in log:\n%s", logs)
	}
	if strings.Contains(logs, c.Settings.Hash) || strings.Contains(logs, c.Settings.Secret) {
		t.Fatal("log leaks hash or session secret")
	}
	// Setup re-opens, as before.
	if res, body := doReq(t, "POST", base+"/api/setup", `{"password":"new admin password"}`); res.StatusCode/100 != 2 {
		t.Fatalf("setup: status %d body %q", res.StatusCode, body)
	}
}

// quarantineOnly turns dir's config.json into a lone config.json.bad-<ts>, as left by a
// power loss after quarantine. damage corrupts it first.
func quarantineOnly(t *testing.T, dir string, damage func(*testing.T, string)) string {
	t.Helper()
	damage(t, dir)
	bad := filepath.Join(dir, "config.json.bad-20261002T000000Z")
	if err := os.Rename(filepath.Join(dir, "config.json"), bad); err != nil {
		t.Fatal(err)
	}
	return filepath.Base(bad)
}

// truncateSettings cuts config.json inside the settings object so nothing is salvageable.
func truncateSettings(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	raw, _ := os.ReadFile(path)
	cut := bytes.Index(raw, []byte(`"session_secret"`))
	if cut < 0 {
		t.Fatal("no session_secret in config")
	}
	if err := os.WriteFile(path, raw[:cut+5], 0o600); err != nil {
		t.Fatal(err)
	}
}

// corruptLogLine is the ERROR line run logs for a corrupt or quarantine-only boot.
func corruptLogLine(logs string) string {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "quarantine") {
			return line
		}
	}
	return ""
}

// TestSalvageQuarantineOnlyBoot: config.json is missing and only a config.json.bad-* is
// left. The boot is never fresh: a salvageable hash keeps setup locked (409); without one
// the setup-open WARN is logged.
func TestSalvageQuarantineOnlyBoot(t *testing.T) {
	const setupOpenWarn = "admin credentials could not be recovered"
	t.Run("salvageable hash", func(t *testing.T) {
		dir := t.TempDir()
		t.Run("first boot", func(t *testing.T) { setupAndLogin(t, startRun(t, dir, testFS())) })
		src := quarantineOnly(t, dir, corruptSlots)
		var logBuf syncBuffer
		base := startRunLog(t, dir, &logBuf)
		if res, body := doReq(t, "POST", base+"/api/setup", `{"password":"attacker password!"}`); res.StatusCode != http.StatusConflict {
			t.Fatalf("setup after quarantine-only boot: status %d body %q, want 409", res.StatusCode, body)
		}
		if res, body := doReq(t, "POST", base+"/api/login", `{"password":"correct horse battery"}`); res.StatusCode != http.StatusOK {
			t.Fatalf("login with original password: status %d body %q", res.StatusCode, body)
		}
		logs := logBuf.String()
		if line := corruptLogLine(logs); !strings.Contains(line, src) {
			t.Errorf("no ERROR line naming %s:\n%s", src, logs)
		}
		if strings.Contains(logs, setupOpenWarn) {
			t.Errorf("setup-open WARN logged although credentials were salvaged:\n%s", logs)
		}
	})

	t.Run("no usable hash", func(t *testing.T) {
		dir := t.TempDir()
		t.Run("first boot", func(t *testing.T) { setupAndLogin(t, startRun(t, dir, testFS())) })
		src := quarantineOnly(t, dir, truncateSettings)
		var logBuf syncBuffer
		base := startRunLog(t, dir, &logBuf)
		logs := logBuf.String()
		line := corruptLogLine(logs)
		if !strings.Contains(line, src) {
			t.Errorf("no ERROR line naming %s:\n%s", src, logs)
		}
		if strings.Contains(line, "recovered") || strings.Contains(line, "salvag") ||
			strings.Contains(line, "config was corrupt") {
			t.Errorf("quarantine-only boot described as a recovery: %s", line)
		}
		if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, setupOpenWarn) {
			t.Fatalf("no setup-open WARN:\n%s", logs)
		}
		if res, body := doReq(t, "POST", base+"/api/setup", `{"password":"new admin password"}`); res.StatusCode/100 != 2 {
			t.Fatalf("setup: status %d body %q", res.StatusCode, body)
		}
	})
}
