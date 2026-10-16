package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/api"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// A connection that was accepted but never sent a request (a client's spare or
// speculative dial, a browser preconnect) must not hold up shutdown: Go's
// Server.Shutdown only treats such StateNew conns as idle after 5 s, which used to
// hit the shutdown deadline and turn a clean SIGTERM into exit 1.
func TestServeUIShutdownWithSilentConn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	defer func() { onListen = nil }()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- serveUI(ctx, ln, uiBindSourceFlag, h, log) }()

	var addr string
	select {
	case addr = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// Let the server accept it (StateNew) before shutting down.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("serveUI = %v, want nil", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("shutdown took %v with a silent conn open", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveUI did not return after cancel")
	}
}

// startServeUI runs serveUI on a loopback listener with the given limits and returns
// its address; cleanup cancels it and waits for a clean return.
func startServeUI(t *testing.T, lim serverLimits, h http.Handler) string {
	t.Helper()
	old := uiServerLimits
	uiServerLimits = lim
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- serveUI(ctx, ln, uiBindSourceFlag, h, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(10 * time.Second):
			t.Error("serveUI did not return")
		}
		uiServerLimits = old
	})
	return ln.Addr().String()
}

func TestServeUILimits(t *testing.T) {
	if uiServerLimits != (serverLimits{read: 30 * time.Second, idle: 120 * time.Second, maxHeader: 16 << 10}) {
		t.Errorf("default limits = %+v", uiServerLimits)
	}
	t.Run("header over cap is 431", func(t *testing.T) {
		addr := startServeUI(t, serverLimits{read: 5 * time.Second, idle: 5 * time.Second, maxHeader: 1 << 10},
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		conn := dialRaw(t, addr)
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-Big: %s\r\n\r\n", strings.Repeat("a", 32<<10))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		if !strings.Contains(line, " 431 ") {
			t.Fatalf("status line %q, want 431", line)
		}
	})
	t.Run("idle keep-alive conn closed", func(t *testing.T) {
		addr := startServeUI(t, serverLimits{read: 5 * time.Second, idle: 200 * time.Millisecond, maxHeader: 16 << 10},
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		conn := dialRaw(t, addr)
		fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		br := bufio.NewReader(conn)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		start := time.Now()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := br.ReadByte(); err != io.EOF {
			t.Fatalf("idle conn read = %v, want EOF", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("idle conn closed after %v", d)
		}
	})
	t.Run("trickled body cut off", func(t *testing.T) {
		got := make(chan error, 1)
		addr := startServeUI(t, serverLimits{read: 300 * time.Millisecond, idle: 5 * time.Second, maxHeader: 16 << 10},
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := io.ReadAll(r.Body)
				got <- err
			}))
		conn := dialRaw(t, addr)
		fmt.Fprint(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")
		stop := time.After(3 * time.Second)
		for {
			select {
			case err := <-got:
				if err == nil {
					t.Fatal("trickled body read completed, want timeout error")
				}
				return
			case <-stop:
				t.Fatal("trickled body not cut off after the read timeout")
			case <-time.After(50 * time.Millisecond):
				conn.Write([]byte("a"))
			}
		}
	})
	t.Run("SSE outlives read timeout", func(t *testing.T) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		store, err := openStore(t.TempDir(), log)
		if err != nil {
			t.Fatal(err)
		}
		mgr := supervisor.New(supervisor.Options{Config: store.Get(), Logger: log})
		t.Cleanup(mgr.Close)
		h := newHandler(store, log, mgr, testFS(), api.NewNameList(""), api.WithSSEKeepalive(time.Second))
		base := "http://" + startServeUI(t, serverLimits{read: 300 * time.Millisecond, idle: 5 * time.Second,
			maxHeader: 16 << 10}, h)
		c := setupAndLogin(t, base)
		req, _ := http.NewRequest("GET", base+"/api/events", nil)
		req.AddCookie(c)
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		br := bufio.NewReader(res.Body)
		start := time.Now()
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended after %v: %v", time.Since(start), err)
			}
			if line == ": keepalive\n" {
				return
			}
		}
	})
}

func dialRaw(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
