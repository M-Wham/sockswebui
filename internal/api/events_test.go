package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/supervisor"
)

type sseFrame struct{ event, data string }

// readFrame reads one SSE frame; comment-only frames return event ":".
func readFrame(t *testing.T, br *bufio.Reader) sseFrame {
	t.Helper()
	var f sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v (partial %+v)", err, f)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if f.event != "" {
				return f
			}
		case strings.HasPrefix(line, ":"):
			f.event = ":"
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data += strings.TrimPrefix(line, "data: ")
		}
	}
}

// nextEvent skips keepalive comments.
func nextEvent(t *testing.T, br *bufio.Reader) sseFrame {
	t.Helper()
	for {
		if f := readFrame(t, br); f.event != ":" {
			return f
		}
	}
}

func waitSubscribers(t *testing.T, bus *supervisor.Bus, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for bus.Subscribers() != want {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d, want %d", bus.Subscribers(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEventsSSE(t *testing.T) {
	a := newAuthed(t)
	_, priv := slotFixture(t, a, 1, false)
	a.ctrl.states[1] = supervisor.StateUp
	a.srv.sseKeepalive = 30 * time.Millisecond
	ts := httptest.NewServer(a.h)
	defer ts.Close()

	if rec := a.do("GET", "/api/events", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d, want 401", rec.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.AddCookie(a.cookie)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content-type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(res.Body)
	snap := nextEvent(t, br)
	var sb struct {
		Slots []supervisor.SlotStatus `json:"slots"`
	}
	if err := json.Unmarshal([]byte(snap.data), &sb); snap.event != "snapshot" || err != nil ||
		len(sb.Slots) != 1 || sb.Slots[0].State != supervisor.StateUp {
		t.Fatalf("snapshot = %+v (%v)", snap, err)
	}
	waitSubscribers(t, a.ctrl.bus, 1)

	now := time.Now()
	a.ctrl.bus.Publish(supervisor.Event{Type: supervisor.EventState, Slot: 1, State: supervisor.StateDegraded,
		Msg: "kill switch: " + testSocksPass + " " + priv, TS: now})
	a.ctrl.bus.Publish(supervisor.Event{Type: supervisor.EventHealth, Slot: 1, TS: now,
		Health: &supervisor.Health{OK: true, ExitIP: "198.51.100.7", Country: "Sweden", LatencyMS: 40}})
	a.ctrl.bus.Publish(supervisor.Event{Type: supervisor.EventThroughput, Slot: 1, TS: now,
		Throughput: &supervisor.Throughput{BytesIn: 10, BytesOut: 20, RateIn: 1, RateOut: 2}})

	want := []string{"state", "health", "throughput"}
	for _, w := range want {
		f := nextEvent(t, br)
		if f.event != w {
			t.Fatalf("frame %+v, want event %s", f, w)
		}
		var e supervisor.Event
		if err := json.Unmarshal([]byte(f.data), &e); err != nil || e.Slot != 1 {
			t.Fatalf("frame data %q: %v", f.data, err)
		}
		if strings.Contains(f.data, testSocksPass) || strings.Contains(f.data, priv) {
			t.Fatalf("frame leaks a secret: %s", f.data)
		}
		if w == "health" && (e.Health == nil || e.Health.ExitIP != "198.51.100.7" || e.Health.LatencyMS != 40) {
			t.Fatalf("health frame = %s", f.data)
		}
	}
	if f := readFrame(t, br); f.event != ":" {
		t.Fatalf("want keepalive comment, got %+v", f)
	}

	cancel()
	waitSubscribers(t, a.ctrl.bus, 0)
}

// A full subscriber buffer drops the oldest events, never blocks the bus.
func TestEventsSSEDropOldest(t *testing.T) {
	in := make(chan supervisor.Event, 100)
	for i := 1; i <= 10; i++ {
		in <- supervisor.Event{Slot: i}
	}
	close(in)
	out, done := dropOldest(in, 3)
	<-done
	var got []int
	for e := range out {
		got = append(got, e.Slot)
	}
	if len(got) != 3 || got[0] != 8 || got[2] != 10 {
		t.Fatalf("kept %v, want [8 9 10]", got)
	}
}

// A server ReadTimeout must not end an open SSE stream: the handler clears
// the connection read deadline, so a keepalive still arrives after the timeout.
func TestEventsSSEOutlivesReadTimeout(t *testing.T) {
	a := newAuthed(t)
	a.srv.sseKeepalive = 600 * time.Millisecond
	ts := httptest.NewUnstartedServer(a.h)
	ts.Config.ReadTimeout = 150 * time.Millisecond
	ts.Start()
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/api/events", nil)
	req.AddCookie(a.cookie)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	if f := readFrame(t, br); f.event != "snapshot" {
		t.Fatalf("first frame %+v", f)
	}
	if f := readFrame(t, br); f.event != ":" {
		t.Fatalf("want keepalive after the read timeout, got %+v", f)
	}
}

// deadlineRecorder records SetReadDeadline calls made via http.ResponseController.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	cleared chan struct{}
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		select {
		case d.cleared <- struct{}{}:
		default:
		}
	}
	return nil
}

// The events handler clears the read deadline itself rather than relying on net/http
// internals, so the ReadTimeout never ends a stream.
func TestEventsSSEClearsReadDeadline(t *testing.T) {
	a := newAuthed(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, "GET", "/api/events", nil)
	req.AddCookie(a.cookie)
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), cleared: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() { defer close(done); a.h.ServeHTTP(w, req) }()
	select {
	case <-w.cleared:
	case <-time.After(2 * time.Second):
		t.Error("events handler did not clear the read deadline")
	}
	cancel()
	<-done
}
