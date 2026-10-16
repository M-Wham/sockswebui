package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/M-Wham/sockswebui/internal/config"
)

func waitStatus(t *testing.T, s *Slot, ok func(SlotDetail) bool) SlotDetail {
	t.Helper()
	deadline := time.Now().Add(waitUp)
	for {
		d := s.Detail()
		if ok(d) {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("detail never matched: %+v", d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Detail reports the last probe and the time of the last state change; a stopped
// slot has neither.
func TestSlotDetail(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	var fail bool
	prober := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		if fail {
			return Result{}, errors.New("probe failed")
		}
		return Result{ExitIP: "198.51.100.7", Country: "Sweden", Latency: 42 * time.Millisecond}, nil
	}
	s, _, _ := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep}, withProber(prober))
	if d := s.Detail(); !d.Since.IsZero() || d.Probe != nil {
		t.Fatalf("new slot detail = %+v, want empty", d)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	d := waitStatus(t, s, func(d SlotDetail) bool { return d.Probe != nil })
	if d.Since.IsZero() || d.Probe.CheckedAt.IsZero() || d.Probe.ExitIP != "198.51.100.7" ||
		d.Probe.Country != "Sweden" || d.Probe.LatencyMS != 42 || d.Probe.Error != "" {
		t.Fatalf("detail = %+v / %+v", d, d.Probe)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if d := s.Detail(); d.Probe != nil || d.Since.IsZero() {
		t.Fatalf("stopped detail = %+v, want no probe and a since", d)
	}
}

// A failed probe clears the exit and keeps a redacted error: the slot's own secrets
// never reach Detail.
func TestSlotDetailFailedProbeRedacted(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	prober := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		return Result{}, errors.New("upstream said hunter2 and " + ep.PrivateKey)
	}
	cfg := rigSlot(1, "wgA")
	cfg.SocksAuth = &config.SocksAuth{Username: "u", Password: "hunter2"}
	s, _, _ := newTestSlot(t, cfg, []config.Endpoint{ep}, withProber(prober))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	d := waitStatus(t, s, func(d SlotDetail) bool { return d.Probe != nil })
	if d.Probe.Error == "" || d.Probe.ExitIP != "" || d.Probe.LatencyMS != 0 {
		t.Fatalf("probe = %+v", d.Probe)
	}
	if strings.Contains(d.Probe.Error, "hunter2") || strings.Contains(d.Probe.Error, ep.PrivateKey) {
		t.Fatalf("probe error leaks a secret: %q", d.Probe.Error)
	}
}

// The session ID and the rendered username are replaced before the length cap, so a cut
// can never leave a prefix of either; the cut also falls on a rune boundary.
func TestNoteProbeRedactsSessionBeforeCap(t *testing.T) {
	const sid = "Zq7Kd9WmRt"
	ep := config.Endpoint{ID: "px", Kind: config.KindHTTP, Host: "203.0.113.5", Port: 3128,
		Username: "cust-" + config.SessionToken, Password: "pw",
		Session: config.Session{Charset: config.SessionAlnum, Length: 10, CurrentID: sid}}
	s := NewSlot(rigSlot(1, "px"), []config.Endpoint{ep}, nil, nil)
	s.noteProbe(Result{}, errors.New(strings.Repeat("x", 288)+" cust-"+sid+" sid "+sid))
	e := s.Detail().Probe.Error
	for _, bad := range []string{"Zq7K", "cust-", sid} {
		if strings.Contains(e, bad) {
			t.Fatalf("error holds %q: %q", bad, e)
		}
	}
	s.noteProbe(Result{}, errors.New("a"+strings.Repeat("é", 200)))
	e = s.Detail().Probe.Error
	if !utf8.ValidString(e) || len(e) > maxProbeErr {
		t.Fatalf("error not cut on a rune boundary: len %d valid %v", len(e), utf8.ValidString(e))
	}
}

// ERROR (via fail) clears the last probe: no stale exit is shown.
func TestStatusErrorClearsProbe(t *testing.T) {
	ep := peerEndpoint(t, "wgA")
	prober := func(context.Context, Dialer, string, time.Duration) (Result, error) {
		return Result{ExitIP: "198.51.100.7", Latency: time.Millisecond}, nil
	}
	s, _, _ := newTestSlot(t, rigSlot(1, "wgA"), []config.Endpoint{ep}, withProber(prober))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, func(d SlotDetail) bool { return d.Probe != nil })
	s.fail(context.Background(), "socks serve: boom")
	if st, _ := s.State(); st != StateError {
		t.Fatalf("state = %v", st)
	}
	if d := s.Detail(); d.Probe != nil {
		t.Fatalf("probe after ERROR = %+v, want nil", d.Probe)
	}
}
