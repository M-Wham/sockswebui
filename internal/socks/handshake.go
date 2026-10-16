package socks

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Protocol constants (RFC 1928, RFC 1929).
const (
	socksVersion   = 0x05
	authVersion    = 0x01
	methodNoAuth   = 0x00
	methodUserPass = 0x02
	methodNone     = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded           = 0x00
	repGeneralFailure      = 0x01
	repCmdNotSupported     = 0x07
	repAddrTypeUnsupported = 0x08
)

// Reason codes used in logs. They never contain secrets.
var (
	errBadVersion     = errors.New("bad_version")
	errNoMethod       = errors.New("no_acceptable_method")
	errBadAuthVersion = errors.New("bad_auth_version")
	errAuthFailed     = errors.New("auth_failed")
	errAuthThrottled  = errors.New("auth_throttled")
	errBadCommand     = errors.New("cmd_not_supported")
	errBadAddrType    = errors.New("atyp_not_supported")
	errBadAddr        = errors.New("bad_address")
)

// credentials holds SHA-256 digests so comparison is constant-time and
// independent of input length.
type credentials struct {
	user, pass [sha256.Size]byte
}

func newCredentials(user, pass string) *credentials {
	return &credentials{user: sha256.Sum256([]byte(user)), pass: sha256.Sum256([]byte(pass))}
}

// checker compares client-supplied credentials.
type checker interface {
	match(user, pass []byte) bool
}

func (c *credentials) match(user, pass []byte) bool {
	u := sha256.Sum256(user)
	p := sha256.Sum256(pass)
	return subtle.ConstantTimeCompare(u[:], c.user[:])&subtle.ConstantTimeCompare(p[:], c.pass[:]) == 1
}

// negotiate performs method selection and, if required, RFC 1929 user/pass
// sub-negotiation. It returns the authenticated username ("" for no-auth).
// Any returned error means the connection must be closed.
// A user/pass handshake from a throttled key is refused without comparing.
func negotiate(rw io.ReadWriter, creds checker, th *throttle, key string) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(rw, hdr[:]); err != nil {
		return "", err
	}
	if hdr[0] != socksVersion {
		return "", errBadVersion
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(rw, methods); err != nil {
		return "", err
	}
	want := byte(methodNoAuth)
	if creds != nil {
		want = methodUserPass
	}
	chosen := byte(methodNone)
	for _, m := range methods {
		if m == want {
			chosen = want
			break
		}
	}
	if _, err := rw.Write([]byte{socksVersion, chosen}); err != nil {
		return "", err
	}
	switch chosen {
	case methodNone:
		return "", errNoMethod
	case methodNoAuth:
		return "", nil
	}
	return authenticate(rw, creds, th, key)
}

func authenticate(rw io.ReadWriter, creds checker, th *throttle, key string) (string, error) {
	var ver [1]byte
	if _, err := io.ReadFull(rw, ver[:]); err != nil {
		return "", err
	}
	if ver[0] != authVersion {
		return "", errBadAuthVersion
	}
	user, err := readLenPrefixed(rw)
	if err != nil {
		return "", err
	}
	pass, err := readLenPrefixed(rw)
	if err != nil {
		return "", err
	}
	at, ok := th.reserve(key)
	if !ok {
		_, _ = rw.Write([]byte{authVersion, 0x01})
		return "", errAuthThrottled // never surface client-supplied bytes (they may be a password)
	}
	if !creds.match(user, pass) {
		_, _ = rw.Write([]byte{authVersion, 0x01})
		return "", errAuthFailed // never surface client-supplied bytes (they may be a password)
	}
	th.refund(key, at)
	if _, err := rw.Write([]byte{authVersion, 0x00}); err != nil {
		return "", err
	}
	return string(user), nil
}

func readLenPrefixed(r io.Reader) ([]byte, error) {
	var l [1]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	b := make([]byte, l[0])
	_, err := io.ReadFull(r, b)
	return b, err
}

// readRequest parses a SOCKS5 request. On a protocol error it returns the REP
// code to send (0 = send nothing, just close). Domain targets are returned
// verbatim and never resolved here.
func readRequest(r io.Reader) (target string, rep byte, err error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", 0, err
	}
	if hdr[0] != socksVersion {
		return "", 0, errBadVersion
	}
	var host string
	switch hdr[3] {
	case atypIPv4, atypIPv6:
		n := net.IPv4len
		if hdr[3] == atypIPv6 {
			n = net.IPv6len
		}
		ip := make(net.IP, n)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", 0, err
		}
		host = ip.String()
	case atypDomain:
		name, err := readLenPrefixed(r)
		if err != nil {
			return "", 0, err
		}
		if len(name) == 0 {
			return "", repGeneralFailure, errBadAddr
		}
		host = string(name)
	default:
		return "", repAddrTypeUnsupported, errBadAddrType
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", 0, err
	}
	if hdr[1] != cmdConnect {
		return "", repCmdNotSupported, errBadCommand
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), 0, nil
}

// writeReply sends a reply with BND.ADDR 0.0.0.0:0 (the chain's local
// address is not meaningful to the client).
func writeReply(w io.Writer, rep byte) error {
	_, err := w.Write([]byte{socksVersion, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// maxLoggedUser bounds how many runes of a client-supplied username are logged.
const maxLoggedUser = 64

// sanitizeUser makes a client-supplied username safe to log: non-printable
// runes and invalid UTF-8 bytes become '?', and it is truncated.
func sanitizeUser(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxLoggedUser {
			b.WriteString("...")
			break
		}
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
