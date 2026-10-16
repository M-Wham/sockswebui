package supervisor

import (
	"runtime"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// TestStartStopNoLeak cycles a [wgA] slot 100 times through the manager and
// checks that every tunnel, listener and goroutine is released (exit
// criterion 4). Most cycles stop mid-start; every 25th waits for UP so the
// full build/probe path is torn down too. The peer stays alive.
func TestStartStopNoLeak(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	sc := rigSlot(1, "wgA")
	sc.Enabled = false
	m, _ := newTestManager(t, config.Config{Endpoints: []config.Endpoint{ep}, Slots: []config.Slot{sc}})
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	// Warm-up cycle so lazily started runtime/netstack goroutines are counted.
	cycle(t, m, true)
	before := runtime.NumGoroutine()

	for i := 0; i < 100; i++ {
		cycle(t, m, i%25 == 0)
	}

	deadline := time.Now().Add(5 * time.Second)
	after := runtime.NumGoroutine()
	for after-before > 5 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	if after-before > 5 {
		buf := make([]byte, 1<<20)
		t.Fatalf("goroutines: before %d, after %d (delta > 5)\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
	if st := status(t, m, 1).State; st != StateStopped {
		t.Fatalf("final state = %s, want STOPPED", st)
	}
}

func cycle(t *testing.T, m *Manager, waitUP bool) {
	t.Helper()
	if err := m.Start(1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if waitUP {
		waitFor(t, "slot UP", waitUp, func() bool { return status(t, m, 1).State == StateUp })
	}
	if err := m.Stop(1); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
