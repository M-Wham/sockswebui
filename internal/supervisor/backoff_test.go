package supervisor

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
)

// nopChain is a chain that never reaches anything; probes are stubbed.
type nopChain struct{}

func (nopChain) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("nop chain")
}
func (nopChain) Close() error { return nil }

// fakeWait is one pending backoff wait; the test fires it.
type fakeWait struct {
	d    time.Duration
	fire chan time.Time
}

// fakeClock records every backoff wait and fires it only on demand.
type fakeClock struct{ waits chan fakeWait }

func newFakeClock() *fakeClock { return &fakeClock{waits: make(chan fakeWait, 64)} }

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	w := fakeWait{d: d, fire: make(chan time.Time, 1)}
	c.waits <- w
	return w.fire
}

func (c *fakeClock) next(t *testing.T) fakeWait {
	t.Helper()
	select {
	case w := <-c.waits:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("no backoff wait scheduled")
		return fakeWait{}
	}
}

func TestBackoff(t *testing.T) {
	t.Run("sequence", func(t *testing.T) {
		b := newBackoff(DefaultBackoffBase, DefaultBackoffCap)
		want := []int{5, 10, 20, 40, 80, 160, 300, 300, 300}
		for i, w := range want {
			if got := b.Next(); got != time.Duration(w)*time.Second {
				t.Fatalf("attempt %d: wait %v, want %ds", i+1, got, w)
			}
		}
		b.Reset()
		if got := b.Next(); got != 5*time.Second {
			t.Fatalf("after Reset: wait %v, want 5s", got)
		}
	})

	t.Run("slot", testBackoffSlot)
}

// testBackoffSlot drives a slot through UP → DEGRADED → backoff rebuilds →
// UP → DEGRADED with a fake clock, then stops it mid-backoff.
func testBackoffSlot(t *testing.T) {
	before := runtime.NumGoroutine()
	var healthy atomic.Bool
	healthy.Store(true)
	prober := func(ctx context.Context, _ Dialer, _ string, _ time.Duration) (Result, error) {
		if healthy.Load() {
			return Result{ExitIP: "10.64.0.2"}, nil
		}
		return Result{}, errors.New("stub: hop down")
	}
	var builds atomic.Int32
	builder := func([]string, []config.Endpoint, bool) (egress.Chain, error) {
		builds.Add(1)
		return nopChain{}, nil
	}
	clk := newFakeClock()
	s, ev, _ := newTestSlot(t, rigSlot(1, "wgA"), nil,
		withBuilder(builder), withProber(prober),
		withInterval(time.Millisecond), withBackoffAfter(clk.after))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, ev, StateUp, 5*time.Second)

	healthy.Store(false)
	waitState(t, ev, StateDegraded, 5*time.Second)
	want := []int{5, 10, 20, 40, 80, 160, 300, 300}
	for i, w := range want {
		fw := clk.next(t)
		if fw.d != time.Duration(w)*time.Second {
			t.Fatalf("rebuild attempt %d: wait %v, want %ds", i+1, fw.d, w)
		}
		if i == len(want)-1 {
			healthy.Store(true)
		}
		fw.fire <- time.Now()
	}
	waitState(t, ev, StateUp, 5*time.Second)
	if got := builds.Load(); got != int32(1+len(want)) {
		t.Fatalf("Build calls = %d, want %d (initial + one per attempt)", got, 1+len(want))
	}

	healthy.Store(false)
	waitState(t, ev, StateDegraded, 5*time.Second)
	if fw := clk.next(t); fw.d != 5*time.Second {
		t.Fatalf("first wait after UP = %v, want reset to 5s", fw.d)
	}

	// Stop while the slot sits in a backoff wait that never fires.
	done := make(chan struct{})
	go func() { _ = s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop during backoff did not return promptly")
	}
	waitState(t, ev, StateStopped, time.Second)
	assertNoLeak(t, before)
}

// assertNoLeak waits for the goroutine count to fall back to before.
func assertNoLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= before {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: before=%d after=%d\n%s", before, n, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
