package egress

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

// steadyGet times n HTTP GETs over ch after one warm-up request (which pays
// every hop's handshake) and returns their median. Each GET opens a new TCP
// conn and resolves the name through the last WG hop's tunnel DNS.
func steadyGet(t *testing.T, ch Chain, n int) time.Duration {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{DialContext: ch.DialContext, DisableKeepAlives: true},
		Timeout:   10 * time.Second,
	}
	get := func() time.Duration {
		start := time.Now()
		resp, err := client.Get("http://" + echoHost + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
		return time.Since(start)
	}
	get()
	ds := make([]time.Duration, n)
	for i := range ds {
		ds[i] = get()
	}
	slices.Sort(ds)
	return ds[n/2]
}

// TestWGWGNoFixedDelay (regression test): on loopback a [WG, WG] route adds no
// fixed per-request delay. The ~1.3 s seen on real multi-hop routes was path
// RTT (the first hop's location), not the chain. Limits are absolute and
// generous so -race and a busy host do not flake it.
func TestWGWGNoFixedDelay(t *testing.T) {
	const (
		limit  = 200 * time.Millisecond
		margin = 150 * time.Millisecond
	)
	one := steadyGet(t, build(t, []string{"wgA"}, newWGRig(t).eps), 5)
	r := newNestedRig(t)
	two := steadyGet(t, build(t, []string{"wgA", "wgB"}, r.eps), 5)
	t.Logf("steady median: [WG] %v, [WG, WG] %v", one, two)
	if two > limit {
		t.Errorf("[WG, WG] steady median %v > %v: fixed per-request delay", two, limit)
	}
	if two > one+margin {
		t.Errorf("[WG, WG] steady median %v exceeds [WG] %v by more than %v", two, one, margin)
	}
}
