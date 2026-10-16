package testrig

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// startLoopbackEcho serves EchoHandler(hop) on 127.0.0.1:0 and returns its port.
func startLoopbackEcho(t *testing.T, hop string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	done := ServeEcho(ln, hop)
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return ln.Addr().(*net.TCPAddr).Port
}

func dialTest(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// echoOver issues GET / over an established tunnel and decodes the echo body.
func echoOver(t *testing.T, c net.Conn) map[string]string {
	t.Helper()
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: echo\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write GET: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	return m
}

// socksHandshake performs greeting (+ optional RFC 1929 auth). It returns the
// RFC 1929 status (0 when no auth was negotiated) and the chosen method.
func socksHandshake(t *testing.T, c net.Conn, user, pass string) (method, status byte) {
	t.Helper()
	if user == "" {
		mustWrite(t, c, []byte{5, 1, 0})
	} else {
		mustWrite(t, c, []byte{5, 1, 2})
	}
	rep := mustRead(t, c, 2)
	if rep[0] != 5 {
		t.Fatalf("greeting version %d", rep[0])
	}
	if rep[1] != 2 {
		return rep[1], 0
	}
	msg := append([]byte{1, byte(len(user))}, user...)
	msg = append(append(msg, byte(len(pass))), pass...)
	mustWrite(t, c, msg)
	ar := mustRead(t, c, 2)
	return 2, ar[1]
}

// socksConnect sends CONNECT for host:port (domain if host is not IPv4) and
// returns the reply code.
func socksConnect(t *testing.T, c net.Conn, host string, port int) byte {
	t.Helper()
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host).To4(); ip != nil {
		req = append(append(req, 1), ip...)
	} else {
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	mustWrite(t, c, req)
	rep := mustRead(t, c, 10) // IPv4 bound address in replies
	return rep[1]
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

func assertClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("expected EOF after failure, got n=%d err=%v", n, err)
	}
}

func TestSOCKS5Upstream(t *testing.T) {
	port := startLoopbackEcho(t, "echo")

	t.Run("no-auth CONNECT", func(t *testing.T) {
		up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "socksA"})
		c := dialTest(t, up.Addr())
		if m, _ := socksHandshake(t, c, "", ""); m != 0 {
			t.Fatalf("method = %#x, want 0x00", m)
		}
		if code := socksConnect(t, c, "127.0.0.1", port); code != 0 {
			t.Fatalf("reply = %#x", code)
		}
		got := echoOver(t, c)
		if got["ip"] != up.SourceIP().String() || got["hop"] != "echo" {
			t.Fatalf("echo = %v, want ip %s", got, up.SourceIP())
		}
		want := "127.0.0.1:" + strconv.Itoa(port)
		if tg := up.Targets(); len(tg) != 1 || tg[0] != want {
			t.Fatalf("Targets = %v, want [%s]", tg, want)
		}
	})

	t.Run("auth correct creds", func(t *testing.T) {
		up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "socksB", Username: "u", Password: "p"})
		c := dialTest(t, up.Addr())
		if m, st := socksHandshake(t, c, "u", "p"); m != 2 || st != 0 {
			t.Fatalf("method %#x status %#x", m, st)
		}
		if code := socksConnect(t, c, "127.0.0.1", port); code != 0 {
			t.Fatalf("reply = %#x", code)
		}
		if got := echoOver(t, c); got["ip"] != up.SourceIP().String() {
			t.Fatalf("echo = %v", got)
		}
	})

	t.Run("auth wrong creds", func(t *testing.T) {
		up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "socksC", Username: "u", Password: "p"})
		c := dialTest(t, up.Addr())
		if m, st := socksHandshake(t, c, "u", "wrong"); m != 2 || st == 0 {
			t.Fatalf("method %#x status %#x, want non-zero status", m, st)
		}
		assertClosed(t, c)
		if tg := up.Targets(); len(tg) != 0 {
			t.Fatalf("Targets = %v, want none", tg)
		}
	})

	t.Run("auth required but not offered", func(t *testing.T) {
		up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "socksD", Username: "u", Password: "p"})
		c := dialTest(t, up.Addr())
		if m, _ := socksHandshake(t, c, "", ""); m != 0xFF {
			t.Fatalf("method = %#x, want 0xFF", m)
		}
		assertClosed(t, c)
	})

	t.Run("domain target via static hosts", func(t *testing.T) {
		up := NewLoopbackSOCKS5(t, UpstreamConfig{
			Name:  "socksE",
			Hosts: map[string]string{"echo.rig.invalid": "127.0.0.1"},
		})
		c := dialTest(t, up.Addr())
		socksHandshake(t, c, "", "")
		if code := socksConnect(t, c, "echo.rig.invalid", port); code != 0 {
			t.Fatalf("reply = %#x", code)
		}
		if got := echoOver(t, c); got["ip"] != up.SourceIP().String() {
			t.Fatalf("echo = %v", got)
		}
		want := "echo.rig.invalid:" + strconv.Itoa(port)
		if tg := up.Targets(); len(tg) != 1 || tg[0] != want {
			t.Fatalf("Targets = %v, want [%s]", tg, want)
		}
	})

	t.Run("unknown domain fails without device resolver", func(t *testing.T) {
		up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "socksF"})
		c := dialTest(t, up.Addr())
		socksHandshake(t, c, "", "")
		if code := socksConnect(t, c, "localhost", port); code == 0 {
			t.Fatal("localhost resolved without a Hosts entry")
		}
	})
}

