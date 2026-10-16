package supervisor

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/M-Wham/sockswebui/internal/egress"
)

// Kill switch (policy "block", the fail-closed default). After fail_threshold consecutive
// failed probes while UP the slot goes DEGRADED: the chain is dropped so the
// gate refuses every new CONNECT (SOCKS reply 0x01), every proxied conn is
// closed, and the chain is rebuilt from the same route with exponential
// backoff until a probe through the new chain succeeds (→ UP). Nothing here
// dials the device route; the allow-direct fallback lives in direct.go.

// stageRebuild is the test-hook stage before each backoff rebuild attempt.
const stageRebuild = "rebuild"

// Startup retry, so a slow first probe does not delay UP by a full interval:
// while STARTING a
// failed probe is retried after 2, 4, 8, 8, … s, never longer than the
// health interval. Once UP the interval applies.
const (
	startRetryBase = 2 * time.Second
	startRetryMax  = 8 * time.Second
)

// probeLoop probes through the chain. The first success moves STARTING →
// UP; while UP it probes every interval and fail_threshold consecutive
// failures trip the kill switch. It returns when ctx is cancelled.
func (s *Slot) probeLoop(ctx context.Context, ch egress.Chain) {
	fails := 0
	// Fresh per run, so a restarted slot retries at 2 s again.
	startBo := newBackoff(min(startRetryBase, s.interval), min(startRetryMax, s.interval))
	for {
		wait := s.interval
		s.hook(stageProbe)
		res, err := s.probe(ctx, ch, s.url, s.timeout)
		if ctx.Err() != nil {
			return
		}
		s.noteProbe(res, err)
		s.publishHealth(res, err)
		switch {
		case err == nil:
			fails = 0
			s.toUp(ctx, res)
		case s.isUp():
			fails++
			s.log.Info("health probe failed", "reason", "probe_failed", "fails", fails,
				"threshold", s.threshold, "err", s.redact(err.Error()))
			if fails >= s.threshold {
				fails = 0
				msg := "kill switch: " + strconv.Itoa(s.threshold) + " consecutive probe failures: " + err.Error()
				if ch = s.degrade(ctx, ch, msg); ch == nil {
					return
				}
			}
		default:
			// Not UP (today only STARTING: ERROR cancels the loop, DEGRADED
			// runs inside degrade). No traffic flows until a probe succeeds.
			wait = startBo.Next()
			s.log.Info("health probe failed", "reason", "probe_failed", "retry_in", wait,
				"err", s.redact(err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-s.after(wait):
		}
	}
}

// publishHealth pushes one probe outcome. Only exit IP, country and latency
// are sent; never the URL or error text.
func (s *Slot) publishHealth(res Result, err error) {
	h := &Health{OK: err == nil}
	if err == nil {
		h.ExitIP, h.Country, h.LatencyMS = s.redact(res.ExitIP), s.redact(res.Country), res.Latency.Milliseconds()
	}
	s.bus.Publish(Event{Type: EventHealth, Slot: s.cfg.ID, Health: h, TS: s.now()})
}

func (s *Slot) isUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StateUp
}

// degrade enters DEGRADED, tears down the old chain and every proxied conn,
// then rebuilds until UP. It returns the new chain, or nil once ctx is done.
func (s *Slot) degrade(ctx context.Context, old egress.Chain, msg string) egress.Chain {
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	s.chain = nil // gate refuses from here on
	conns := s.conns
	s.conns = make(map[net.Conn]struct{})
	s.setLocked(StateDegraded, s.redact(msg))
	s.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
	_ = old.Close()
	s.log.Info("kill switch closed proxied connections", "reason", "degraded", "conns", len(conns))
	return s.rebuild(ctx)
}

// rebuild waits the next backoff step, builds a fresh chain from the same
// configured route and probes through it; success installs it (UP),
// failure closes it and backs off further. Returns nil once ctx is done.
func (s *Slot) rebuild(ctx context.Context) egress.Chain {
	bo := newBackoff(s.boBase, s.boMax) // fresh per episode: reset after UP
	for attempt := 1; ; attempt++ {
		wait := bo.Next()
		s.log.Info("rebuild scheduled", "reason", "backoff", "attempt", attempt, "wait", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-s.boAfter(wait):
		}
		s.hook(stageRebuild)
		ch, err := s.buildRoute(s.cfg.Route)
		if err != nil {
			s.log.Info("rebuild failed", "reason", "build_failed", "attempt", attempt, "err", s.redact(err.Error()))
			continue
		}
		res, err := s.probe(ctx, ch, s.url, s.timeout)
		if ctx.Err() != nil {
			_ = ch.Close()
			return nil
		}
		s.noteProbe(res, err)
		if err != nil {
			_ = ch.Close()
			s.log.Info("rebuild probe failed", "reason", "probe_failed", "attempt", attempt, "err", s.redact(err.Error()))
			continue
		}
		if !s.restore(ctx, ch, res) {
			_ = ch.Close()
			return nil
		}
		return ch
	}
}

// restore installs ch and moves DEGRADED → UP. It refuses unless the slot
// is still DEGRADED and not stopping, so an ERROR (e.g. a SOCKS serve panic
// during the rebuild) is never overwritten with UP. Conns held at that point
// are allow-direct fallbacks; they are closed so traffic returns to the
// tunnel instead of lingering on the device route.
func (s *Slot) restore(ctx context.Context, ch egress.Chain, res Result) bool {
	s.mu.Lock()
	if ctx.Err() != nil || s.state != StateDegraded {
		s.mu.Unlock()
		return false
	}
	s.chain = ch
	conns := s.conns
	s.conns = make(map[net.Conn]struct{})
	s.setLocked(StateUp, upMsg("rebuilt, probe ok", res))
	s.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
	if len(conns) > 0 {
		s.log.Info("closed fallback connections", "reason", "up", "conns", len(conns))
	}
	return true
}
