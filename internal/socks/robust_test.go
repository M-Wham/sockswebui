package socks

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A dial that outlasts HandshakeTimeout (but not DialTimeout) must still
// produce a success reply: the handshake deadline must not cover the dial.
func TestServerConnectSlowDial(t *testing.T) {
	fd := newFakeDialer()
	fd.delay = 300 * time.Millisecond
	_, addr := startServer(t, Options{Dialer: fd, HandshakeTimeout: 200 * time.Millisecond, DialTimeout: 5 * time.Second})
	c := dialServer(t, addr)
	greet(t, c, 0x00)
	mustWrite(t, c, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
	if rep := readReply(t, c); rep != 0x00 {
		t.Fatalf("rep = %#x, want 0x00", rep)
	}
	remote := <-fd.remotes
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(5 * time.Second))
	mustWrite(t, c, []byte("hi"))
	if got := mustRead(t, remote, 2); string(got) != "hi" {
		t.Fatalf("remote got %q", got)
	}
}

// flakyListener returns a non-timeout error (EMFILE) for the first N Accepts.
type flakyListener struct {
	net.Listener
	mu    sync.Mutex
	fails int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.fails > 0 {
		l.fails--
		l.mu.Unlock()
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func TestServerAcceptErrorRetries(t *testing.T) {
	fd := newFakeDialer()
	s, err := New(Options{Dialer: fd})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &flakyListener{Listener: inner, fails: 3}
	served := make(chan error, 1)
	go func() { served <- s.Serve(ln) }()

	c := dialServer(t, inner.Addr().String())
	if m := greet(t, c, 0x00); m != 0x00 {
		t.Fatalf("method = %#x", m)
	}
	select {
	case err := <-served:
		t.Fatalf("Serve returned early: %v", err)
	default:
	}
	_ = s.Close()
	select {
	case err := <-served:
		if !errors.Is(err, ErrServerClosed) {
			t.Fatalf("Serve = %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}

func TestServerMaxConns(t *testing.T) {
	fd := newFakeDialer()
	_, addr := startServer(t, Options{Dialer: fd, MaxConns: 1})
	first := dialServer(t, addr)
	greet(t, first, 0x00) // first conn is being served
	second := dialServer(t, addr)
	expectClosed(t, second)
}

func TestAuthFailureLogSanitizesUser(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
	fd := newFakeDialer()
	_, addr := startServer(t, Options{Dialer: fd, Username: "alice", Password: "s3cret", Logger: logger})
	c := dialServer(t, addr)
	greet(t, c, 0x02)
	evil := strings.Repeat("A", 200) + "\nlevel=ERROR forged=1"
	if st := userPass(t, c, evil, "nope-password"); st != 0x01 {
		t.Fatalf("status = %#x", st)
	}
	expectClosed(t, c)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "auth_failed") {
		t.Fatalf("log missing reason: %q", out)
	}
	if strings.Contains(out, strings.Repeat("A", 65)) {
		t.Fatalf("username not truncated: %q", out)
	}
	if strings.Contains(out, "forged") || strings.Contains(out, `\n`) {
		t.Fatalf("username not sanitized: %q", out)
	}
	if strings.Contains(out, "nope-password") {
		t.Fatal("password logged")
	}
}

func TestSanitizeUser(t *testing.T) {
	cases := map[string]string{
		"alice":                 "alice",
		"a\x00b\nc":             "a?b?c",
		strings.Repeat("x", 80): strings.Repeat("x", 64) + "...",
		"\xff\xfe":              "??",
	}
	for in, want := range cases {
		if got := sanitizeUser(in); got != want {
			t.Errorf("sanitizeUser(%q) = %q, want %q", in, got, want)
		}
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// dialFrom connects to addr from the given loopback source address.
func dialFrom(t *testing.T, src, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial from %s: %v", src, err)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// counts returns the tracked client total and the per-source count for key.
func (s *Server) counts(key string) (active, perSource int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.perSource.n[key]
}

func waitSourceCount(t *testing.T, s *Server, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, got := s.counts(key)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("per-source count for %s = %d, want %d", key, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// connectOK runs a full no-auth CONNECT on c and checks payload reaches the remote.
func connectOK(t *testing.T, c net.Conn, fd *fakeDialer) {
	t.Helper()
	if m := greet(t, c, 0x00); m != 0x00 {
		t.Fatalf("method = %#x", m)
	}
	mustWrite(t, c, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
	if rep := readReply(t, c); rep != 0x00 {
		t.Fatalf("rep = %#x, want 0x00", rep)
	}
	remote := <-fd.remotes
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(5 * time.Second))
	mustWrite(t, c, []byte("hi"))
	if got := mustRead(t, remote, 2); string(got) != "hi" {
		t.Fatalf("remote got %q", got)
	}
}

// TestPerSourceCap: one source cannot hold the whole listener.
func TestPerSourceCap(t *testing.T) {
	fd := newFakeDialer()
	s, addr := startServer(t, Options{Dialer: fd, MaxConns: 256, HandshakeTimeout: time.Minute})
	if s.maxConns != 256 || s.perSource.limit != 128 {
		t.Fatalf("maxConns=%d perSource=%d, want 256/128", s.maxConns, s.perSource.limit)
	}
	const hog = "127.0.0.2"
	conns := make([]net.Conn, 300)
	for i := range conns {
		conns[i] = dialFrom(t, hog, addr)
		mustWrite(t, conns[i], []byte{0x05, 0x01, 0x00}) // greeting: answered only if read
	}

	// The accept loop handles conns in order, so once this CONNECT is served every
	// hog conn has been accepted or refused. The relay is held open (both ends
	// closed at cleanup) so it is still tracked when the counts are read below.
	openRelay(t, addr, fd)

	var served, refused []net.Conn
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			b := make([]byte, 2)
			n, err := c.Read(b)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && n >= 1 && b[0] == 0x05:
				served = append(served, c)
			case err != nil && n == 0:
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					t.Errorf("hog conn neither answered nor closed")
					return
				}
				refused = append(refused, c)
			default:
				t.Errorf("hog conn: n=%d err=%v b=%#x", n, err, b[:n])
			}
		}(c)
	}
	wg.Wait()
	if len(served) != 128 || len(refused) != 172 {
		t.Fatalf("served=%d refused=%d, want 128/172", len(served), len(refused))
	}
	if active, n := s.counts(hog); active != 129 || n != 128 {
		t.Fatalf("tracked active=%d hog=%d, want 129 (128 hog + the relay)/128", active, n)
	}
	if calls := fd.Calls(); len(calls) != 1 {
		t.Fatalf("dialer calls = %v, want only the 127.0.0.1 CONNECT", calls)
	}

	// Releasing one hog conn admits the next.
	_ = served[0].Close()
	waitSourceCount(t, s, hog, 127)
	next := dialFrom(t, hog, addr)
	if m := greet(t, next, 0x00); m != 0x00 {
		t.Fatalf("method = %#x", m)
	}
	waitSourceCount(t, s, hog, 128)
	over := dialFrom(t, hog, addr)
	mustWrite(t, over, []byte{0x05, 0x01, 0x00})
	expectClosed(t, over)
}

// TestPerSourceCapNoLeak: every exit path releases the per-source slot.
func TestPerSourceCapNoLeak(t *testing.T) {
	fd := newFakeDialer()
	s, addr := startServer(t, Options{Dialer: fd, MaxConns: 4})
	for i := 0; i < 1000; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if i%2 == 0 {
			_, _ = c.Write([]byte{0x04, 0x01, 0x00}) // bad version: server rejects and closes
			expectClosed(t, c)
		}
		_ = c.Close() // odd i: client hangs up before greeting (EOF)
	}
	waitSourceCount(t, s, "127.0.0.1", 0)
	connectOK(t, dialServer(t, addr), fd)
	waitSourceCount(t, s, "127.0.0.1", 1)
	if active, _ := s.counts("127.0.0.1"); active != 1 {
		t.Fatalf("active = %d, want 1", active)
	}
}

// TestPerSourceCapDefault: MaxConnsPerSource 0 resolves to max(1, MaxConns/2).
func TestPerSourceCapDefault(t *testing.T) {
	for _, tc := range []struct{ maxConns, perSource, want int }{
		{256, 0, 128}, {0, 0, 128}, {3, 0, 1}, {1, 0, 1}, {256, 5, 5},
	} {
		s, err := New(Options{Dialer: newFakeDialer(), MaxConns: tc.maxConns, MaxConnsPerSource: tc.perSource})
		if err != nil {
			t.Fatal(err)
		}
		if s.perSource.limit != tc.want {
			t.Errorf("MaxConns=%d MaxConnsPerSource=%d: cap = %d, want %d",
				tc.maxConns, tc.perSource, s.perSource.limit, tc.want)
		}
	}
}

// TestPerSourceCapKeying: the cap uses sourceKey, so remotes sharing a key in the shared
// table share one budget.
func TestPerSourceCapKeying(t *testing.T) {
	sc := newSourceCounts(1)
	held := map[string]bool{}
	for _, tc := range clientKeyTable {
		ok := sc.acquire(sourceKey(tc.remote))
		if ok == held[tc.key] {
			t.Errorf("acquire(%q) = %v with key %q already held=%v", tc.remote, ok, tc.key, held[tc.key])
		}
		held[tc.key] = true
	}
	for key := range held {
		sc.release(key)
	}
	if len(sc.n) != 0 {
		t.Fatalf("table not empty after release: %v", sc.n)
	}
}
