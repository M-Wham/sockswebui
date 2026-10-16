package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// writeUIBindConfig stores a valid config.json whose settings.ui_bind_addr is addr and
// returns its exact bytes.
func writeUIBindConfig(t *testing.T, dir, addr string) []byte {
	t.Helper()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Settings.UIBindAddr = addr
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// bootUI runs the entrypoint with args until it listens, then stops it. It returns the
// bound address and the captured log.
func bootUI(t *testing.T, args []string) (addr, logs string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	onListen = func(a string) { ready <- a }
	defer func() { onListen = nil }()
	var logBuf syncBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, io.Discard, &logBuf, testFS()) }()
	select {
	case addr = <-ready:
	case code := <-done:
		cancel()
		t.Fatalf("run exited early with code %d; log:\n%s", code, logBuf.String())
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	return addr, logBuf.String()
}

// logLine returns the first log line containing every one of parts, or "".
func logLine(logs string, parts ...string) string {
	for _, line := range strings.Split(logs, "\n") {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return line
		}
	}
	return ""
}

// The stored setting decides the bind unless --ui-addr is
// passed explicitly.
func TestUIBindFromSettings(t *testing.T) {
	t.Run("resolve rows", func(t *testing.T) {
		stored := config.Default()
		stored.Settings.UIBindAddr = "127.0.0.1:0"
		for _, tc := range []struct {
			name      string
			args      []string
			cfg       config.Config
			addr, src string
		}{
			{"setting", nil, stored, "127.0.0.1:0", "setting"},
			{"flag", []string{"--ui-addr", "127.0.0.2:0"}, stored, "127.0.0.2:0", "flag"},
			{"flag equal to setting", []string{"--ui-addr", "127.0.0.1:0"}, stored, "127.0.0.1:0", "flag"},
		} {
			o, err := parseFlags(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("%s: parseFlags: %v", tc.name, err)
			}
			if addr, src := resolveUIAddr(o, tc.cfg); addr != tc.addr || src != tc.src {
				t.Errorf("%s: resolveUIAddr = %q, %q; want %q, %q", tc.name, addr, src, tc.addr, tc.src)
			}
		}
	})

	t.Run("fresh dir resolves to the default", func(t *testing.T) {
		dir := t.TempDir()
		store, err := config.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		o, err := parseFlags([]string{"--data-dir", dir}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if addr, src := resolveUIAddr(o, store.Get()); addr != "0.0.0.0:8787" || src != "setting" {
			t.Fatalf("fresh dir resolves to %q, %q; want 0.0.0.0:8787, setting", addr, src)
		}
	})

	t.Run("stored setting, no flag", func(t *testing.T) {
		dir := t.TempDir()
		// Validation rejects port 0 for ui_bind_addr, so a just-free port stands in.
		want := "127.0.0.1:" + strconv.Itoa(freePort(t))
		before := writeUIBindConfig(t, dir, want)
		addr, logs := bootUI(t, []string{"--data-dir", dir})
		if addr != want {
			t.Fatalf("listened on %s, want stored %s", addr, want)
		}
		if logLine(logs, "level=INFO", "ui listening", "addr="+want, "source=setting") == "" {
			t.Errorf("no INFO ui listening line with source=setting:\n%s", logs)
		}
		if strings.Contains(logs, "level=WARN") && strings.Contains(logs, "overrides") {
			t.Errorf("override WARN without a flag:\n%s", logs)
		}
		after, err := os.ReadFile(filepath.Join(dir, config.ConfigFileName))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("config.json changed by boot (err=%v)", err)
		}
	})

	t.Run("explicit flag overrides setting", func(t *testing.T) {
		dir := t.TempDir()
		stored := "127.0.0.1:" + strconv.Itoa(freePort(t))
		before := writeUIBindConfig(t, dir, stored)
		addr, logs := bootUI(t, []string{"--data-dir", dir, "--ui-addr", "127.0.0.2:0"})
		host, _, err := net.SplitHostPort(addr)
		if err != nil || host != "127.0.0.2" {
			t.Fatalf("listened on %s, want 127.0.0.2", addr)
		}
		if logLine(logs, "level=INFO", "ui listening", "source=flag") == "" {
			t.Errorf("no INFO ui listening line with source=flag:\n%s", logs)
		}
		warn := logLine(logs, "level=WARN", "--ui-addr", stored, "127.0.0.2:0")
		if warn == "" {
			t.Errorf("no WARN naming flag and stored value:\n%s", logs)
		}
		if n := strings.Count(logs, warn); warn != "" && n != 1 {
			t.Errorf("override WARN logged %d times, want 1", n)
		}
		after, _ := os.ReadFile(filepath.Join(dir, config.ConfigFileName))
		if !bytes.Equal(before, after) {
			t.Fatal("config.json changed by boot")
		}
	})

	t.Run("stored address fails to bind", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		dir := t.TempDir()
		writeUIBindConfig(t, dir, busy.Addr().String())
		listened := make(chan string, 1)
		onListen = func(a string) { listened <- a }
		defer func() { onListen = nil }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var stderr syncBuffer
		if code := run(ctx, []string{"--data-dir", dir}, io.Discard, &stderr, testFS()); code != 1 {
			t.Fatalf("exit code = %d, want 1; log:\n%s", code, stderr.String())
		}
		line := logLine(stderr.String(), "level=ERROR", "ui server", "source=setting", busy.Addr().String())
		if line == "" || !strings.Contains(line, "--ui-addr") {
			t.Fatalf("listen error does not name the source and the --ui-addr escape hatch:\n%s", stderr.String())
		}
	})
}

// TestRunReportsUIBindInSettings: run passes the resolved UI bind to the API
// (api.WithUIBind), so GET /api/settings reports it read-only.
func TestRunReportsUIBindInSettings(t *testing.T) {
	base := startRun(t, t.TempDir(), testFS()) // passes --ui-addr 127.0.0.1:0
	c := setupAndLogin(t, base)
	res, body := doReq(t, "GET", base+"/api/settings", "", c)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings: status %d body %q", res.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m["ui_bind_effective"] != "127.0.0.1:0" || m["ui_bind_source"] != uiBindSourceFlag {
		t.Errorf("effective=%v source=%v, want 127.0.0.1:0 / flag", m["ui_bind_effective"], m["ui_bind_source"])
	}
}
