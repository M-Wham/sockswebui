package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// Rotate rate limit: one successful rotate per endpoint per 5 s.
const (
	rotateWindow  = 5 * time.Second
	rotateMaxKeys = 1024
)

var errNoSessionTemplate = errors.New("endpoint has no session template")

type rotateResponse struct {
	CurrentID    string `json:"current_id"`
	RebuiltSlots []int  `json:"rebuilt_slots"`
}

// handleRotateEndpoint implements POST /api/endpoints/{id}/rotate.
// Check order: 404, 409, 429. Only a saved rotate counts against the limit.
func (s *Server) handleRotateEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	// startMu (after rotateMu; nothing takes them in the other order) keeps Apply from
	// interleaving with a slot start, save or import.
	s.startMu.Lock()
	defer s.startMu.Unlock()

	var ep config.Endpoint
	var limited bool
	var retry time.Duration
	var rebuilt []int
	err := s.store.Update(func(c *config.Config) error {
		i := endpointIndex(c.Endpoints, id)
		if i < 0 {
			return errEndpointNotFound
		}
		if c.Endpoints[i].Session == (config.Session{}) {
			return errNoSessionTemplate
		}
		var ok bool
		if ok, retry = s.rotateLimiter.peek(id); !ok {
			limited = true
			return errRotateLimited
		}
		next := c.Endpoints[i]
		prev := next.Session.CurrentID
		next.Session.CurrentID = ""
		sid, err := config.NewSessionID(s.randReader(), next.Session.Charset, next.Session.Length, prev)
		if err != nil {
			return err
		}
		next.Session.CurrentID = sid
		c.Endpoints[i] = next
		ep = next
		// Snapshot immediately before Apply; best-effort (may go stale).
		rebuilt = s.ctrl.RotatedSlots(*c, id)
		return nil
	})
	switch {
	case errors.Is(err, errEndpointNotFound):
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	case errors.Is(err, errNoSessionTemplate):
		writeError(w, http.StatusConflict, "endpoint has no rotating session")
		return
	case limited:
		setRetryAfter(w, retry)
		writeError(w, http.StatusTooManyRequests, "rotated too recently; try again shortly")
		return
	case err != nil:
		s.writeStoreError(w, "rotate endpoint", err, endpointPath(0))
		return
	}
	s.rotateLimiter.record(id)
	if rebuilt == nil {
		rebuilt = []int{}
	}
	for _, sl := range rebuilt {
		s.log.Info("session rotated", "endpoint", ep.Name, "id", ep.Session.CurrentID, "slot", sl, "remote", clientKey(r))
	}
	if len(rebuilt) == 0 { // a rotation with no running slot is still an audited action
		s.log.Info("session rotated", "endpoint", ep.Name, "id", ep.Session.CurrentID, "remote", clientKey(r))
	}
	s.applyConfig("rotate endpoint")
	writeJSON(w, http.StatusOK, rotateResponse{CurrentID: ep.Session.CurrentID, RebuiltSlots: rebuilt})
}

var errRotateLimited = errors.New("rotate rate limited")
