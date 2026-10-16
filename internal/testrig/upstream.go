package testrig

// Shared plumbing for the fake SOCKS5 and HTTP CONNECT upstreams.
//
// Last-hop identification (relied on by the egress and supervisor tests): every loopback upstream
// created with NewLoopbackSOCKS5/NewLoopbackHTTP dials onward from its OWN
// distinct loopback source IP (127.0.0.2, 127.0.0.3, ... via net.Dialer.LocalAddr),
// exposed as SourceIP(). The echo endpoint reports the caller's address as "ip",
// so echo["ip"] == up.SourceIP().String() proves that upstream was the last hop.
// An upstream placed on a WG peer's netstack (via NewSOCKS5Upstream/NewHTTPUpstream
// with the netstack's DialContext) dials from that netstack's address instead.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// DialFunc is the onward dialer an upstream uses to reach its targets.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// UpstreamConfig configures a fake upstream proxy.
type UpstreamConfig struct {
	Name     string            // label for test output
	Username string            // auth required when non-empty
	Password string            // never logged
	Hosts    map[string]string // hostname → IP for domain targets (no device DNS)
	Dial     DialFunc          // onward dialer; set by the loopback helpers

	// AcceptUser, when set, replaces the exact Username match: any username it
	// returns true for passes (e.g. a prefix match, so rendered session IDs are
	// accepted). Password is still compared when non-empty. Unset keeps the
	// exact-match behaviour above.
	AcceptUser func(user string) bool
	// AllowNoAuth, with AcceptUser, also accepts clients that send no credentials.
	AllowNoAuth bool
}

// Credential is one username/password pair an upstream accepted. Test data only:
// it is held in memory and never logged.
type Credential struct {
	User string
	Pass string
}

const onwardDialTimeout = 10 * time.Second

// upstream is the shared listener/connection lifecycle of both fakes.
type upstream struct {
	cfg      UpstreamConfig
	ln       net.Listener
	sourceIP netip.Addr

	mu      sync.Mutex
	targets []string
	creds   []Credential
	conns   map[net.Conn]struct{}
	closed  bool

	wg        sync.WaitGroup
	closeOnce sync.Once
}

func newUpstream(ln net.Listener, cfg UpstreamConfig, handle func(*upstream, net.Conn)) *upstream {
	u := &upstream{cfg: cfg, ln: ln, conns: map[net.Conn]struct{}{}}
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !u.track(c) {
				_ = c.Close()
				return
			}
			u.wg.Add(1)
			go func() {
				defer u.wg.Done()
				defer u.untrack(c)
				_ = c.SetDeadline(time.Now().Add(onwardDialTimeout))
				handle(u, c)
			}()
		}
	}()
	return u
}

func (u *upstream) track(c net.Conn) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return false
	}
	u.conns[c] = struct{}{}
	return true
}

func (u *upstream) untrack(c net.Conn) {
	u.mu.Lock()
	delete(u.conns, c)
	u.mu.Unlock()
	_ = c.Close()
}

func (u *upstream) record(target string) {
	u.mu.Lock()
	u.targets = append(u.targets, target)
	u.mu.Unlock()
}

// authRequired reports whether clients must authenticate.
func (u *upstream) authRequired() bool { return u.cfg.Username != "" || u.cfg.AcceptUser != nil }

// noAuthOK reports whether a client sending no credentials is accepted.
func (u *upstream) noAuthOK() bool {
	return !u.authRequired() || (u.cfg.AcceptUser != nil && u.cfg.AllowNoAuth)
}

// authOK checks credentials (passwords in constant time) and records the pair
// when accepted.
func (u *upstream) authOK(user, pass string) bool {
	var ok bool
	if u.cfg.AcceptUser != nil {
		ok = u.cfg.AcceptUser(user) &&
			(u.cfg.Password == "" || subtle.ConstantTimeCompare([]byte(pass), []byte(u.cfg.Password)) == 1)
	} else {
		uok := subtle.ConstantTimeCompare([]byte(user), []byte(u.cfg.Username))
		pok := subtle.ConstantTimeCompare([]byte(pass), []byte(u.cfg.Password))
		ok = uok&pok == 1
	}
	if ok {
		u.mu.Lock()
		u.creds = append(u.creds, Credential{User: user, Pass: pass})
		u.mu.Unlock()
	}
	return ok
}

// Credentials returns every username/password pair accepted so far, in order.
func (u *upstream) Credentials() []Credential {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Credential(nil), u.creds...)
}

// dialOnward resolves target via the static hosts map and dials it.
func (u *upstream) dialOnward(target string) (net.Conn, error) {
	if u.cfg.Dial == nil {
		return nil, errors.New("testrig: upstream has no onward dialer")
	}
	addr, err := resolveTarget(u.cfg.Hosts, target)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), onwardDialTimeout)
	defer cancel()
	return u.cfg.Dial(ctx, "tcp", addr)
}

// relay copies both ways until either side ends, then closes both.
func (u *upstream) relay(client io.ReadWriter, cc, remote net.Conn) {
	_ = cc.SetDeadline(time.Time{})
	if !u.track(remote) {
		_ = remote.Close()
		return
	}
	defer u.untrack(remote)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, remote); done <- struct{}{} }()
	<-done
	_ = cc.Close()
	_ = remote.Close()
	<-done
}

// Name is the configured label.
func (u *upstream) Name() string { return u.cfg.Name }

// Addr is the listener address clients connect to.
func (u *upstream) Addr() string { return u.ln.Addr().String() }

// SourceIP is the loopback address onward dials originate from (zero for
// upstreams built with an injected dialer).
func (u *upstream) SourceIP() netip.Addr { return u.sourceIP }

// Targets returns every CONNECT target accepted so far, verbatim as received.
func (u *upstream) Targets() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.targets...)
}

// Close stops accepting, closes active connections, and waits for goroutines.
func (u *upstream) Close() error {
	u.closeOnce.Do(func() {
		u.mu.Lock()
		u.closed = true
		for c := range u.conns {
			_ = c.Close()
		}
		u.mu.Unlock()
		_ = u.ln.Close()
		u.wg.Wait()
	})
	return nil
}

var nextSource atomic.Uint32

// allocSourceIP hands out 127.0.0.2 .. 127.0.0.254, then 127.0.1.2 .., so the
// first 253*256 upstreams in a process get distinct source IPs (all of 127/8 is
// loopback on Linux).
func allocSourceIP() netip.Addr {
	n := nextSource.Add(1) - 1
	return netip.AddrFrom4([4]byte{127, 0, byte(n / 253 % 256), byte(2 + n%253)})
}

// listenLoopback returns a 127.0.0.1 listener and a config whose Dial
// originates from a fresh distinct loopback source IP.
func listenLoopback(cfg UpstreamConfig) (net.Listener, UpstreamConfig, netip.Addr, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, cfg, netip.Addr{}, fmt.Errorf("listen %s: %w", cfg.Name, err)
	}
	src := allocSourceIP()
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: src.AsSlice()}, Timeout: onwardDialTimeout}
	cfg.Dial = d.DialContext
	return ln, cfg, src, nil
}
