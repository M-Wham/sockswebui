//go:build testrig

package main

import (
	"log/slog"
	"net/http"

	"github.com/M-Wham/sockswebui/internal/api"
)

// rigPauser is the part of the rig WG peer the dev controls drive.
type rigPauser interface {
	Pause()
	Resume()
}

// devRigRoutes are the dev-only rig controls (testrig build only). They sit
// behind the normal admin session + CSRF gate:
//
//	POST /api/dev/rig/pause   drop every WireGuard datagram at the rig peer (hop dies)
//	POST /api/dev/rig/resume  undo pause (hop comes back)
//
// Both are idempotent and answer 204. Used for the SSE transcript and the
// Playwright smoke.
func devRigRoutes(p rigPauser, log *slog.Logger) []api.Option {
	toggle := func(name string, f func()) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			f()
			log.Warn("dev testrig: peer " + name)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return []api.Option{
		api.WithRoute("POST /api/dev/rig/pause", toggle("paused", p.Pause)),
		api.WithRoute("POST /api/dev/rig/resume", toggle("resumed", p.Resume)),
	}
}
