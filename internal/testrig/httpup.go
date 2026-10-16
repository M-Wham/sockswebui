package testrig

// Fake HTTP CONNECT upstream with optional Proxy-Authorization: Basic.
// Test fake only. See upstream.go for how tests identify the last hop (SourceIP).

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// HTTPUpstream is a fake HTTP CONNECT proxy that records CONNECT targets.
type HTTPUpstream struct{ *upstream }

// NewHTTPUpstream serves HTTP CONNECT on ln, dialing onward with cfg.Dial.
// The caller owns Close.
func NewHTTPUpstream(ln net.Listener, cfg UpstreamConfig) *HTTPUpstream {
	return &HTTPUpstream{newUpstream(ln, cfg, handleHTTPConnect)}
}

// NewLoopbackHTTP serves on 127.0.0.1:0, dials onward from a distinct
// loopback SourceIP, and registers Close with t.Cleanup.
func NewLoopbackHTTP(t testing.TB, cfg UpstreamConfig) *HTTPUpstream {
	t.Helper()
	up, err := StartLoopbackHTTP(cfg)
	if err != nil {
		t.Fatalf("testrig: %v", err)
	}
	t.Cleanup(func() { _ = up.Close() })
	return up
}

// StartLoopbackHTTP is NewLoopbackHTTP without testing.TB: the caller owns Close.
func StartLoopbackHTTP(cfg UpstreamConfig) (*HTTPUpstream, error) {
	ln, cfg, src, err := listenLoopback(cfg)
	if err != nil {
		return nil, err
	}
	up := NewHTTPUpstream(ln, cfg)
	up.sourceIP = src
	return up, nil
}

func handleHTTPConnect(u *upstream, c net.Conn) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		httpStatus(c, http.StatusMethodNotAllowed, "")
		return
	}
	if h := req.Header.Get("Proxy-Authorization"); u.authRequired() && !(h == "" && u.noAuthOK()) && !u.basicOK(h) {
		httpStatus(c, http.StatusProxyAuthRequired, "Proxy-Authenticate: Basic realm=\"testrig\"\r\n")
		return
	}
	target := req.RequestURI // "host:port" verbatim as received
	u.record(target)
	remote, err := u.dialOnward(target)
	if err != nil {
		httpStatus(c, http.StatusBadGateway, "")
		return
	}
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = remote.Close()
		return
	}
	// Bytes the client sent after the request may sit in br; read through it.
	u.relay(struct {
		io.Reader
		io.Writer
	}{br, c}, c, remote)
}

func (u *upstream) basicOK(h string) bool {
	enc, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return ok && u.authOK(user, pass)
}

func httpStatus(c net.Conn, code int, extra string) {
	_, _ = io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(code)+" "+http.StatusText(code)+
		"\r\n"+extra+"Content-Length: 0\r\nConnection: close\r\n\r\n")
}
