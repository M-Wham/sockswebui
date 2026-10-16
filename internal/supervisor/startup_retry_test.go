package supervisor

// While STARTING, a failed probe is retried after 2, 4, 8 s (capped at the
// health interval); once UP the normal interval applies. On real hardware
// [WG, niceproxy] needed ~46 s to reach UP because a slow first probe was followed by a full 30 s interval.

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// waitRecorder fires the first n waits at once and records every
// requested duration; later waits never fire, so the loop parks until stop.
type waitRecorder struct {
	mu    sync.Mutex
	n     int
	waits []time.Duration
}

func (r *waitRecorder) after(d time.Duration) <-chan time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waits = append(r.waits, d)
	c := make(chan time.Time, 1)
	if len(r.waits) <= r.n {
		c <- time.Now()
	}
	return c
}

func (r *waitRecorder) got() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.waits...)
}

// scriptProber returns the scripted outcomes in order (true = ok), then ok.
func scriptProber(script ...bool) (proberFunc, *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context, Dialer, string, time.Duration) (Result, error) {
		i := int(calls.Add(1)) - 1
		if i < len(script) && !script[i] {
			return Result{}, errors.New("health: probe: context deadline exceeded")
		}
		return Result{ExitIP: "198.51.100.7"}, nil
	}, &calls
}

func secs(v ...int) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, s := range v {
		out[i] = time.Duration(s) * time.Second
	}
	return out
}

func TestStartingRetryCadence(t *testing.T) {
	cases := []struct {
		name     string
		interval int
		script   []bool
		want     []time.Duration
	}{
		// 4 failures while STARTING, UP, one failure while UP (below the
		// threshold of 2), ok: UP waits stay at the interval.
		{"interval 30", 30, []bool{false, false, false, false, true, false, true}, secs(2, 4, 8, 8, 30, 30, 30)},
		{"capped at interval", 3, []bool{false, false, false, true}, secs(2, 3, 3, 3)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := peerEndpoint(t, "wgA")
			cfg := rigSlot(1, "wgA")
			cfg.Health.IntervalS = tc.interval
			prober, calls := scriptProber(tc.script...)
			rec := &waitRecorder{n: len(tc.want) - 1}
			s, ev, _ := newTestSlot(t, cfg, []config.Endpoint{ep}, withProber(prober), withAfter(rec.after))
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			waitState(t, ev, StateUp, 5*time.Second)
			waitFor(t, "all waits", 5*time.Second, func() bool { return len(rec.got()) >= len(tc.want) })
			if got := rec.got(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("waits = %v, want %v (probes %d)", got, tc.want, calls.Load())
			}
			if st, _ := s.State(); st != StateUp {
				t.Fatalf("state = %s, want UP (one failure is below the threshold)", st)
			}
		})
	}
}

// TestStartingRetryResetsOnRestart: a slot restarted after a long STARTING
// episode retries at 2 s again, not where the last episode stopped.
func TestStartingRetryResetsOnRestart(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	cfg := rigSlot(1, "wgA")
	cfg.Health.IntervalS = 30
	fail := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		return Result{}, errors.New("health: probe: refused")
	}
	rec := &waitRecorder{n: 3}
	s, ev, _ := newTestSlot(t, cfg, []config.Endpoint{ep}, withProber(fail), withAfter(rec.after))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first episode waits", 5*time.Second, func() bool { return len(rec.got()) >= 4 })
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	waitState(t, ev, StateStopped, 5*time.Second)
	if st, _ := s.State(); st == StateUp {
		t.Fatal("slot went UP without a successful probe")
	}
	rec.mu.Lock()
	rec.waits, rec.n = nil, 0
	rec.mu.Unlock()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second episode wait", 5*time.Second, func() bool { return len(rec.got()) >= 1 })
	if got := rec.got()[0]; got != 2*time.Second {
		t.Fatalf("first retry after restart = %v, want 2s", got)
	}
}

// TestWGSocksUpOnFirstProbe: a [WG, SOCKS5] slot with the real prober and
// the default 30 s interval reaches UP on its first probe.
func TestWGSocksUpOnFirstProbe(t *testing.T) {
	p := testrig.NewWGPeer(t, "wgA")
	ln, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), 1080))
	if err != nil {
		t.Fatal(err)
	}
	up := testrig.NewSOCKS5Upstream(ln, testrig.UpstreamConfig{
		Name: "socksB", Dial: p.Net().DialContext, AllowNoAuth: true,
		Hosts: map[string]string{testrig.EchoHost: p.TunnelIP().String()},
	})
	t.Cleanup(func() { _ = up.Close() })
	wg := p.ClientConfig()
	wg.ID = "wgA"
	sk := config.Endpoint{ID: "socksB", Name: "socksB", Kind: config.KindSOCKS5, Host: p.TunnelIP().String(), Port: 1080}
	cfg := rigSlot(1, "wgA", "socksB")
	cfg.Health.IntervalS = 30
	var probes atomic.Int32
	prober := func(ctx context.Context, d Dialer, url string, to time.Duration) (Result, error) {
		probes.Add(1)
		return Probe(ctx, d, url, to)
	}
	s, ev, _ := newTestSlot(t, cfg, []config.Endpoint{wg, sk}, withProber(prober))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, ev, StateUp, 5*time.Second)
	if n := probes.Load(); n != 1 {
		t.Fatalf("UP after %d probes, want 1", n)
	}
}
