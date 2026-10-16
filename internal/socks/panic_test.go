package socks

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// panicDialer panics on its first dial and fails cleanly afterwards.
type panicDialer struct{ n atomic.Int32 }

func (d *panicDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	if d.n.Add(1) == 1 {
		panic("boom from dialer")
	}
	return nil, errors.New("down")
}

// A panic while serving one connection must not kill the process or the
// server: that connection is closed, the panic is logged, others are served.
func TestServerRecoversConnectionPanic(t *testing.T) {
	var logs bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &mu}, nil))
	_, addr := startServer(t, Options{Dialer: &panicDialer{}, Logger: log})

	c := dialServer(t, addr)
	greet(t, c, 0x00)
	mustWrite(t, c, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
	expectClosed(t, c)

	c2 := dialServer(t, addr)
	greet(t, c2, 0x00)
	mustWrite(t, c2, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
	if rep := readReply(t, c2); rep != 0x01 {
		t.Fatalf("rep after recovered panic = %#x, want 0x01", rep)
	}
	mu.Lock()
	got := logs.String()
	mu.Unlock()
	if !strings.Contains(got, "panic") || !strings.Contains(got, "level=ERROR") {
		t.Fatalf("panic not logged as ERROR: %q", got)
	}
}

// panicListener panics on Accept.
type panicListener struct{ net.Listener }

func (panicListener) Accept() (net.Conn, error) { panic("boom from accept") }

func TestServerServeRecoversPanic(t *testing.T) {
	s, err := New(Options{Dialer: newFakeDialer()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Serve(panicListener{ln})
	if err == nil || errors.Is(err, ErrServerClosed) {
		t.Fatalf("Serve after panic = %v, want a panic error", err)
	}
	if c, derr := net.Dial("tcp", ln.Addr().String()); derr == nil {
		_ = c.Close()
		t.Fatal("listener still open after Serve recovered a panic")
	}
}
