package main

import (
	"bufio"
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

var wantSecurityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'; base-uri 'none'",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
}

func assertSecurityHeaders(t *testing.T, what string, h http.Header) {
	t.Helper()
	for k, want := range wantSecurityHeaders {
		if got := h.Values(k); len(got) != 1 || got[0] != want {
			t.Errorf("%s: %s = %q, want exactly %q", what, k, got, want)
		}
	}
}

// startHandler serves the full newHandler stack (with a fast SSE keepalive) over httptest.
func startHandler(t *testing.T) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := openStore(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	mgr := supervisor.New(supervisor.Options{Config: store.Get(), Logger: log})
	t.Cleanup(mgr.Close)
	uiFS := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>sockswebui</title>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"assets/app-1.css": {Data: []byte("body{}")},
	}
	srv := httptest.NewServer(newHandler(store, log, mgr, uiFS, api.NewNameList(""), api.WithSSEKeepalive(20*time.Millisecond)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSecurityHeaders(t *testing.T) {
	base := startHandler(t)
	rows := []struct {
		path   string
		status int
	}{
		{"/", 200},
		{"/assets/app-1.js", 200},
		{"/assets/app-1.css", 200},
		{"/no-such-file", 404},
		{"/api/settings", 401},
		{"/api/nope", 401},
	}
	for _, r := range rows {
		res, _ := doReq(t, "GET", base+r.path, "")
		if res.StatusCode != r.status {
			t.Errorf("GET %s: status %d, want %d", r.path, res.StatusCode, r.status)
		}
		assertSecurityHeaders(t, "GET "+r.path, res.Header)
	}

	c := setupAndLogin(t, base)
	res, body := doReq(t, "GET", base+"/api/settings", "", c)
	if res.StatusCode != 200 || !strings.Contains(body, "ui_bind_addr") {
		t.Fatalf("GET /api/settings: %d %s", res.StatusCode, body)
	}
	assertSecurityHeaders(t, "GET /api/settings (200)", res.Header)

	t.Run("events", func(t *testing.T) {
		req, err := http.NewRequest("GET", base+"/api/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(c)
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("GET /api/events: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("events: status %d content-type %q", res.StatusCode, res.Header.Get("Content-Type"))
		}
		assertSecurityHeaders(t, "GET /api/events", res.Header)
		// The keepalive must flush through the full stack (no writer wrapping).
		lines := make(chan string, 16)
		go func() {
			br := bufio.NewReader(res.Body)
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					close(lines)
					return
				}
				lines <- l
			}
		}()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatal("stream ended before a keepalive")
				}
				if l == ": keepalive\n" {
					return
				}
			case <-deadline:
				t.Fatal("no keepalive flushed within 5s")
			}
		}
	})
}
