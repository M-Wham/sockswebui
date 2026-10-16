package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// writeSlotConfig stores one enabled direct slot on 127.0.0.1:port. The stored UI bind
// address is elsewhere so validation accepts the slot; the --ui-addr flag decides the
// real UI address. The health URL is local so nothing could leave the host.
func writeSlotConfig(t *testing.T, dir string, port int) {
	t.Helper()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Settings.UIBindAddr = "192.0.2.1:9"
	cfg.Slots = []config.Slot{{
		ID: 1, Name: "s1", Enabled: true, BindAddr: "127.0.0.1", Port: port,
		Route: []string{}, Killswitch: config.KillswitchAllowDirect,
		Health: config.Health{IntervalS: 30, URL: "http://127.0.0.1:1/", FailThreshold: 2},
	}}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// freePort returns a TCP port on 127.0.0.1 that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// When the UI port is taken, run exits 1 quickly and never autostarts a slot.
func TestRunListenFailStartsNoSlot(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	dir := t.TempDir()
	slotPort := freePort(t)
	writeSlotConfig(t, dir, slotPort)

	autostarted := make(chan struct{}, 1)
	onAutostart = func(*supervisor.Manager) { autostarted <- struct{}{} }
	t.Cleanup(func() { onAutostart = nil })

	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), []string{"--data-dir", dir, "--ui-addr", busy.Addr().String()},
			io.Discard, &stderr, testFS())
	}()
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit code = %d, want 1; stderr=%q", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit within 2 s although the UI port is taken")
	}
	select {
	case <-autostarted:
		t.Error("slots were autostarted although the UI listener failed")
	default:
	}
	if strings.Contains(stderr.String(), "to=STARTING") {
		t.Errorf("a slot logged a start: %q", stderr.String())
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(slotPort)))
	if err != nil {
		t.Fatalf("slot port left bound after exit: %v", err)
	}
	ln.Close()
}

// A slot configured on the real UI port loses the race: it ends in ERROR and
// the UI keeps serving.
func TestSlotOnUIPortGoesError(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	writeSlotConfig(t, dir, port)

	mgrc := make(chan *supervisor.Manager, 1)
	onAutostart = func(m *supervisor.Manager) { mgrc <- m }
	t.Cleanup(func() { onAutostart = nil })
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	t.Cleanup(func() { onListen = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	uiAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	go func() {
		done <- run(ctx, []string{"--data-dir", dir, "--ui-addr", uiAddr}, io.Discard, io.Discard, testFS())
	}()
	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
		case <-time.After(10 * time.Second):
			t.Error("run did not return after cancel")
		}
	}()

	var mgr *supervisor.Manager
	select {
	case mgr = <-mgrc:
	case code := <-done:
		t.Fatalf("run exited with %d; the slot stole the UI port", code)
	case <-time.After(5 * time.Second):
		t.Fatal("slots were never autostarted")
	}
	select {
	case addr := <-ready:
		if addr != uiAddr {
			t.Errorf("UI listening on %s, want %s", addr, uiAddr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UI did not start serving")
	}
	deadline := time.Now().Add(5 * time.Second)
	var st supervisor.SlotStatus
	for time.Now().Before(deadline) {
		st = mgr.Snapshot()[0]
		if st.State == supervisor.StateError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.State != supervisor.StateError {
		t.Fatalf("slot state = %q (%s), want error", st.State, st.Msg)
	}
	if res, body := doReq(t, "GET", "http://"+uiAddr+"/", ""); res.StatusCode != 200 || !strings.Contains(body, "sockswebui") {
		t.Errorf("GET /: status %d body %q", res.StatusCode, body)
	}
}
