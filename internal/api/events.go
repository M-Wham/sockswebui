package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/M-Wham/sockswebui/internal/supervisor"
)

const (
	defaultSSEKeepalive = 15 * time.Second
	sseBuffer           = 64 // per-subscriber events held for a slow client
	maxEventStreams     = 16 // concurrent SSE streams
)

// snapshotSlot is one slot in the snapshot frame: the live fields plus the same
// read-only status object that GET /api/slots returns.
type snapshotSlot struct {
	supervisor.SlotStatus
	Status slotStatusView `json:"status"`
}

type snapshotEvent struct {
	Slots []snapshotSlot `json:"slots"`
}

// handleEvents streams slot events over SSE: an initial
// "snapshot", then "state", "health" and "throughput" frames, with a comment
// keepalive. It returns, releasing its subscription, when the client goes.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Auth already passed (requireAuth); take a slot before subscribing so
	// anonymous requests can never consume one.
	select {
	case s.sseSlots <- struct{}{}:
		defer func() { <-s.sseSlots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "too many event streams")
		return
	}
	rc := http.NewResponseController(w)
	// The UI server's ReadTimeout must not end a long-lived stream; there is no
	// request body left to read. Writers without deadline support are fine to ignore.
	_ = rc.SetReadDeadline(time.Time{})
	sub, cancel := s.ctrl.Subscribe()
	events, done := dropOldest(sub, sseBuffer)
	defer func() { cancel(); <-done }()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	snap := s.snapshotFrame()
	if writeFrame(w, "snapshot", snap) != nil || rc.Flush() != nil {
		return
	}
	tick := time.NewTicker(s.sseKeepalive)
	defer tick.Stop()
	// Capture the session token once; a revoked or expired session ends the stream.
	var tok string
	if c, err := r.Cookie(sessionCookieName); err == nil {
		tok = c.Value
	}
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-events:
			if !ok || !s.sessions.valid(tok) {
				return
			}
			switch e.Type {
			case supervisor.EventState, supervisor.EventHealth, supervisor.EventThroughput:
			default:
				continue
			}
			e.Msg = newSecretRedactor(s.store.Get()).Replace(e.Msg)
			err = writeFrame(w, e.Type, e)
		case <-tick.C:
			if !s.sessions.valid(tok) {
				return
			}
			_, err = io.WriteString(w, ": keepalive\n\n")
		}
		if err != nil || rc.Flush() != nil {
			return
		}
	}
}

// writeFrame writes one SSE frame. JSON never contains a raw newline.
func writeFrame(w io.Writer, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", strings.TrimSpace(event), b)
	return err
}

// dropOldest relays in to a buffered channel of size n; when the reader lags
// and the buffer is full, the oldest buffered event is discarded. The relay
// never blocks; out is closed and done closed once in is closed.
func dropOldest(in <-chan supervisor.Event, n int) (<-chan supervisor.Event, <-chan struct{}) {
	out := make(chan supervisor.Event, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		for e := range in {
			for sent := false; !sent; {
				select {
				case out <- e:
					sent = true
				default:
					select {
					case <-out:
					default:
					}
				}
			}
		}
	}()
	return out, done
}