func httpConnect(t *testing.T, c net.Conn, target, auth string) *http.Response {
	t.Helper()
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if auth != "" {
		req += "Proxy-Authorization: " + auth + "\r\n"
	}
	mustWrite(t, c, []byte(req+"\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	return resp
}

func basic(u, p string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))
}

func TestHTTPConnectUpstream(t *testing.T) {
	port := startLoopbackEcho(t, "echo")
	hosts := map[string]string{"echo.rig.invalid": "127.0.0.1"}

	t.Run("CONNECT then tunnel", func(t *testing.T) {
		up := NewLoopbackHTTP(t, UpstreamConfig{Name: "httpA", Hosts: hosts})
		c := dialTest(t, up.Addr())
		target := "echo.rig.invalid:" + strconv.Itoa(port)
		if resp := httpConnect(t, c, target, ""); resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if got := echoOver(t, c); got["ip"] != up.SourceIP().String() {
			t.Fatalf("echo = %v, want ip %s", got, up.SourceIP())
		}
		if tg := up.Targets(); len(tg) != 1 || tg[0] != target {
			t.Fatalf("Targets = %v, want [%s]", tg, target)
		}
	})

	t.Run("auth", func(t *testing.T) {
		up := NewLoopbackHTTP(t, UpstreamConfig{Name: "httpB", Username: "u", Password: "p", Hosts: hosts})
		target := "echo.rig.invalid:" + strconv.Itoa(port)
		for _, tc := range []struct {
			name, auth string
			want       int
		}{
			{"missing", "", 407},
			{"wrong", basic("u", "nope"), 407},
			{"correct", basic("u", "p"), 200},
		} {
			c := dialTest(t, up.Addr())
			resp := httpConnect(t, c, target, tc.auth)
			if resp.StatusCode != tc.want {
				t.Fatalf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.want)
			}
			if tc.want == 200 {
				if got := echoOver(t, c); got["ip"] != up.SourceIP().String() {
					t.Fatalf("echo = %v", got)
				}
			}
		}
		if tg := up.Targets(); len(tg) != 1 || tg[0] != target {
			t.Fatalf("Targets = %v, want only the authorized target", tg)
		}
	})

	t.Run("distinct source IPs", func(t *testing.T) {
		a := NewLoopbackHTTP(t, UpstreamConfig{Name: "x"})
		b := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "y"})
		if a.SourceIP() == b.SourceIP() {
			t.Fatalf("upstreams share source IP %s", a.SourceIP())
		}
	})
}

func TestSentinel(t *testing.T) {
	s := NewSentinel(t)
	if h, _, _ := net.SplitHostPort(s.Addr()); h != "127.0.0.1" {
		t.Fatalf("Addr = %s, want 127.0.0.1", s.Addr())
	}
	const clients, per = 5, 1000
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", s.Addr())
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer c.Close()
			if _, err := c.Write(make([]byte, per)); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for s.Bytes() != clients*per && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Bytes(); got != clients*per {
		t.Fatalf("Bytes = %d, want %d", got, clients*per)
	}
	// Open counts live conns and drops as clients close (all closed above).
	for s.Open() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.Open(); n != 0 {
		t.Fatalf("Open = %d after every client closed, want 0", n)
	}
	held, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	for s.Open() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.Open(); n != 1 {
		t.Fatalf("Open = %d with one held conn, want 1", n)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c, err := net.DialTimeout("tcp", s.Addr(), time.Second); err == nil {
		c.Close()
		t.Fatal("sentinel still accepting after Close")
	}
}
