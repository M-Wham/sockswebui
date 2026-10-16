package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

func TestParseFlagsDefaults(t *testing.T) {
	o, err := parseFlags(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if o.ShowVersion {
		t.Error("ShowVersion default should be false")
	}
	if o.DataDir != defaultDataDir {
		t.Errorf("DataDir = %q, want %q", o.DataDir, defaultDataDir)
	}
	// --ui-addr has no default; the stored setting decides unless it is passed.
	if o.UIAddr != "" || o.UIAddrExplicit {
		t.Errorf("UIAddr = %q explicit=%v, want unset", o.UIAddr, o.UIAddrExplicit)
	}
	// Data dir /var/lib/sockswebui, ui_bind_addr default 0.0.0.0:8787.
	if defaultDataDir != "/var/lib/sockswebui" || config.Default().Settings.UIBindAddr != "0.0.0.0:8787" {
		t.Errorf("defaults diverge from the documented defaults: %q %q", defaultDataDir, config.Default().Settings.UIBindAddr)
	}
}

func TestParseFlagsOverrides(t *testing.T) {
	o, err := parseFlags([]string{"--version", "--data-dir", "/tmp/x", "--ui-addr", "127.0.0.1:9999"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !o.ShowVersion || o.DataDir != "/tmp/x" || o.UIAddr != "127.0.0.1:9999" || !o.UIAddrExplicit {
		t.Errorf("unexpected options: %+v", o)
	}
}

func TestParseFlagsRejectsUnknownAndEmpty(t *testing.T) {
	if _, err := parseFlags([]string{"--bogus"}, io.Discard); err == nil {
		t.Error("expected error for unknown flag")
	}
	if _, err := parseFlags([]string{"--data-dir", ""}, io.Discard); err == nil {
		t.Error("expected error for empty --data-dir")
	}
	if _, err := parseFlags([]string{"--ui-addr", ""}, io.Discard); err == nil {
		t.Error("expected error for empty --ui-addr")
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--version"}, &out, &errOut, fstest.MapFS{})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	got := strings.TrimSpace(out.String())
	if got == "" || !strings.Contains(got, version) {
		t.Errorf("version output = %q, want it to contain %q", got, version)
	}
	if version != "dev" {
		t.Errorf("default version = %q, want dev", version)
	}
}

// TestVersionFlag: the Makefile's default VERSION is the HEAD short SHA (no tag,
// no -dirty), and a binary built with that ldflag prints exactly
// "sockswebui <sha>" for --version.
func TestVersionFlag(t *testing.T) {
	shaOut, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))
	mkCmd := exec.Command("make", "-C", "../..", "-n", "-s", "build")
	for _, kv := range os.Environ() { // an exported VERSION would mask the default
		if !strings.HasPrefix(kv, "VERSION=") {
			mkCmd.Env = append(mkCmd.Env, kv)
		}
	}
	mk, err := mkCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n build: %v\n%s", err, mk)
	}
	if want := "-X main.version=" + sha + `"`; !strings.Contains(string(mk), want) {
		t.Fatalf("make default ldflags lack %q:\n%s", want, mk)
	}
	bin := filepath.Join(t.TempDir(), "sockswebui")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+sha, "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got, want := string(out), "sockswebui "+sha+"\n"; got != want {
		t.Errorf("--version = %q, want %q", got, want)
	}
}

