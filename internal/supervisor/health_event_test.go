package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// A probe result is pushed as a health event carrying exit IP, country and
// latency, never the probe URL.
func TestSlotHealthEvent(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	prober := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		return Result{ExitIP: "198.51.100.7", Country: "Sweden", Latency: 42 * time.Millisecond}, nil
	}
	s, ev, _ := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep}, withProber(prober))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.After(waitUp)
	for {
		select {
		case e := <-ev:
			if e.Type != EventHealth {
				continue
			}
			h := e.Health
			if e.Slot != 1 || h == nil || !h.OK || h.ExitIP != "198.51.100.7" || h.Country != "Sweden" || h.LatencyMS != 42 {
				t.Fatalf("health event = %+v (%+v)", e, h)
			}
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "http") {
				t.Fatalf("health event leaks a URL: %s", b)
			}
			return
		case <-deadline:
			t.Fatal("no health event")
		}
	}
}
