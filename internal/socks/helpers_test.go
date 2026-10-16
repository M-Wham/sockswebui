package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeDialer records every dial and returns one end of a net.Pipe.
type fakeDialer struct {
	mu      sync.Mutex
	calls   []string // "network addr"
	remotes chan net.Conn
	err     error
	delay   time.Duration
}

func newFakeDialer() *fakeDialer { return &fakeDialer{remotes: make(chan net.Conn, 8)} }

func (f *fakeDialer) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.calls = append(f.calls, network+" "+addr)
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, f.err
	}
	local, remote := net.Pipe()
	f.remotes <- remote
	return local, nil
}

func (f *fakeDialer) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func startServer(t *testing.T, opts Options) (*Server, string) {
	t.Helper()
	s, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()
	t.Cleanup(func() {
		_ = s.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("Serve did not return after Close")
		}
	})
	return s, ln.Addr().String()
}

func dialServer(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mustWrite(t *testing.T, c net.Conn, b []byte) {
	t.Helper()
	if _, err := c.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustRead(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return b
}

// greet sends a greeting with the given methods and returns the chosen method.
func greet(t *testing.T, c net.Conn, methods ...byte) byte {
	t.Helper()
	mustWrite(t, c, append([]byte{0x05, byte(len(methods))}, methods...))
	r := mustRead(t, c, 2)
	if r[0] != 0x05 {
		t.Fatalf("greeting reply version = %#x", r[0])
	}
	return r[1]
}

func userPass(t *testing.T, c net.Conn, user, pass string) byte {
	t.Helper()
	msg := []byte{0x01, byte(len(user))}
	msg = append(msg, user...)
	msg = append(msg, byte(len(pass)))
	msg = append(msg, pass...)
	mustWrite(t, c, msg)
	r := mustRead(t, c, 2)
	if r[0] != 0x01 {
		t.Fatalf("auth reply version = %#x", r[0])
	}
	return r[1]
}

func requestBytes(cmd byte, atyp byte, addr []byte, port uint16) []byte {
	b := []byte{0x05, cmd, 0x00, atyp}
	if atyp == 0x03 {
		b = append(b, byte(len(addr)))
	}
	b = append(b, addr...)
	return binary.BigEndian.AppendUint16(b, port)
}

// readReply reads a full SOCKS5 reply and returns the REP code.
func readReply(t *testing.T, c net.Conn) byte {
	t.Helper()
	h := mustRead(t, c, 4)
	if h[0] != 0x05 {
		t.Fatalf("reply version = %#x", h[0])
	}
	switch h[3] {
	case 0x01:
		mustRead(t, c, 4+2)
	case 0x04:
		mustRead(t, c, 16+2)
	case 0x03:
		l := mustRead(t, c, 1)
		mustRead(t, c, int(l[0])+2)
	default:
		t.Fatalf("reply atyp = %#x", h[3])
	}
	return h[1]
}

func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 1)
	n, err := c.Read(b)
	if err == nil {
		t.Fatalf("expected closed conn, read %d bytes %#x", n, b[:n])
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("conn not closed (read timed out)")
	}
}
