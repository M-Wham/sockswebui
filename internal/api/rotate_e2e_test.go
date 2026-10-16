package api

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// slotConnect does a no-auth SOCKS5 CONNECT to the testrig echo host via addr.
func slotConnect(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial slot: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = c.Write([]byte{5, 1, 0})
	var g [2]byte
	if _, err := io.ReadFull(c, g[:]); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	host := testrig.EchoHost
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, 80)
	_, _ = c.Write(req)
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil || rep[1] != 0 {
		t.Fatalf("connect reply %v: %v", rep, err)
	}
}

func TestRotateEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := testrig.ServeEcho(ln, "sx")
	t.Cleanup(func() { _ = ln.Close(); <-done })
	up := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{
		Name:       "sx",
		AcceptUser: func(u string) bool { return strings.HasPrefix(u, "acct-sid-") },
		Hosts:      map[string]string{testrig.EchoHost: ln.Addr().String()},
	})
	h, p, _ := net.SplitHostPort(up.Addr())
	port, _ := strconv.Atoi(p)

	e := newTestEnv(t)
	e.setup("hunter22-long")
	err = e.store.Update(func(c *config.Config) error {
		c.Endpoints = []config.Endpoint{{ID: "ep-sx", Name: "sx", Kind: config.KindSOCKS5, Host: h, Port: port,
			Username: "acct-sid-" + config.SessionToken, Password: "pw",
			Session: config.Session{Charset: config.SessionAlnum, Length: 10, CurrentID: "AAAAAAAAAA"}}}
		c.Slots = []config.Slot{{ID: 1, Name: "s1", Enabled: true, BindAddr: "127.0.0.1", Port: freePort(t),
			Route: []string{"ep-sx"}, Killswitch: config.KillswitchBlock,
			Health: config.Health{IntervalS: 1, URL: "http://" + testrig.EchoHost + "/", FailThreshold: 2}}}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	mgr := supervisor.New(supervisor.Options{Config: e.store.Get(), Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
	t.Cleanup(mgr.Close)
	e.srv.ctrl = mgr
	if err := mgr.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var addr string
	waitFor(t, func() bool {
		for _, s := range mgr.Snapshot() {
			if s.ID == 1 && s.State == supervisor.StateUp {
				addr = s.Addr
				return true
			}
		}
		return false
	})
	slotConnect(t, addr)
	creds := up.Credentials()
	if got := creds[len(creds)-1].User; got != "acct-sid-AAAAAAAAAA" {
		t.Fatalf("pre-rotate user %q", got)
	}

	cookie, csrf := e.login("hunter22-long")
	rec := e.do("POST", "/api/endpoints/ep-sx/rotate", "", withCookie(cookie), withCSRF(csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
	}
	newID := e.store.Get().Endpoints[0].Session.CurrentID
	if newID == "AAAAAAAAAA" || !strings.Contains(rec.Body.String(), newID) || !strings.Contains(rec.Body.String(), `"rebuilt_slots":[1]`) {
		t.Fatalf("body %s stored %q", rec.Body, newID)
	}
	waitFor(t, func() bool {
		for _, s := range mgr.Snapshot() {
			if s.ID == 1 && s.State == supervisor.StateUp {
				return true
			}
		}
		return false
	})
	slotConnect(t, addr)
	creds = up.Credentials()
	if got := creds[len(creds)-1].User; got != "acct-sid-"+newID {
		t.Fatalf("post-rotate user %q, want id %q", got, newID)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for condition")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
