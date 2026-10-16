package testrig

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func prefixUser(p string) func(string) bool {
	return func(u string) bool { return strings.HasPrefix(u, p) }
}

func TestSOCKS5AcceptUserRecordsCredentials(t *testing.T) {
	up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "s", AcceptUser: prefixUser("acct-"), AllowNoAuth: true})

	if m, st := socksHandshake(t, dialTest(t, up.Addr()), "acct-ABC", "any"); m != 2 || st != 0 {
		t.Fatalf("prefixed user: method %d status %d", m, st)
	}
	if m, st := socksHandshake(t, dialTest(t, up.Addr()), "other-ABC", "any"); m != 2 || st == 0 {
		t.Fatalf("foreign user accepted: method %d status %d", m, st)
	}
	if m, _ := socksHandshake(t, dialTest(t, up.Addr()), "", ""); m != 0 {
		t.Fatalf("no-auth with AllowNoAuth: method %d", m)
	}
	got := up.Credentials()
	if len(got) != 1 || got[0] != (Credential{User: "acct-ABC", Pass: "any"}) {
		t.Fatalf("Credentials() = %+v", got)
	}
}

func TestSOCKS5AcceptUserChecksPassword(t *testing.T) {
	up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "s", Password: "pw", AcceptUser: prefixUser("a")})
	if _, st := socksHandshake(t, dialTest(t, up.Addr()), "a1", "nope"); st == 0 {
		t.Fatal("wrong password accepted")
	}
	if m, _ := socksHandshake(t, dialTest(t, up.Addr()), "", ""); m != methodNoAccep {
		t.Fatalf("no-auth without AllowNoAuth: method %d", m)
	}
}

func TestHTTPAcceptUserRecordsCredentials(t *testing.T) {
	port := startLoopbackEcho(t, "e")
	up := NewLoopbackHTTP(t, UpstreamConfig{Name: "h", AcceptUser: prefixUser("acct-"),
		Hosts: map[string]string{"echo": "127.0.0.1"}})
	target := "echo:" + strconv.Itoa(port)
	if r := httpConnect(t, dialTest(t, up.Addr()), target, basic("acct-XYZ", "p:w")); r.StatusCode != 200 {
		t.Fatalf("prefixed user: %d", r.StatusCode)
	}
	if r := httpConnect(t, dialTest(t, up.Addr()), target, basic("x", "p")); r.StatusCode != 407 {
		t.Fatalf("foreign user: %d", r.StatusCode)
	}
	if r := httpConnect(t, dialTest(t, up.Addr()), target, ""); r.StatusCode != 407 {
		t.Fatalf("no auth without AllowNoAuth: %d", r.StatusCode)
	}
	got := up.Credentials()
	if len(got) != 1 || got[0] != (Credential{User: "acct-XYZ", Pass: "p:w"}) {
		t.Fatalf("Credentials() = %+v", got)
	}
}

// Existing exact-match auth also records what it accepted.
func TestExactAuthRecordsCredentials(t *testing.T) {
	up := NewLoopbackSOCKS5(t, UpstreamConfig{Name: "s", Username: "u", Password: "p"})
	if _, st := socksHandshake(t, dialTest(t, up.Addr()), "u", "p"); st != 0 {
		t.Fatalf("status %d", st)
	}
	if got := up.Credentials(); len(got) != 1 || got[0] != (Credential{User: "u", Pass: "p"}) {
		t.Fatalf("Credentials() = %+v", got)
	}
}

// The dev rig SOCKS upstream accepts no-auth (seeded rigSOCKS) and any username
// starting with DevRigSOCKSUserPrefix (Playwright rotate check).
func TestDevRigSOCKSAcceptsPrefix(t *testing.T) {
	r, err := StartDevRig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !strings.HasPrefix(DevRigSOCKSUserPrefix+"x", DevRigSOCKSUserPrefix) || DevRigSOCKSUserPrefix == "" {
		t.Fatal("empty prefix")
	}
	// Reach the upstream over the rig peer's own netstack (it is not on loopback).
	dial := func() net.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := r.Peer().Net().DialContext(ctx, "tcp",
			netip.AddrPortFrom(r.Peer().TunnelIP(), devRigSOCKSPort).String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	if m, st := socksHandshake(t, dial(), DevRigSOCKSUserPrefix+"AbC123", "whatever"); m != 2 || st != 0 {
		t.Fatalf("prefixed user: method %d status %d", m, st)
	}
	if _, st := socksHandshake(t, dial(), "nope", "x"); st == 0 {
		t.Fatal("foreign user accepted")
	}
	if m, _ := socksHandshake(t, dial(), "", ""); m != 0 {
		t.Fatalf("no-auth: method %d", m)
	}
}
