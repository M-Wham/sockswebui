package socks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"time"
)

// Dialer opens outbound connections. The server never dials on its own:
// every outbound connection goes through this injected dialer.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Options configures a Server.
type Options struct {
	Dialer            Dialer        // required
	Username          string        // empty = no auth
	Password          string        // required when Username is set; never logged
	HandshakeTimeout  time.Duration // default 10s; bounds greeting+auth+request
	DialTimeout       time.Duration // default 10s; bounds the CONNECT dial
	Logger            *slog.Logger  // default: discard
	MaxConns          int           // max concurrent client conns; 0 = 256
	MaxConnsPerSource int           // max concurrent client conns per source key; 0 = max(1, MaxConns/2)
	IdleTimeout       time.Duration // close a relay silent in both directions this long; 0 = 15 min
	// Redact scrubs secrets from a dial error before it is logged. nil = the
	// error text is not logged, only its reason (secrets are never logged).
	Redact func(string) string
}

const (
	defaultTimeout  = 10 * time.Second
	defaultMaxConns = 256
	// defaultIdleTimeout closes relays with no payload in either direction
	// for this long.
	defaultIdleTimeout = 15 * time.Minute
	// replyGrace bounds writing the CONNECT reply after the dial returns.
	replyGrace = 5 * time.Second
	// maxAcceptBackoff caps the retry delay after a failed Accept.
	maxAcceptBackoff = time.Second
)

// ErrServerClosed is returned by Serve after Close.
var ErrServerClosed = errors.New("socks: server closed")

// errServePanic is returned by Serve when its accept loop panicked.
var errServePanic = errors.New("socks: internal error: panic in serve loop")

// Server is a SOCKS5 server supporting CONNECT only.
type Server struct {
	dialer           Dialer
	creds            checker   // nil = no auth
	throttle         *throttle // auth failures per source
	handshakeTimeout time.Duration
	dialTimeout      time.Duration
	idleTimeout      time.Duration
	maxConns         int
	perSource        *sourceCounts
	log              *slog.Logger
	redact           func(string) string // nil: dial error text not logged
	agg              *logAgg             // aggregates handshake reject and max_conns lines

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	closed    bool
	listeners map[net.Listener]struct{}
	conns     map[net.Conn]struct{} // clients and upstreams, for Close
	active    int                   // client conns being served
	wg        sync.WaitGroup
}

// New validates opts and returns a Server. A nil Dialer is an error: there is
// no default route to fall back to (fail closed).
func New(opts Options) (*Server, error) {
	if opts.Dialer == nil {
		return nil, errors.New("socks: Dialer is required")
	}
	if opts.Username == "" && opts.Password != "" {
		return nil, errors.New("socks: Password set without Username")
	}
	if opts.Username != "" && opts.Password == "" {
		return nil, errors.New("socks: Username set without Password")
	}
	if len(opts.Username) > 255 || len(opts.Password) > 255 {
		return nil, errors.New("socks: credentials longer than 255 bytes")
	}
	s := &Server{
		dialer:           opts.Dialer,
		handshakeTimeout: opts.HandshakeTimeout,
		dialTimeout:      opts.DialTimeout,
		idleTimeout:      opts.IdleTimeout,
		maxConns:         opts.MaxConns,
		log:              opts.Logger,
		redact:           opts.Redact,
		listeners:        make(map[net.Listener]struct{}),
		conns:            make(map[net.Conn]struct{}),
		throttle:         newThrottle(),
	}
	if opts.Username != "" {
		s.creds = newCredentials(opts.Username, opts.Password)
	}
	if s.handshakeTimeout <= 0 {
		s.handshakeTimeout = defaultTimeout
	}
	if s.dialTimeout <= 0 {
		s.dialTimeout = defaultTimeout
	}
	if s.idleTimeout <= 0 {
		s.idleTimeout = defaultIdleTimeout
	}
	if s.maxConns <= 0 {
		s.maxConns = defaultMaxConns
	}
	s.perSource = newSourceCounts(perSourceCap(s.maxConns, opts.MaxConnsPerSource))
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.agg = newLogAgg(s.log)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// Serve accepts connections on ln until Close is called. It always closes ln.
// A panic in the accept loop is recovered, logged and returned as an error.
func (s *Server) Serve(ln net.Listener) (err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return ErrServerClosed
	}
	s.listeners[ln] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.listeners, ln)
		s.mu.Unlock()
		_ = ln.Close()
	}()
	defer func() {
		if r := recover(); r != nil {
			s.logPanic("serve", r)
			err = errServePanic
		}
	}()

	var backoff time.Duration
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.isClosed() {
				return ErrServerClosed
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			// Transient (EMFILE, ECONNABORTED, ...): back off and retry
			// instead of taking the slot down.
			backoff = min(max(2*backoff, 5*time.Millisecond), maxAcceptBackoff)
			s.log.Warn("socks accept failed; retrying", "reason", "accept_error", "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-s.ctx.Done():
				return ErrServerClosed
			}
			continue
		}
		backoff = 0
		key := sourceKey(c.RemoteAddr().String())
		switch s.track(c, key) {
		case trackClosed:
			_ = c.Close()
			return ErrServerClosed
		case trackFull:
			_ = c.Close()
			if s.agg.allow("max_conns", key) {
				s.log.Warn("socks connection refused", "peer", c.RemoteAddr().String(), "reason", "max_conns")
			}
			continue
		}
		go func() {
			defer s.wg.Done()
			defer s.untrackClient(c, key)
			defer func() {
				if r := recover(); r != nil {
					s.logPanic("connection", r)
				}
			}()
			s.handle(c)
		}()
	}
}

