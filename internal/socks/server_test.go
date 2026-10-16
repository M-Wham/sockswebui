package socks

import (
	"bytes"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestNewRequiresDialer(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New with nil Dialer: want error (fail closed)")
	}
	if _, err := New(Options{Dialer: newFakeDialer(), Username: "u"}); err == nil {
		t.Fatal("New with username but no password: want error")
	}
}

func TestServerConnect(t *testing.T) {
	cases := []struct {
		name string
		atyp byte
		addr []byte
		port uint16
		want string
	}{
		{"ipv4", 0x01, []byte{192, 0, 2, 7}, 80, "tcp 192.0.2.7:80"},
		{"ipv6", 0x04, net.ParseIP("2001:db8::1").To16(), 8443, "tcp [2001:db8::1]:8443"},
		{"domain", 0x03, []byte("example.invalid"), 443, "tcp example.invalid:443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := newFakeDialer()
			_, addr := startServer(t, Options{Dialer: fd})
			c := dialServer(t, addr)
			if m := greet(t, c, 0x00); m != 0x00 {
				t.Fatalf("method = %#x, want 0x00", m)
			}
			mustWrite(t, c, requestBytes(0x01, tc.atyp, tc.addr, tc.port))
			if rep := readReply(t, c); rep != 0x00 {
				t.Fatalf("rep = %#x, want 0x00", rep)
			}
			if got := fd.Calls(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("dialer calls = %q, want [%q]", got, tc.want)
			}
			remote := <-fd.remotes
			defer remote.Close()
			_ = remote.SetDeadline(time.Now().Add(5 * time.Second))

			// client -> remote
			mustWrite(t, c, []byte("ping"))
			if got := mustRead(t, remote, 4); !bytes.Equal(got, []byte("ping")) {
				t.Fatalf("remote got %q", got)
			}
			// remote -> client
			mustWrite(t, remote, []byte("pong!"))
			if got := mustRead(t, c, 5); !bytes.Equal(got, []byte("pong!")) {
				t.Fatalf("client got %q", got)
			}
			// closing the remote closes the client side.
			_ = remote.Close()
			expectClosed(t, c)
		})
	}
}

func TestServerAuth(t *testing.T) {
	authOpts := func(fd *fakeDialer) Options {
		return Options{Dialer: fd, Username: "alice", Password: "s3cret"}
	}
	t.Run("no-auth offered when auth required", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, authOpts(fd))
		c := dialServer(t, addr)
		if m := greet(t, c, 0x00); m != 0xFF {
			t.Fatalf("method = %#x, want 0xFF", m)
		}
		expectClosed(t, c)
		if n := len(fd.Calls()); n != 0 {
			t.Fatalf("dialer called %d times", n)
		}
	})
	t.Run("correct credentials", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, authOpts(fd))
		c := dialServer(t, addr)
		if m := greet(t, c, 0x00, 0x02); m != 0x02 {
			t.Fatalf("method = %#x, want 0x02", m)
		}
		if st := userPass(t, c, "alice", "s3cret"); st != 0x00 {
			t.Fatalf("auth status = %#x, want 0x00", st)
		}
		mustWrite(t, c, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
		if rep := readReply(t, c); rep != 0x00 {
			t.Fatalf("rep = %#x", rep)
		}
		if got := fd.Calls(); len(got) != 1 {
			t.Fatalf("dialer calls = %q", got)
		}
		(<-fd.remotes).Close()
	})
	for _, bad := range []struct{ name, user, pass string }{
		{"wrong password", "alice", "nope"},
		{"wrong user", "bob", "s3cret"},
		{"empty", "", ""},
	} {
		t.Run(bad.name, func(t *testing.T) {
			fd := newFakeDialer()
			_, addr := startServer(t, authOpts(fd))
			c := dialServer(t, addr)
			if m := greet(t, c, 0x02); m != 0x02 {
				t.Fatalf("method = %#x", m)
			}
			if st := userPass(t, c, bad.user, bad.pass); st != 0x01 {
				t.Fatalf("auth status = %#x, want 0x01", st)
			}
			expectClosed(t, c)
			if n := len(fd.Calls()); n != 0 {
				t.Fatalf("dialer called %d times on auth failure", n)
			}
		})
	}
	t.Run("no auth configured", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd})
		c := dialServer(t, addr)
		if m := greet(t, c, 0x02, 0x00); m != 0x00 {
			t.Fatalf("method = %#x, want 0x00", m)
		}
	})
	t.Run("no auth configured, client offers only user/pass", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd})
		c := dialServer(t, addr)
		if m := greet(t, c, 0x02); m != 0xFF {
			t.Fatalf("method = %#x, want 0xFF", m)
		}
		expectClosed(t, c)
	})
}

