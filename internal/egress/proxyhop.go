package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// hopAddr is a proxy endpoint's host:port.
func hopAddr(ep config.Endpoint) string {
	return net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))
}

// proxyHop returns a dialFunc that reaches ep through prev and then asks ep,
// via connect, to open a tunnel to the target. The hostname in the target is
// handed to the proxy unresolved.
func proxyHop(ep config.Endpoint, prev dialFunc,
	connect func(c net.Conn, ep config.Endpoint, target string) (net.Conn, error)) dialFunc {
	via := hopAddr(ep)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := prev(ctx, "tcp", via)
		if err != nil {
			return nil, fmt.Errorf("egress: reach %s hop %q: %w", ep.Kind, ep.ID, err)
		}
		out, err := withHandshakeDeadline(ctx, c, func() (net.Conn, error) { return connect(c, ep, addr) })
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("egress: %s hop %q: %w", ep.Kind, ep.ID, err)
		}
		return out, nil
	}
}

// withHandshakeDeadline runs fn with c's deadline set to the earlier of ctx's
// deadline and now+dialTimeout, aborts it if ctx is cancelled, and clears the
// deadline afterwards so the returned connection is unbounded.
func withHandshakeDeadline(ctx context.Context, c net.Conn, fn func() (net.Conn, error)) (net.Conn, error) {
	dl := time.Now().Add(dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	if err := c.SetDeadline(dl); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	out, err := fn()
	if !stop() { // ctx fired mid-handshake and poisoned the deadline
		return nil, fmt.Errorf("handshake aborted: %w", ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return out, nil
}

func socks5Hop(ep config.Endpoint, prev dialFunc) dialFunc {
	return proxyHop(ep, prev, socks5Connect)
}

// SOCKS5 (RFC 1928) CONNECT client with optional RFC 1929 user/pass auth.
const (
	socksVer        = 5
	socksNoAuth     = 0x00
	socksUserPass   = 0x02
	socksNoAccept   = 0xFF
	socksCmdConnect = 0x01
	atypIPv4        = 0x01
	atypDomain      = 0x03
	atypIPv6        = 0x04
)

func socks5Connect(c net.Conn, ep config.Endpoint, target string) (net.Conn, error) {
	user, pass := RenderCredentials(ep)
	method := byte(socksNoAuth)
	if user != "" {
		method = socksUserPass
	}
	if _, err := c.Write([]byte{socksVer, 1, method}); err != nil {
		return nil, err
	}
	var rep [2]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return nil, fmt.Errorf("read method selection: %w", err)
	}
	if rep[0] != socksVer {
		return nil, fmt.Errorf("bad SOCKS version %d", rep[0])
	}
	if rep[1] != method {
		return nil, fmt.Errorf("proxy refused auth method %#x (got %#x)", method, rep[1])
	}
	if method == socksUserPass {
		if err := socks5Auth(c, user, pass); err != nil {
			return nil, err
		}
	}
	req, err := socks5Request(target)
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(req); err != nil {
		return nil, err
	}
	return c, socks5ReadReply(c)
}

// socks5Auth performs RFC 1929 with already-rendered credentials. It is the
// single RFC 1929 length check (defence in depth behind config.Validate) and
// runs before any credential byte is written. Errors never include the
// credentials.
func socks5Auth(c net.Conn, user, pass string) error {
	if len(user) > 255 || len(pass) > 255 {
		return errors.New("username or password longer than 255 bytes")
	}
	msg := make([]byte, 0, 3+len(user)+len(pass))
	msg = append(msg, 0x01, byte(len(user)))
	msg = append(msg, user...)
	msg = append(msg, byte(len(pass)))
	msg = append(msg, pass...)
	if _, err := c.Write(msg); err != nil {
		return err
	}
	var st [2]byte
	if _, err := io.ReadFull(c, st[:]); err != nil {
		return fmt.Errorf("read auth status: %w", err)
	}
	if st[1] != 0x00 {
		return errors.New("proxy rejected credentials")
	}
	return nil
}

// socks5Request encodes CONNECT target; hostnames use ATYP 0x03 (domain).
func socks5Request(target string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("bad port %q", portStr)
	}
	req := []byte{socksVer, socksCmdConnect, 0x00}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		if ip.Is4() || ip.Is4In6() {
			b := ip.Unmap().As4()
			req = append(append(req, atypIPv4), b[:]...)
		} else {
			b := ip.As16()
			req = append(append(req, atypIPv6), b[:]...)
		}
	} else {
		if len(host) > 255 {
			return nil, errors.New("hostname longer than 255 bytes")
		}
		req = append(append(req, atypDomain, byte(len(host))), host...)
	}
	return binary.BigEndian.AppendUint16(req, uint16(port)), nil
}

// socks5ReadReply reads the CONNECT reply and discards the bound address.
func socks5ReadReply(c net.Conn) error {
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return fmt.Errorf("read CONNECT reply: %w", err)
	}
	if hdr[0] != socksVer {
		return fmt.Errorf("bad SOCKS version %d", hdr[0])
	}
	if hdr[1] != 0x00 {
		return fmt.Errorf("CONNECT refused, reply %#x", hdr[1])
	}
	var n int
	switch hdr[3] {
	case atypIPv4:
		n = 4
	case atypIPv6:
		n = 16
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return err
		}
		n = int(l[0])
	default:
		return fmt.Errorf("bad reply address type %#x", hdr[3])
	}
	_, err := io.ReadFull(c, make([]byte, n+2))
	return err
}
