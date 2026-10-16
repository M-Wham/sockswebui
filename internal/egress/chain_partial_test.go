package egress

import (
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

var errInjectedStart = errors.New("injected: hop start failed")

// failNthStart swaps startWGFn so the nth call fails (closing the bind it was
// handed, as startWG's own error paths do). Not parallel-safe: it swaps
// package state; restored in t.Cleanup.
func failNthStart(t *testing.T, n int) {
	t.Helper()
	orig := startWGFn
	calls := 0
	startWGFn = func(s wgSpec, b conn.Bind) (*wgTunnel, error) {
		calls++
		if calls == n {
			_ = b.Close()
			return nil, errInjectedStart
		}
		return orig(s, b)
	}
	t.Cleanup(func() { startWGFn = orig })
}

// TestBuildPartialFailureCloses (no leaked tunnel on a failed build): when hop 2 of [wgA, wgB]
// fails to start, Build returns the error and hop 1's tunnel is torn down:
// its OS UDP port can be bound again and its goroutines are gone within 1 s.
// A dead peer never fails Build, hence the startWGFn fault seam.
func TestBuildPartialFailureCloses(t *testing.T) {
	r := newNestedRig(t)
	binds := recordBinds(t)
	failNthStart(t, 2)
	base := runtime.NumGoroutine()

	ch, err := Build([]string{"wgA", "wgB"}, r.eps)
	if err == nil {
		_ = ch.Close()
		t.Fatal("Build succeeded although hop 2 failed to start")
	}
	if !errors.Is(err, errInjectedStart) {
		t.Fatalf("Build error = %v, want the injected hop-2 start failure", err)
	}
	if len(*binds) != 1 || len((*binds)[0].ports) != 1 {
		t.Fatalf("OS binds = %d, want exactly hop 1's with one opened port", len(*binds))
	}
	hop1 := (*binds)[0]
	hop1.mu.Lock()
	port, open := hop1.ports[0], hop1.open
	hop1.mu.Unlock()
	if open {
		t.Fatal("hop 1's OS bind is still open after hop 2 failed")
	}

	l, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(port)})
	if err != nil {
		t.Fatalf("hop 1's OS UDP port %d not re-bindable after failed Build: %v", port, err)
	}
	_ = l.Close()

	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines = %d after failed Build, want <= baseline %d within 1s\n%s",
				runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}
