package socks

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type closeWriter interface{ CloseWrite() error }

const relayBufSize = 32 * 1024

// idleRelay is the state both pipe loops of one relay share, so payload in
// either direction keeps the whole relay alive.
type idleRelay struct {
	a, b      net.Conn
	timeout   time.Duration
	start     time.Time    // monotonic origin for last
	last      atomic.Int64 // ns since start of the last payload read, either direction
	deadlines bool         // false = fallback watcher (a conn rejected read deadlines)
	wdeadline bool         // both conns accept write deadlines
	once      sync.Once
	log       *slog.Logger
}

// relay copies bytes in both directions until both directions finish, an
// error occurs, or no payload has moved in either direction for idle. A clean
// EOF is propagated as a half-close when the destination supports it;
// otherwise both connections are closed. Both connections are always closed
// when relay returns.
func relay(a, b net.Conn, idle time.Duration, log *slog.Logger) {
	r := &idleRelay{a: a, b: b, timeout: idle, start: time.Now(), log: log}
	r.deadlines = a.SetReadDeadline(r.deadline()) == nil && b.SetReadDeadline(r.deadline()) == nil
	r.wdeadline = a.SetWriteDeadline(time.Time{}) == nil && b.SetWriteDeadline(time.Time{}) == nil

	var watcher sync.WaitGroup
	stop := make(chan struct{})
	if !r.deadlines {
		// Fallback for conns without read deadlines: a watcher goroutine for
		// this relay only.
		_ = a.SetReadDeadline(time.Time{})
		_ = b.SetReadDeadline(time.Time{})
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			r.watch(stop)
		}()
	}

	done := make(chan struct{}, 1)
	go func() {
		r.pipe(b, a)
		done <- struct{}{}
	}()
	r.pipe(a, b)
	<-done
	close(stop)
	watcher.Wait()
	_ = a.Close()
	_ = b.Close()
}

// pipe copies src to dst and signals end-of-stream on dst. With deadlines it
// re-arms the read deadline from the shared activity stamp before each read.
// Each write must complete within the idle timeout: a peer that stops reading
// would otherwise pin the relay while the other direction keeps it non-idle.
func (r *idleRelay) pipe(dst, src net.Conn) {
	buf := make([]byte, relayBufSize)
	var err error
	for {
		if r.deadlines {
			if err = src.SetReadDeadline(r.deadline()); err != nil {
				break // fail closed
			}
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			r.last.Store(int64(time.Since(r.start)))
			if r.wdeadline {
				if err = dst.SetWriteDeadline(time.Now().Add(r.timeout)); err != nil {
					break // fail closed
				}
			}
			if _, err = dst.Write(buf[:n]); err != nil {
				break
			}
		}
		if rerr == nil {
			continue
		}
		if rerr == io.EOF {
			err = nil
			break
		}
		err = rerr
		if r.deadlines && errors.Is(rerr, os.ErrDeadlineExceeded) {
			if !r.idle() {
				continue // the other direction is active
			}
			r.closeIdle()
		}
		break
	}
	if cw, ok := dst.(closeWriter); ok && err == nil {
		if cw.CloseWrite() == nil {
			return
		}
	}
	_ = dst.Close()
	_ = src.Close()
}

// deadline is the time the relay becomes idle if no payload moves before it.
func (r *idleRelay) deadline() time.Time {
	return r.start.Add(time.Duration(r.last.Load()) + r.timeout)
}

// idleFor is how long no payload has moved in either direction.
func (r *idleRelay) idleFor() time.Duration {
	return time.Since(r.start) - time.Duration(r.last.Load())
}

func (r *idleRelay) idle() bool { return r.idleFor() >= r.timeout }

// closeIdle closes both conns and logs once at DEBUG: an idle close is
// neither a fallback nor a kill-switch event, and must not flood the slot log.
func (r *idleRelay) closeIdle() {
	r.once.Do(func() {
		_ = r.a.Close()
		_ = r.b.Close()
		r.log.Debug("socks relay idle closed", "reason", "idle_timeout", "idle", r.timeout)
	})
}

// watch is the deadline-unsupported fallback: it closes the relay once idle
// and exits when stop is closed.
func (r *idleRelay) watch(stop <-chan struct{}) {
	t := time.NewTimer(r.timeout)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if d := r.idleFor(); d < r.timeout {
				t.Reset(r.timeout - d)
				continue
			}
			r.closeIdle()
			return
		}
	}
}
