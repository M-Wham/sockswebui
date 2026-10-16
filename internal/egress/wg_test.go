package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// recordingBind wraps the real OS bind and records the ports it opened and
// whether it is currently open.
type recordingBind struct {
	conn.Bind
	mu    sync.Mutex
	ports []uint16
	open  bool
}

func (b *recordingBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, p, err := b.Bind.Open(port)
	if err == nil {
		b.mu.Lock()
		b.ports, b.open = append(b.ports, p), true
		b.mu.Unlock()
	}
	return fns, p, err
}

func (b *recordingBind) Close() error {
	b.mu.Lock()
	b.open = false
	b.mu.Unlock()
	return b.Bind.Close()
}

// recordBinds swaps the OS bind constructor for one that records every bind.
// Not parallel-safe: it swaps package state.
func recordBinds(t *testing.T) *[]*recordingBind {
	t.Helper()
	orig := newOSBind
	var mu sync.Mutex
	var binds []*recordingBind
	newOSBind = func() conn.Bind {
		b := &recordingBind{Bind: orig()}
		mu.Lock()
		binds = append(binds, b)
		mu.Unlock()
		return b
	}
	t.Cleanup(func() { newOSBind = orig })
	return &binds
}

func waitGoroutines(t *testing.T, max int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= max {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines = %d, want <= %d\n%s", n, max, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWGClose(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		r := newWGRig(t)
		checkClose(t, []string{"wgA"}, r.eps, "wgA")
	})
	t.Run("nested", func(t *testing.T) {
		r := newNestedRig(t)
		checkClose(t, []string{"wgA", "wgB"}, r.eps, "wgB")
	})
}

// checkClose builds route, proves it with an echo, closes it and asserts the
// tunnels closed innermost first, the one OS bind is released, dials are
// refused and goroutines return to baseline.
func checkClose(t *testing.T, route []string, eps []config.Endpoint, wantHop string) {
	t.Helper()
	binds := recordBinds(t)
	var closed []string
	origHook := onTunnelClose
	onTunnelClose = func(id string) { closed = append(closed, id) }
	t.Cleanup(func() { onTunnelClose = origHook })
	before := runtime.NumGoroutine()

	ch, err := Build(route, eps)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := getEcho(t, ch); got["hop"] != wantHop {
		t.Fatalf("echo = %v, want hop=%s", got, wantHop)
	}
	if len(*binds) != 1 {
		t.Fatalf("OS binds = %d, want exactly 1 (first WG hop only)", len(*binds))
	}
	b := (*binds)[0]
	if err := ch.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	want := make([]string, 0, len(route))
	for i := len(route) - 1; i >= 0; i-- {
		want = append(want, route[i])
	}
	if strings.Join(closed, ",") != strings.Join(want, ",") {
		t.Fatalf("tunnels closed in order %v, want %v (innermost first, once)", closed, want)
	}

	b.mu.Lock()
	open, ports := b.open, append([]uint16(nil), b.ports...)
	b.mu.Unlock()
	if open || len(ports) == 0 {
		t.Fatalf("OS bind open=%v ports=%v after Close, want closed", open, ports)
	}
	last := ports[len(ports)-1]
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(last)})
	if err != nil {
		t.Fatalf("WG UDP port %d not released: %v", last, err)
	}
	_ = uc.Close()

	if c, err := ch.DialContext(context.Background(), "tcp", "10.64.0.1:80"); err == nil {
		_ = c.Close()
		t.Fatal("DialContext after Close succeeded")
	} else if !errors.Is(err, ErrClosed) {
		t.Fatalf("DialContext after Close: %v, want ErrClosed", err)
	}
	waitGoroutines(t, before+2)
}

func TestWGEndpointNotIP(t *testing.T) {
	binds := recordBinds(t)
	lookups := 0
	origLookup := deviceLookup
	deviceLookup = func(context.Context, string) ([]netip.Addr, error) {
		lookups++
		return nil, errors.New("blocked")
	}
	t.Cleanup(func() { deviceLookup = origLookup })

	r := newWGRig(t)
	ep := r.eps[0]
	ep.Endpoint = "wg.rig.invalid:" + strings.Split(ep.Endpoint, ":")[1]
	ch, err := Build([]string{"wgA"}, []config.Endpoint{ep})
	if err == nil {
		_ = ch.Close()
		t.Fatal("Build with a hostname WG endpoint succeeded, want error (fail closed)")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("error %q should name the endpoint field", err)
	}
	if lookups != 0 || len(*binds) != 0 {
		t.Fatalf("device lookups=%d OS binds=%d, want 0/0", lookups, len(*binds))
	}
}

func TestWGBuildRejects(t *testing.T) {
	binds := recordBinds(t)
	r := newWGRig(t)
	sentinel := testrig.NewSentinel(t)
	base := r.eps[0]
	second := base
	second.ID = "wgB"
	for name, mut := range map[string]func(*config.Endpoint){
		"no private key":  func(e *config.Endpoint) { e.PrivateKey = "" },
		"no addresses":    func(e *config.Endpoint) { e.Addresses = nil },
		"bad address":     func(e *config.Endpoint) { e.Addresses = []string{"nope"} },
		"no allowed ips":  func(e *config.Endpoint) { e.AllowedIPs = nil },
		"bad allowed ip":  func(e *config.Endpoint) { e.AllowedIPs = []string{"10.0.0.0/99"} },
		"bad dns":         func(e *config.Endpoint) { e.DNS = []string{"dns.example"} },
		"secret in error": func(e *config.Endpoint) { e.Addresses = []string{e.PrivateKey} },
	} {
		ep := base
		mut(&ep)
		ch, err := Build([]string{"wgA"}, []config.Endpoint{ep})
		if err == nil {
			_ = ch.Close()
			t.Errorf("%s: Build succeeded, want error", name)
			continue
		}
		if strings.Contains(err.Error(), base.PrivateKey) {
			t.Errorf("%s: error leaks the private key", name)
		}
	}
	nested := second
	nested.PrivateKey = ""
	if ch, err := Build([]string{"wgA", "wgB"}, []config.Endpoint{base, nested}); err == nil {
		_ = ch.Close()
		t.Error("nested WG route with an invalid inner hop built")
	}
	if len(*binds) != 0 || sentinel.Conns() != 0 {
		t.Fatalf("OS binds=%d sentinel conns=%d after rejected builds, want 0/0", len(*binds), sentinel.Conns())
	}
}
