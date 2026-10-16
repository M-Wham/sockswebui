package api

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// SlotController runs the slots; *supervisor.Manager satisfies it. Handlers
// depend on this interface so tests can use a fake.
type SlotController interface {
	Start(id int) error
	Stop(id int) error
	// CheckStart reports, read-only, whether sc would conflict with a running
	// slot's WG endpoint (*supervisor.EndpointInUseError).
	CheckStart(sc config.Slot, eps []config.Endpoint) error
	Apply(cfg config.Config) error
	Snapshot() []supervisor.SlotStatus
	Subscribe() (<-chan supervisor.Event, func())
	Logs(id int) []supervisor.LogLine
	// RotatedSlots returns, before Apply, the ids of slots in cfg using
	// endpointID that are STARTING, UP, DEGRADED or ERROR
	// (rebuilt_slots).
	RotatedSlots(cfg config.Config, endpointID string) []int
}

// Server is the HTTP API. All /api/ routes except POST /api/setup and POST /api/login
// require a session cookie; state-changing methods also require X-CSRF-Token.
type Server struct {
	store    *config.Store
	ctrl     SlotController
	log      *slog.Logger
	params   argonParams
	sessions *sessionStore
	// loginLimiter caps login attempts per client IP.
	loginLimiter *rateLimiter
	// rotateLimiter allows one successful rotate per endpoint id per rotateWindow;
	// rotateMu makes peek..record atomic.
	rotateLimiter *rateLimiter
	rotateMu      sync.Mutex
	// startMu serializes slot start (secrets check..Start..persist) with config import
	// (replace..apply) and slot/endpoint PUT (update..apply), so none can land between a
	// start's secrets check and ctrl.Start (fail closed). Order: startMu,
	// then the store lock, then the controller's own lock.
	startMu sync.Mutex
	mux     *http.ServeMux
	// names is the accepted UI name list for the Origin check (WithNameList); the zero
	// value accepts IP literals only.
	names NameList
	// importParsed, if set (tests only), runs after import parsing and before the
	// store update, to interleave a concurrent change deterministically.
	importParsed func()
	// startChecked, if set (tests only), runs in slot start after the unlocked secrets
	// pre-check and before the locked re-check, to land a concurrent import in between.
	startChecked func()
	// startRechecked, if set (tests only), runs in slot start under startMu after the
	// locked re-check and before ctrl.Start, to fire a concurrent slot/endpoint PUT there.
	startRechecked func()
	// agg aggregates repeated failed-login and rate-limit lines per client key.
	agg *logAgg
	// sseKeepalive is the SSE comment keepalive interval.
	sseKeepalive time.Duration
	// sseSlots bounds concurrent event streams (maxEventStreams).
	sseSlots chan struct{}
	// rand feeds session ID generation and export salt/nonce; nil means crypto/rand.Reader.
	rand io.Reader
	// uiBindAddr and uiBindSource are the boot-time UI bind reported read-only by
	// GET /api/settings (WithUIBind).
	uiBindAddr   string
	uiBindSource string
	// appVersion is written into config exports (informational).
	appVersion string
	// now, hostname and freeOSMemory default to time.Now, os.Hostname and
	// debug.FreeOSMemory when nil; tests inject them.
	now          func() time.Time
	hostname     func() (string, error)
	freeOSMemory func()
	// replaceConfig, if set (tests only), replaces store.ReplaceWithBackupFunc in config
	// import so a backup-write failure can be injected.
	replaceConfig func(build func(config.Config) (config.Config, error), beforeCommit func()) (string, error)
}

// New returns the API handler backed by store, driving slots through ctrl.
// log must never receive secrets.
func New(store *config.Store, log *slog.Logger, ctrl SlotController, opts ...Option) http.Handler {
	if ctrl == nil {
		panic("api.New: nil SlotController")
	}
	s := newServer(store, log, defaultParams)
	s.ctrl = ctrl
	for _, o := range opts {
		o(s)
	}
	return s
}

// Close stops the log aggregator's ticker and logs pending summaries. It is idempotent.
func (s *Server) Close() error {
	s.agg.close()
	return nil
}

// Option customises New.
type Option func(*Server)

// WithRandReader replaces the randomness source for session IDs (tests only).
func WithRandReader(r io.Reader) Option { return func(s *Server) { s.rand = r } }

// WithAppVersion sets the app_version written into config exports.
func WithAppVersion(v string) Option { return func(s *Server) { s.appVersion = v } }

// WithUIBind records the UI address actually used at boot and its source ("flag" or
// "setting") for the read-only ui_bind_effective / ui_bind_source settings fields.
func WithUIBind(addr, source string) Option {
	return func(s *Server) { s.uiBindAddr, s.uiBindSource = addr, source }
}

// WithClock replaces time.Now for export timestamps and file names (tests only).
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// WithHostname replaces os.Hostname for export file names (tests only).
func WithHostname(h func() (string, error)) Option { return func(s *Server) { s.hostname = h } }

// WithFreeOSMemory replaces the debug.FreeOSMemory call made after each key derivation
// (tests only).
func WithFreeOSMemory(f func()) Option { return func(s *Server) { s.freeOSMemory = f } }

// WithSSEKeepalive sets the SSE comment keepalive interval (tests only). Values <= 0
// are ignored and the default stays.
func WithSSEKeepalive(d time.Duration) Option {
	return func(s *Server) {
		if d > 0 {
			s.sseKeepalive = d
		}
	}
}

func (s *Server) randReader() io.Reader {
	if s.rand != nil {
		return s.rand
	}
	return rand.Reader
}

// WithRoute registers h at pattern (a ServeMux pattern under /api/) behind the
// normal auth gate: a session cookie always, plus X-CSRF-Token for
// state-changing methods. Release builds pass none; the testrig build uses it
// for the dev-only rig controls.
func WithRoute(pattern string, h http.Handler) Option {
	_, path, _ := strings.Cut(pattern, " ")
	if path == "" {
		path = pattern
	}
	if !strings.HasPrefix(path, "/api/") {
		panic("api.WithRoute: pattern must be under /api/: " + pattern)
	}
	return func(s *Server) { s.mux.Handle(pattern, h) }
}

func newServer(store *config.Store, log *slog.Logger, p argonParams) *Server {
	s := &Server{store: store, log: log, params: p, sessions: newSessionStore(),
		loginLimiter: newRateLimiter(loginLimit, loginWindow, loginMaxClients), mux: http.NewServeMux(),
		rotateLimiter: newRateLimiter(1, rotateWindow, rotateMaxKeys),
		sseKeepalive:  defaultSSEKeepalive,
		sseSlots:      make(chan struct{}, maxEventStreams)}
	s.agg = newLogAgg(log)
	s.mux.HandleFunc("POST /api/setup", s.handleSetup)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.registerEndpoints()
	s.registerSlots()
	s.registerSettings()
	s.mux.HandleFunc("POST /api/config/export", s.handleExportConfig)
	s.mux.HandleFunc("POST /api/config/import", s.handleImportConfig)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	return s
}

// ServeHTTP applies the Origin check, then the auth gate, then dispatches to the mux
// (order: Origin before requireAuth and the limiter).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.originCheck(s.requireAuth(s.mux)).ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