// Close stops all Serve loops, closes every client and upstream connection,
// waits for connection goroutines to exit, then stops the log aggregator and
// flushes its summaries. It is idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		for ln := range s.listeners {
			_ = ln.Close()
		}
		for c := range s.conns {
			_ = c.Close()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.agg.close()
	return nil
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type trackResult int

const (
	trackOK trackResult = iota
	trackClosed
	trackFull
)

// track registers a client connection (with a goroutine slot) unless the
// server is closed, at MaxConns, or key is at its per-source cap. A refused
// conn is closed by the caller before any read or dial.
func (s *Server) track(c net.Conn, key string) trackResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return trackClosed
	}
	if s.active >= s.maxConns || !s.perSource.acquire(key) {
		return trackFull
	}
	s.active++
	s.conns[c] = struct{}{}
	s.wg.Add(1)
	return trackOK
}

// untrackClient releases the global and per-source slots together, so neither can leak.
func (s *Server) untrackClient(c net.Conn, key string) {
	s.mu.Lock()
	s.active--
	s.perSource.release(key)
	s.mu.Unlock()
	s.untrack(c)
}

// trackUpstream registers a dialed connection so Close can tear it down.
func (s *Server) trackUpstream(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	_ = c.Close()
}

func (s *Server) handle(c net.Conn) {
	peer := c.RemoteAddr().String()
	_ = c.SetDeadline(time.Now().Add(s.handshakeTimeout))

	// user is non-empty only after authentication succeeded (the configured
	// name); a failed or throttled login never logs client-supplied bytes (they may be a password).
	key := sourceKey(peer)
	user, err := negotiate(c, s.creds, s.throttle, key)
	if err != nil {
		// Every handshake reject is aggregated per source (an unauthenticated
		// flood must not push kill-switch lines out of the slot ring).
		if why := reason(err); s.agg.allow(why, key) {
			s.log.Info("socks handshake rejected", "peer", peer, "reason", why)
		}
		return
	}
	target, rep, err := readRequest(c)
	if err != nil {
		if rep != 0 {
			_ = writeReply(c, rep)
		}
		// Request rejects are aggregated per source like handshake rejects: on a
		// no-auth slot any client can send them at will.
		if why := reason(err); s.agg.allow(why, key) {
			s.log.Info("socks request rejected", "peer", peer, "user", sanitizeUser(user), "reason", why)
		}
		return
	}

	// The handshake deadline must not cover the dial; bound dial + reply instead.
	_ = c.SetDeadline(time.Now().Add(s.dialTimeout + replyGrace))
	ctx, cancel := context.WithTimeout(s.ctx, s.dialTimeout)
	up, err := s.dialer.DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		// Fail closed: no retry, no alternative route.
		_ = writeReply(c, repGeneralFailure)
		// Aggregated per source too: a DEGRADED slot refuses every CONNECT, so a
		// client looping on it must not flush the slot log.
		args := []any{"peer", peer, "user", sanitizeUser(user), "reason", "dial_failed"}
		errText := ""
		if s.redact != nil {
			errText = s.redact(err.Error())
			args = append(args, "err", errText)
		}
		if s.agg.allowErr("dial_failed", key, errText) {
			s.log.Info("socks connect failed", args...)
		}
		return
	}
	if !s.trackUpstream(up) {
		_ = up.Close()
		return
	}
	defer s.untrack(up)

	if err := writeReply(c, repSucceeded); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	s.log.Debug("socks connect", "peer", peer, "user", sanitizeUser(user))
	relay(c, up, s.idleTimeout, s.log.With("peer", peer))
}

// logPanic records a recovered panic at ERROR. The panic value is not
// logged: it may carry request data (and so secrets); the stack locates it.
func (s *Server) logPanic(where string, r any) {
	s.log.Error("socks panic recovered", "reason", "panic", "where", where,
		"type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
}

// reason maps an error to a log-safe reason code.
func reason(err error) string {
	for _, known := range []error{errBadVersion, errNoMethod, errBadAuthVersion, errAuthFailed, errAuthThrottled,
		errBadCommand, errBadAddrType, errBadAddr} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "handshake_timeout"
	}
	return "io_error"
}
