package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/socks"
)

// ErrNotUp is returned by a slot's gate dialer whenever the slot is not UP.
// The SOCKS server maps it to reply 0x01 (fail closed).
var ErrNotUp = errors.New("supervisor: slot is not up")

// ErrRunning is returned by Start on a slot that is already running.
var ErrRunning = errors.New("supervisor: slot already running")

// Test-hook stages inside the slot goroutine.
const (
	stageListen = "listen"
	stageBuild  = "build"
	stageProbe  = "probe"
)

type (
	builderFunc func(route []string, eps []config.Endpoint, blockLAN bool) (egress.Chain, error)
	proberFunc  func(ctx context.Context, d Dialer, url string, timeout time.Duration) (Result, error)
	afterFunc   func(time.Duration) <-chan time.Time
)

// option overrides a Slot dependency; tests only.
type option func(*Slot)

func withBuilder(b builderFunc) option { return func(s *Slot) { s.build = b } }
func withProber(p proberFunc) option   { return func(s *Slot) { s.probe = p } }
func withAfter(a afterFunc) option     { return func(s *Slot) { s.after = a } }

// withInterval overrides the probe interval (and so the probe timeout).
func withInterval(d time.Duration) option {
	return func(s *Slot) { s.interval, s.timeout = d, min(d, DefaultProbeTimeout) }
}

// withBackoff overrides the rebuild backoff base and cap.
func withBackoff(base, max time.Duration) option {
	return func(s *Slot) { s.boBase, s.boMax = base, max }
}

// withBackoffAfter overrides the clock used for rebuild backoff waits.
func withBackoffAfter(a afterFunc) option { return func(s *Slot) { s.boAfter = a } }

// withRing makes the slot log into ring, so a rebuilt slot keeps the log
// history of the slot it replaces.
func withRing(r *LogRing) option { return func(s *Slot) { s.ring = r } }

func withHook(h func(stage string)) option {
	return func(s *Slot) { s.hook = h }
}

// Slot runs one SOCKS5 listener and its egress chain through the slot
// lifecycle (see State). Its SOCKS server dials only through a gate that consults the
// slot state: UP → chain; DEGRADED → device route only under killswitch
// "allow-direct" (direct.go); anything else → ErrNotUp. Otherwise a direct
// chain exists only when the configured route is explicitly empty.
type Slot struct {
	cfg  config.Slot
	eps  []config.Endpoint
	bus  *Bus
	log  *slog.Logger // redacting; feeds ring and the process logger
	ring *LogRing
	now  func() time.Time
	hook func(stage string)

	build builderFunc
	probe proberFunc
	after afterFunc

	url       string
	interval  time.Duration
	timeout   time.Duration
	threshold int

	boBase, boMax time.Duration
	boAfter       afterFunc

	life sync.Mutex // serializes Start and Stop

	mu        sync.Mutex
	state     State
	msg       string
	since     time.Time    // last state change (status.go)
	lastProbe *ProbeStatus // last health probe (status.go)
	chain     egress.Chain
	conns     map[net.Conn]struct{} // proxied conns handed out by the gate
	rx, tx    atomic.Uint64         // bytes relayed in/out through gate conns (throughput.go)
	srv       *socks.Server
	ln        net.Listener
	cancel    context.CancelFunc
	wg        sync.WaitGroup // run + serve goroutines
}

// NewSlot returns a STOPPED slot for cfg, resolving route hops in eps.
// Transitions are published on bus (may be nil). Everything the slot logs
// is redacted, kept in its LogRing and mirrored to log.
func NewSlot(cfg config.Slot, eps []config.Endpoint, bus *Bus, log *slog.Logger, opts ...option) *Slot {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Slot{
		cfg:   cfg,
		eps:   eps,
		bus:   bus,
		ring:  &LogRing{},
		now:   time.Now,
		hook:  func(string) {},
		build: buildEgress,
		probe: Probe,
		after: time.After,
		state: StateStopped,
		conns: make(map[net.Conn]struct{}),

		url:       cfg.Health.URL,
		interval:  time.Duration(cfg.Health.IntervalS) * time.Second,
		timeout:   TimeoutFor(cfg.Health),
		threshold: cfg.Health.FailThreshold,
		boBase:    DefaultBackoffBase,
		boMax:     DefaultBackoffCap,
		boAfter:   time.After,
	}
	if s.url == "" {
		s.url = config.DefaultHealthURL
	}
	if s.interval <= 0 {
		s.interval = config.DefaultHealthSeconds * time.Second
	}
	if s.threshold <= 0 {
		s.threshold = config.DefaultFailThreshold
	}
	for _, o := range opts {
		o(s)
	}
	s.log = slog.New(newSlotHandler(s.secrets(), s.ring, log.Handler())).With("slot", cfg.ID)
	return s
}

// State returns the current state and its log-safe message.
func (s *Slot) State() (State, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.msg
}

