package testrig

// Minimal fake SOCKS5 upstream (RFC 1928 CONNECT only, ATYP IPv4/domain, with
// optional RFC 1929 user/pass). Test fake only — not the product server.
// See upstream.go for how tests identify the last hop (SourceIP).

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
)

const (
	socksVer      = 5
	methodNone    = 0x00
	methodUserPwd = 0x02
	methodNoAccep = 0xFF

	repOK          = 0x00
	repFailure     = 0x01
	repHostUnreach = 0x04
	repCmdUnsupp   = 0x07
	repATYPUnsupp  = 0x08
)

// SOCKS5Upstream is a fake SOCKS5 proxy that records CONNECT targets.
type SOCKS5Upstream struct{ *upstream }

// NewSOCKS5Upstream serves SOCKS5 on ln, dialing onward with cfg.Dial
// (e.g. a WG peer netstack's DialContext). The caller owns Close.
func NewSOCKS5Upstream(ln net.Listener, cfg UpstreamConfig) *SOCKS5Upstream {
	return &SOCKS5Upstream{newUpstream(ln, cfg, handleSOCKS5)}
}

// NewLoopbackSOCKS5 serves on 127.0.0.1:0, dials onward from a distinct
// loopback SourceIP, and registers Close with t.Cleanup.
func NewLoopbackSOCKS5(t testing.TB, cfg UpstreamConfig) *SOCKS5Upstream {
	t.Helper()
	up, err := StartLoopbackSOCKS5(cfg)
	if err != nil {
		t.Fatalf("testrig: %v", err)
	}
	t.Cleanup(func() { _ = up.Close() })
	return up
}

// StartLoopbackSOCKS5 is NewLoopbackSOCKS5 without testing.TB: the caller owns Close.
func StartLoopbackSOCKS5(cfg UpstreamConfig) (*SOCKS5Upstream, error) {
	ln, cfg, src, err := listenLoopback(cfg)
	if err != nil {
		return nil, err
	}
	up := NewSOCKS5Upstream(ln, cfg)
	up.sourceIP = src
	return up, nil
}

func handleSOCKS5(u *upstream, c net.Conn) {
	if !socksNegotiate(u, c) {
		return
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != socksVer {
		return
	}
	if hdr[1] != 0x01 { // CONNECT only
		socksReply(c, repCmdUnsupp)
		return
	}
	var host string
	switch hdr[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = netip.AddrFrom4([4]byte(b)).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	default:
		socksReply(c, repATYPUnsupp)
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	u.record(target)
	remote, err := u.dialOnward(target)
	if err != nil {
		socksReply(c, repHostUnreach)
		return
	}
	if !socksReply(c, repOK) {
		_ = remote.Close()
		return
	}
	u.relay(c, c, remote)
}

// socksNegotiate handles the greeting and optional RFC 1929 sub-negotiation.
func socksNegotiate(u *upstream, c net.Conn) bool {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != socksVer {
		return false
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return false
	}
	want, ok := pickMethod(u, methods)
	if !ok {
		_, _ = c.Write([]byte{socksVer, methodNoAccep})
		return false
	}
	if _, err := c.Write([]byte{socksVer, want}); err != nil {
		return false
	}
	if want == methodNone {
		return true
	}
	user, pass, ok := readUserPass(c)
	if !ok {
		return false
	}
	if !u.authOK(user, pass) {
		_, _ = c.Write([]byte{0x01, 0x01})
		return false
	}
	_, err := c.Write([]byte{0x01, 0x00})
	return err == nil
}

// pickMethod prefers user/pass when auth is required and offered, else
// no-auth when that is acceptable.
func pickMethod(u *upstream, methods []byte) (byte, bool) {
	offered := func(want byte) bool {
		for _, m := range methods {
			if m == want {
				return true
			}
		}
		return false
	}
	if u.authRequired() && offered(methodUserPwd) {
		return methodUserPwd, true
	}
	if u.noAuthOK() && offered(methodNone) {
		return methodNone, true
	}
	return 0, false
}

func readUserPass(c net.Conn) (user, pass string, ok bool) {
	readStr := func() (string, bool) {
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", false
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", false
		}
		return string(b), true
	}
	v := make([]byte, 1)
	if _, err := io.ReadFull(c, v); err != nil || v[0] != 0x01 {
		return "", "", false
	}
	if user, ok = readStr(); !ok {
		return "", "", false
	}
	pass, ok = readStr()
	return user, pass, ok
}

// socksReply writes a reply with a zero IPv4 bound address.
func socksReply(c net.Conn, code byte) bool {
	_, err := c.Write([]byte{socksVer, code, 0, 0x01, 0, 0, 0, 0, 0, 0})
	return err == nil
}
