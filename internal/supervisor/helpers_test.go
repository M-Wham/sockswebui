package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// rigSlot is a slot config with route [wgA] on 127.0.0.1:0 probing the echo.
func rigSlot(id int, route ...string) config.Slot {
	return config.Slot{
		ID: id, Name: "slot" + strconv.Itoa(id), Enabled: true,
		BindAddr: "127.0.0.1", Port: 0,
		Route:      route,
		Killswitch: config.KillswitchBlock,
		Health:     config.Health{IntervalS: 1, URL: "http://" + testrig.EchoHost + "/", FailThreshold: 2},
	}
}

// peerEndpoint starts a testrig peer named id and returns its endpoint.
func peerEndpoint(t *testing.T, id string) config.Endpoint {
	t.Helper()
	p := testrig.NewWGPeer(t, id)
	ep := p.ClientConfig()
	ep.ID = id
	return ep
}

// spyChain wraps a chain and records Close.
type spyChain struct {
	egress.Chain
	closed chan struct{}
	once   sync.Once
}

func (c *spyChain) Close() error {
	c.once.Do(func() { close(c.closed) })
	if c.Chain == nil {
		return nil
	}
	return c.Chain.Close()
}

// loopbackDirectBuilder is the anti-vacuity test builder (injected through the
// slot's builder test seam): the
// empty route (direct / allow-direct fallback) gets testrig.LoopbackDirect,
// so it still reaches the loopback sentinel once egress denies loopback;
// any other route uses the real egress.Build. Every test asserting the
// sentinel saw nothing, or needing the direct chain to reach loopback, must
// opt in, or a fail-open gate would show zero sentinel traffic for the
// wrong reason.
func loopbackDirectBuilder(route []string, eps []config.Endpoint, blockLAN bool) (egress.Chain, error) {
	if len(route) == 0 {
		return testrig.LoopbackDirect(blockLAN), nil
	}
	return egress.Build(route, eps)
}

// builderSpy records every Build call and wraps inner (default egress.Build).
type builderSpy struct {
	mu     sync.Mutex
	routes [][]string // nil entry = called with nil route
	chains []*spyChain
	fail   error       // when set, Build returns it instead
	inner  builderFunc // nil = buildEgress
}

func (b *builderSpy) build(route []string, eps []config.Endpoint, blockLAN bool) (egress.Chain, error) {
	b.mu.Lock()
	b.routes = append(b.routes, route)
	b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	inner := b.inner
	if inner == nil {
		inner = buildEgress
	}
	ch, err := inner(route, eps, blockLAN)
	if err != nil {
		return nil, err
	}
	sc := &spyChain{Chain: ch, closed: make(chan struct{})}
	b.mu.Lock()
	b.chains = append(b.chains, sc)
	b.mu.Unlock()
	return sc, nil
}

func (b *builderSpy) calls() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]string(nil), b.routes...)
}

func (b *builderSpy) chain(t *testing.T, i int) *spyChain {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if i >= len(b.chains) {
		t.Fatalf("chain %d not built (have %d)", i, len(b.chains))
	}
	return b.chains[i]
}

// logBuf is a concurrency-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newLogger() (*slog.Logger, *logBuf) {
	lb := &logBuf{}
	return slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug})), lb
}

// newTestSlot creates a slot, subscribes to its events and stops it at cleanup.
func newTestSlot(t *testing.T, cfg config.Slot, eps []config.Endpoint, opts ...option) (*Slot, <-chan Event, *logBuf) {
	t.Helper()
	bus := NewBus()
	ch, cancel := bus.Subscribe(64)
	t.Cleanup(cancel)
	log, lb := newLogger()
	s := NewSlot(cfg, eps, bus, log, opts...)
	t.Cleanup(func() { _ = s.Stop() })
	return s, ch, lb
}

// waitState reads events until one with state want arrives, returning the
// states seen (including want). Fails after timeout.
func waitState(t *testing.T, ch <-chan Event, want State, timeout time.Duration) ([]State, Event) {
	t.Helper()
	var seen []State
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed; seen %v, want %s", seen, want)
			}
			if e.Type != EventState {
				continue
			}
			seen = append(seen, e.State)
			if e.State == want {
				return seen, e
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s; seen %v", want, seen)
		}
	}
}

// socksConnect performs a no-auth SOCKS5 CONNECT to target via addr and
// returns the reply code and the connection (closed at cleanup).
func socksConnect(t *testing.T, addr, target string) (byte, net.Conn) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial slot: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	var g [2]byte
	if _, err := io.ReadFull(c, g[:]); err != nil || g != [2]byte{5, 0} {
		t.Fatalf("greeting reply %v: %v", g, err)
	}
	host, portS, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portS)
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("request: %v", err)
	}
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		t.Fatalf("connect reply: %v", err)
	}
	return rep[1], c
}

// socksEcho GETs the echo through the slot's SOCKS listener.
func socksEcho(t *testing.T, addr string) map[string]string {
	t.Helper()
	rep, c := socksConnect(t, addr, testrig.EchoHost+":80")
	if rep != 0 {
		t.Fatalf("CONNECT reply = %#x, want 0x00", rep)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+testrig.EchoHost+"/", nil)
	req.Close = true
	if err := req.Write(c); err != nil {
		t.Fatalf("write GET: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	return got
}

// failProber never succeeds.
func failProber(context.Context, Dialer, string, time.Duration) (Result, error) {
	return Result{}, errors.New("stub: probe failed")
}

// assertClosed checks nothing accepts on addr any more.
func assertClosed(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = c.Close()
		t.Fatalf("listener %s still accepting", addr)
	}
}

// socksTry is a non-fatal SOCKS5 CONNECT for use from goroutines, bounded
// by timeout. On reply 0x00 the caller owns the returned conn; otherwise it
// is closed. (A CONNECT to host loopback through a tunnel never completes,
// so callers probing the sentinel via the chain use a short timeout.)
func socksTry(addr, target string, timeout time.Duration) (byte, net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return 0xff, nil, err
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	fail := func(err error) (byte, net.Conn, error) { _ = c.Close(); return 0xff, nil, err }
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	var g [2]byte
	if _, err := io.ReadFull(c, g[:]); err != nil {
		return fail(err)
	}
	host, portS, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portS)
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		return fail(err)
	}
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return fail(err)
	}
	if rep[1] != 0 {
		_ = c.Close()
		return rep[1], nil, nil
	}
	return 0, c, nil
}

// sentinelHammer keeps CONNECTing to target through the slot at addr and
// writes a payload on every success, until the returned stop func is called.
// It returns how many CONNECTs succeeded.
func sentinelHammer(addr, target string) (stop func() int) {
	done := make(chan struct{})
	var ok atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if rep, c, err := socksTry(addr, target, 500*time.Millisecond); err == nil && rep == 0 {
				ok.Add(1)
				_, _ = c.Write([]byte("leak"))
				_ = c.Close()
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	return func() int { close(done); wg.Wait(); return int(ok.Load()) }
}

// trySend CONNECTs to target via the slot (bounded) and writes p on success.
func trySend(addr, target string, p []byte) {
	if rep, c, err := socksTry(addr, target, time.Second); err == nil && rep == 0 {
		_, _ = c.Write(p)
		_ = c.Close()
	}
}

// gateListener kills an upstream proxy hop on demand: while down, every
// accepted connection is closed at once, as if the proxy were unreachable.
// Connections accepted earlier are left alone; the kill switch must close them.
type gateListener struct {
	net.Listener
	down atomic.Bool
}

func (g *gateListener) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil || !g.down.Load() {
			return c, err
		}
		_ = c.Close()
	}
}