// Addr is the bound SOCKS listener address, or "" when there is none.
func (s *Slot) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Start moves a STOPPED or ERROR slot to STARTING and brings it up in the
// slot goroutine. Failures surface as ERROR events, not as Start errors.
func (s *Slot) Start() error {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	if s.state.running() {
		s.mu.Unlock()
		return ErrRunning
	}
	s.mu.Unlock()
	if err := s.teardown(); err != nil { // leftovers from ERROR
		s.log.Warn("teardown before start failed", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.setLocked(StateStarting, "starting")
	s.wg.Add(1)
	s.mu.Unlock()
	go s.run(ctx)
	return nil
}

// Stop tears the slot down from any state: cancels the slot goroutine,
// closes the SOCKS server (listener and connections) and the chain, then
// moves to STOPPED. Idempotent. The slot is STOPPED even when an error
// (from closing the chain) is returned.
func (s *Slot) Stop() error {
	s.life.Lock()
	defer s.life.Unlock()
	err := s.teardown()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateStopped {
		s.setLocked(StateStopped, "stopped")
	}
	return err
}

// teardown releases everything the slot owns and waits for its goroutines.
// It returns the chain's Close error, if any.
func (s *Slot) teardown() error {
	// Cancel under s.mu before snapshotting: attach checks ctx under s.mu,
	// so nothing can be attached after this snapshot.
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	srv, ln := s.srv, s.ln
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close() // closes ln and every client/upstream conn
	} else if ln != nil {
		_ = ln.Close()
	}
	s.wg.Wait()
	s.mu.Lock()
	ch := s.chain
	s.chain, s.srv, s.ln = nil, nil, nil
	s.mu.Unlock()
	if ch != nil {
		if err := ch.Close(); err != nil {
			return fmt.Errorf("close chain: %w", err)
		}
	}
	return nil
}

// run is the slot goroutine. A panic anywhere in it is recovered into ERROR.
func (s *Slot) run(ctx context.Context) {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("slot goroutine panic recovered", "reason", "panic",
				"panic", s.redact(fmt.Sprint(r)), "stack", string(debug.Stack()))
			s.fail(ctx, "internal error: panic in slot goroutine")
		}
	}()

	s.hook(stageListen)
	addr := net.JoinHostPort(s.cfg.BindAddr, strconv.Itoa(s.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.fail(ctx, "listen: "+err.Error())
		return
	}
	srv, err := s.newServer()
	if err != nil {
		_ = ln.Close()
		s.fail(ctx, "socks: "+err.Error())
		return
	}
	if !s.attach(ctx, func() { s.ln, s.srv = ln, srv }) {
		_ = srv.Close()
		_ = ln.Close()
		return
	}
	s.wg.Add(1)
	go s.serve(ctx, srv, ln)

	s.hook(stageBuild)
	// The route is passed verbatim: never substituted on failure (fail closed).
	ch, err := s.buildRoute(s.cfg.Route)
	if err != nil {
		s.fail(ctx, "build route: "+err.Error())
		return
	}
	if !s.attach(ctx, func() { s.chain = ch }) {
		_ = ch.Close()
		return
	}
	s.probeLoop(ctx, ch)
}

func (s *Slot) toUp(ctx context.Context, res Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.state != StateStarting {
		return
	}
	s.setLocked(StateUp, upMsg("probe ok", res))
}

func upMsg(prefix string, res Result) string {
	if res.ExitIP != "" {
		return prefix + ": exit " + res.ExitIP
	}
	return prefix
}

// serve runs the SOCKS server. A panic or an unexpected exit while the slot
// is running is converted to ERROR instead of killing the process.
func (s *Slot) serve(ctx context.Context, srv *socks.Server, ln net.Listener) {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("socks serve panic recovered", "reason", "panic",
				"panic", s.redact(fmt.Sprint(r)), "stack", string(debug.Stack()))
			s.fail(ctx, "internal error: panic in socks server")
		}
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, socks.ErrServerClosed) {
		s.fail(ctx, "socks serve: "+err.Error())
	}
}

// attach stores resources unless the slot was stopped meanwhile.
func (s *Slot) attach(ctx context.Context, f func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	f()
	return true
}

// fail moves the slot to ERROR (unless it is being stopped), cancels the
// slot context and closes the chain and every proxied conn. Cancelling ends
// the slot goroutine's work: a build still in flight is refused by attach
// and closed, and no probe loop or rebuild runs on in ERROR. The listener
// stays bound so clients get 0x01 until Stop (or Start, which tears down).
func (s *Slot) fail(ctx context.Context, msg string) {
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	if s.cancel != nil {
		s.cancel() // kept set: teardown calls it again (no-op) and still releases srv/ln
	}
	ch, conns := s.chain, s.conns
	s.chain, s.conns = nil, make(map[net.Conn]struct{})
	s.setLocked(StateError, s.redact(msg))
	s.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
	if ch != nil {
		_ = ch.Close()
	}
}

// setLocked records a transition, logs it with its reason and publishes it.
// Callers hold s.mu.
func (s *Slot) setLocked(st State, msg string) {
	from := s.state
	s.state, s.msg = st, msg
	s.noteStateLocked(st)
	lvl := slog.LevelInfo
	if st == StateError {
		lvl = slog.LevelError
	}
	s.log.Log(context.Background(), lvl, "slot state", "from", string(from), "to", string(st), "reason", msg)
	s.bus.Publish(Event{Type: EventState, Slot: s.cfg.ID, State: st, Msg: msg, TS: s.now()})
}

// newServer builds the slot's SOCKS server around the gate dialer.
func (s *Slot) newServer() (*socks.Server, error) {
	opts := socks.Options{Dialer: gate{s}, Logger: s.log, Redact: s.redact}
	if a := s.cfg.SocksAuth; a != nil {
		opts.Username, opts.Password = a.Username, a.Password
	}
	return socks.New(opts)
}