func TestRunBadFlagExitCode(t *testing.T) {
	code := run(context.Background(), []string{"--nope"}, io.Discard, io.Discard, fstest.MapFS{})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func testFS() fstest.MapFS {
	return fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>sockswebui</title>")}}
}

func TestUIHandlerServesIndex(t *testing.T) {
	srv := httptest.NewServer(newUIHandler(testFS()))
	defer srv.Close()
	res, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if !strings.Contains(string(body), "sockswebui") {
		t.Errorf("body = %q", body)
	}
}

func TestEnsureDataDirCreates0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := ensureDataDir(dir); err != nil {
		t.Fatalf("ensureDataDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want dir 0700", fi.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("data dir should be empty, got %d entries", len(entries))
	}
	// Idempotent on an existing dir.
	if err := ensureDataDir(dir); err != nil {
		t.Fatalf("second ensureDataDir: %v", err)
	}
}

func TestRunServesAndShutsDownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dataDir := filepath.Join(t.TempDir(), "data")
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	t.Cleanup(func() { onListen = nil })

	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--data-dir", dataDir, "--ui-addr", "127.0.0.1:0"}, io.Discard, io.Discard, testFS())
	}()

	var addr string
	select {
	case addr = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	res, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "sockswebui") {
		t.Errorf("status=%d body=%q", res.StatusCode, body)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

// startRun runs the binary entrypoint against dataDir and returns its base URL; the
// server is stopped (and its exit code checked) at test cleanup.
func startRun(t *testing.T, dataDir string, uiFS fs.FS) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--data-dir", dataDir, "--ui-addr", "127.0.0.1:0"}, io.Discard, io.Discard, uiFS)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
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

func doReq(t *testing.T, method, url, body string, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// setupAndLogin sets the admin password and returns the session cookie.
func setupAndLogin(t *testing.T, base string) *http.Cookie {
	t.Helper()
	const pw = `{"password":"correct horse battery"}`
	if res, body := doReq(t, "POST", base+"/api/setup", pw); res.StatusCode/100 != 2 {
		t.Fatalf("setup: status %d body %q", res.StatusCode, body)
	}
	res, body := doReq(t, "POST", base+"/api/login", pw)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d body %q", res.StatusCode, body)
	}
	for _, c := range res.Cookies() {
		if c.Value != "" {
			return c
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

func TestRunRoutesAPI(t *testing.T) {
	base := startRun(t, t.TempDir(), testFS())
	if res, body := doReq(t, "GET", base+"/api/settings", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/settings without cookie: status %d body %q", res.StatusCode, body)
	}
	if res, body := doReq(t, "GET", base+"/api", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api without cookie: status %d body %q", res.StatusCode, body)
	}
	c := setupAndLogin(t, base)
	res, body := doReq(t, "GET", base+"/api/settings", "", c)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "ui_bind_addr") {
		t.Fatalf("GET /api/settings: status %d body %q", res.StatusCode, body)
	}
	if res, body := doReq(t, "GET", base+"/", ""); res.StatusCode != 200 || !strings.Contains(body, "sockswebui") {
		t.Errorf("GET /: status %d body %q", res.StatusCode, body)
	}
}

func TestUIHandlerNoDirectoryListing(t *testing.T) {
	uiFS := fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>sockswebui</title>")},
		"assets/app.js":       {Data: []byte("console.log(1)")},
		"docs/index.html":     {Data: []byte("docs-index")},
		"assets/sub/deep.css": {Data: []byte("body{}")},
	}
	srv := httptest.NewServer(newUIHandler(uiFS))
	defer srv.Close()
	for _, p := range []string{"/assets/", "/assets", "/assets/sub/", "/nope/"} {
		res, body := doReq(t, "GET", srv.URL+p, "")
		if res.StatusCode != http.StatusNotFound && !strings.Contains(body, "sockswebui") {
			t.Errorf("GET %s: status %d body %q (want 404 or index.html)", p, res.StatusCode, body)
		}
		if strings.Contains(body, "app.js") || strings.Contains(body, "sub/") || strings.Contains(body, "<pre>") {
			t.Errorf("GET %s leaked a directory listing: %q", p, body)
		}
	}
	if res, body := doReq(t, "GET", srv.URL+"/assets/app.js", ""); res.StatusCode != 200 || body != "console.log(1)" {
		t.Errorf("GET /assets/app.js: status %d body %q", res.StatusCode, body)
	}
	if res, body := doReq(t, "GET", srv.URL+"/docs/", ""); res.StatusCode != 200 || body != "docs-index" {
		t.Errorf("GET /docs/: status %d body %q", res.StatusCode, body)
	}
}

func TestCorruptBoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := startRun(t, dir, testFS())
	if res, body := doReq(t, "GET", base+"/", ""); res.StatusCode != 200 || !strings.Contains(body, "sockswebui") {
		t.Errorf("GET /: status %d body %q", res.StatusCode, body)
	}
	bad, _ := filepath.Glob(filepath.Join(dir, "config.json.bad-*"))
	if len(bad) != 1 {
		t.Fatalf("quarantined files = %v, want exactly one", bad)
	}
	c := setupAndLogin(t, base)
	res, body := doReq(t, "GET", base+"/api/settings", "", c)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings: status %d body %q", res.StatusCode, body)
	}
	var s struct {
		ConfigError string `json:"config_error"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil || s.ConfigError == "" {
		t.Errorf("config_error empty (err=%v) in %q", err, body)
	}
}

func TestRunUnreadableConfigExits(t *testing.T) {
	dir := t.TempDir()
	// config.json as a directory makes the read fail with EISDIR, even as root.
	if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	listened := make(chan string, 1)
	onListen = func(addr string) { listened <- addr }
	t.Cleanup(func() { onListen = nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	code := run(ctx, []string{"--data-dir", dir, "--ui-addr", "127.0.0.1:0"}, io.Discard, &stderr, testFS())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr.String())
	}
	select {
	case addr := <-listened:
		t.Fatalf("server listened on %s although config was unreadable", addr)
	default:
	}
	out := stderr.String()
	if !strings.Contains(out, "config.json") || !strings.Contains(out, "is a directory") {
		t.Errorf("stderr = %q, want it to name config.json and the read error", out)
	}
	if bad, _ := filepath.Glob(filepath.Join(dir, "config.json.bad-*")); len(bad) != 0 {
		t.Errorf("unreadable config was quarantined: %v", bad)
	}
}

func TestParseFlagsUIHost(t *testing.T) {
	o, err := parseFlags(nil, io.Discard)
	if err != nil || len(o.UIHosts) != 0 {
		t.Fatalf("default: %v %v", o.UIHosts, err)
	}
	o, err = parseFlags([]string{"--ui-host", "ui.example", "--ui-host=Router.Example."}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if len(o.UIHosts) != 2 || o.UIHosts[0] != "ui.example" || o.UIHosts[1] != "Router.Example." {
		t.Errorf("UIHosts = %q", o.UIHosts)
	}
	for _, bad := range []string{"", "https://ui.example", "ui.example/admin", "ui.example:8080", "[::1]:80", "ui example"} {
		if _, err := parseFlags([]string{"--ui-host", bad}, io.Discard); err == nil {
			t.Errorf("--ui-host %q: expected error", bad)
		}
		code := run(context.Background(), []string{"--ui-host", bad}, io.Discard, io.Discard, fstest.MapFS{})
		if code != 2 {
			t.Errorf("--ui-host %q: exit %d, want 2", bad, code)
		}
	}
}

func TestUIHandlerFontContentType(t *testing.T) {
	if got := uiContentTypes[".woff2"]; got != "font/woff2" {
		t.Fatalf("uiContentTypes[.woff2] = %q", got)
	}
	uiFS := fstest.MapFS{"fonts/x.woff2": {Data: []byte("wOF2")}}
	srv := httptest.NewServer(newUIHandler(uiFS))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/fonts/x.woff2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "font/woff2" {
		t.Fatalf("Content-Type = %q", ct)
	}
}