func TestServerUnsupported(t *testing.T) {
	for _, cmd := range []struct {
		name string
		cmd  byte
	}{{"bind", 0x02}, {"udp associate", 0x03}, {"unknown", 0x09}} {
		t.Run(cmd.name, func(t *testing.T) {
			fd := newFakeDialer()
			_, addr := startServer(t, Options{Dialer: fd})
			c := dialServer(t, addr)
			greet(t, c, 0x00)
			mustWrite(t, c, requestBytes(cmd.cmd, 0x01, []byte{192, 0, 2, 1}, 80))
			if rep := readReply(t, c); rep != 0x07 {
				t.Fatalf("rep = %#x, want 0x07", rep)
			}
			expectClosed(t, c)
			if n := len(fd.Calls()); n != 0 {
				t.Fatalf("dialer called %d times", n)
			}
		})
	}
	t.Run("bad atyp", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd})
		c := dialServer(t, addr)
		greet(t, c, 0x00)
		mustWrite(t, c, []byte{0x05, 0x01, 0x00, 0x07, 1, 2, 3, 4, 0, 80})
		if rep := readReply(t, c); rep != 0x08 {
			t.Fatalf("rep = %#x, want 0x08", rep)
		}
		expectClosed(t, c)
	})
	t.Run("bad version byte", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd})
		c := dialServer(t, addr)
		mustWrite(t, c, []byte{0x04, 0x01, 0x00})
		expectClosed(t, c)
		if n := len(fd.Calls()); n != 0 {
			t.Fatalf("dialer called %d times", n)
		}
	})
	t.Run("bad request version", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd})
		c := dialServer(t, addr)
		greet(t, c, 0x00)
		mustWrite(t, c, []byte{0x04, 0x01, 0x00, 0x01, 1, 2, 3, 4, 0, 80})
		expectClosed(t, c)
		if n := len(fd.Calls()); n != 0 {
			t.Fatalf("dialer called %d times", n)
		}
	})
	t.Run("dialer error", func(t *testing.T) {
		fd := newFakeDialer()
		fd.err = errors.New("no route")
		_, addr := startServer(t, Options{Dialer: fd})
		c := dialServer(t, addr)
		greet(t, c, 0x00)
		mustWrite(t, c, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
		if rep := readReply(t, c); rep != 0x01 {
			t.Fatalf("rep = %#x, want 0x01", rep)
		}
		expectClosed(t, c)
		if got := fd.Calls(); len(got) != 1 {
			t.Fatalf("dialer calls = %q, want exactly one (no fallback)", got)
		}
	})
	t.Run("handshake timeout", func(t *testing.T) {
		fd := newFakeDialer()
		_, addr := startServer(t, Options{Dialer: fd, HandshakeTimeout: 100 * time.Millisecond})
		c := dialServer(t, addr)
		mustWrite(t, c, []byte{0x05}) // stall mid-greeting
		expectClosed(t, c)
	})
}

func TestServerClose(t *testing.T) {
	base := runtime.NumGoroutine()

	fd := newFakeDialer()
	s, err := New(Options{Dialer: fd})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(ln) }()

	// Two active relays plus one connection idle in the handshake.
	var clients []net.Conn
	var remotes []net.Conn
	for i := 0; i < 2; i++ {
		c := dialServer(t, ln.Addr().String())
		greet(t, c, 0x00)
		mustWrite(t, c, requestBytes(0x01, 0x03, []byte("example.invalid"), 443))
		if rep := readReply(t, c); rep != 0x00 {
			t.Fatalf("rep = %#x", rep)
		}
		clients = append(clients, c)
		remotes = append(remotes, <-fd.remotes)
	}
	idle := dialServer(t, ln.Addr().String())
	clients = append(clients, idle)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-served:
		if !errors.Is(err, ErrServerClosed) {
			t.Fatalf("Serve returned %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
	for _, c := range clients {
		expectClosed(t, c)
	}
	for _, r := range remotes {
		_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := r.Read(make([]byte, 1)); err == nil {
			t.Fatal("remote side still open after Close")
		}
		_ = r.Close()
	}
	for _, c := range clients {
		_ = c.Close()
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Serve(ln); !errors.Is(err, ErrServerClosed) {
		t.Fatalf("Serve after Close = %v, want ErrServerClosed", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= base+2 {
			break
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines = %d, baseline %d\n%s", n, base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}
