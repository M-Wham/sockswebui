package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/M-Wham/sockswebui/internal/api"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// startGuarded serves newHandler with the given accepted-name list.
func startGuarded(t *testing.T, names api.NameList) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := openStore(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	mgr := supervisor.New(supervisor.Options{Config: store.Get(), Logger: log})
	t.Cleanup(mgr.Close)
	uiFS := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>sockswebui</title>")}}
	srv := httptest.NewServer(newHandler(store, log, mgr, uiFS, names, api.WithSSEKeepalive(20*time.Millisecond)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func doHost(t *testing.T, method, url, host, body string, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	client := &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

const testPW = `{"password":"correct horse battery"}`

func TestHostAllowListFullStack(t *testing.T) {
	base := startGuarded(t, api.NewNameList("pibox", "ui.example"))

	refuse := func(method, path, host, body string, c ...*http.Cookie) {
		t.Helper()
		res, b := doHost(t, method, base+path, host, body, c...)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(b, `"host not allowed"`) {
			t.Errorf("%s %s Host %q: %d %q, want 403 host not allowed", method, path, host, res.StatusCode, b)
		}
		assertSecurityHeaders(t, method+" "+path+" 403", res.Header)
	}

	// Evil setup with a valid body must not claim the instance.
	refuse("POST", "/api/setup", "evil.example", testPW)
	refuse("POST", "/api/setup", "evil.example:8080", testPW)
	// Evil logins must not touch the limiter (5/min): ten of them, then a real login works.
	for i := 0; i < 10; i++ {
		refuse("POST", "/api/login", "evil.example", testPW)
	}
	refuse("GET", "/", "evil.example", "")
	refuse("GET", "/api/events", "evil.example", "")

	// Setup is still open: the evil setup never ran.
	if res, b := doHost(t, "POST", base+"/api/setup", "127.0.0.1:8080", testPW); res.StatusCode/100 != 2 {
		t.Fatalf("setup after evil setup: %d %q", res.StatusCode, b)
	}
	res, b := doHost(t, "POST", base+"/api/login", "[::1]:8080", testPW)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login after 10 evil logins: %d %q (limiter counted refused hosts?)", res.StatusCode, b)
	}
	var c *http.Cookie
	for _, k := range res.Cookies() {
		if k.Value != "" {
			c = k
		}
	}
	if c == nil {
		t.Fatal("no session cookie")
	}
	// With a valid session, a foreign Host is still refused before the SSE subscription.
	refuse("GET", "/api/events", "evil.example", "", c)
	refuse("GET", "/api/settings", "ui.example.evil.test", "", c)

	for _, host := range []string{"pibox.local:8080", "127.0.0.1:8080", "[::1]:8080", "localhost", "ui.example", "PIBOX"} {
		res, b := doHost(t, "GET", base+"/api/settings", host, "", c)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET /api/settings Host %q: %d %q", host, res.StatusCode, b)
		}
		res, _ = doHost(t, "GET", base+"/", host, "")
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET / Host %q: %d", host, res.StatusCode)
		}
	}
}

func TestHostAllowListEmptyHostRaw(t *testing.T) {
	// net/http's client always sends a Host; drive the handler directly for an empty one (HTTP/1.0).
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := openStore(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	mgr := supervisor.New(supervisor.Options{Config: store.Get(), Logger: log})
	t.Cleanup(mgr.Close)
	h := newHandler(store, log, mgr, fstest.MapFS{}, api.NewNameList("pibox"))
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = ""
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("empty Host: %d, want 403", rec.Code)
	}
	assertSecurityHeaders(t, "empty Host 403", rec.Header())
}

func TestHostAllowListBootNames(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	calls := 0
	names := bootNames(func() (string, error) { calls++; return "PiBox", nil }, []string{"ui.example"}, log)
	if calls != 1 {
		t.Errorf("hostname read %d times, want 1", calls)
	}
	for _, h := range []string{"pibox", "pibox.local:8080", "ui.example", "localhost", "127.0.0.1"} {
		if !names.Allows(h) {
			t.Errorf("Allows(%q) = false", h)
		}
	}
	if names.Allows("evil.example") {
		t.Error("evil.example allowed")
	}

	// os.Hostname failure logs WARN and never allows all.
	buf.Reset()
	names = bootNames(func() (string, error) { return "", errors.New("boom") }, nil, log)
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "hostname") {
		t.Errorf("no WARN on hostname failure: %q", buf.String())
	}
	for _, h := range []string{"evil.example", ".local", "", "anything"} {
		if names.Allows(h) {
			t.Errorf("after hostname failure Allows(%q) = true", h)
		}
	}
	if !names.Allows("localhost") || !names.Allows("10.0.0.5:8080") {
		t.Error("IP literals and localhost must still pass")
	}
}
