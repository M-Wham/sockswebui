package testrig

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// Sentinel is a "direct route" tripwire: a TCP listener on 127.0.0.1 that
// counts every byte it receives. Kill-switch tests assert Bytes() == 0.
type Sentinel struct {
	ln    net.Listener
	bytes atomic.Int64
	conns atomic.Int64

	mu     sync.Mutex
	active map[net.Conn]struct{}
	closed bool

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// NewSentinel starts a sentinel and registers Close with t.Cleanup.
func NewSentinel(t testing.TB) *Sentinel {
	t.Helper()
	s, err := StartSentinel()
	if err != nil {
		t.Fatalf("testrig: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// StartSentinel is NewSentinel without testing.TB: the caller owns Close.
func StartSentinel() (*Sentinel, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sentinel listen: %w", err)
	}
	s := &Sentinel{ln: ln, active: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func (s *Sentinel) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.active[c] = struct{}{}
		s.mu.Unlock()
		s.conns.Add(1)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			_, _ = io.Copy(counter{&s.bytes}, c)
			s.mu.Lock()
			delete(s.active, c)
			s.mu.Unlock()
			_ = c.Close()
		}()
	}
}

type counter struct{ n *atomic.Int64 }

func (w counter) Write(p []byte) (int, error) { w.n.Add(int64(len(p))); return len(p), nil }

// Addr is the sentinel's 127.0.0.1:port address.
func (s *Sentinel) Addr() string { return s.ln.Addr().String() }

// Bytes is the total number of bytes received across all connections.
func (s *Sentinel) Bytes() int64 { return s.bytes.Load() }

// Conns is the number of connections accepted.
func (s *Sentinel) Conns() int64 { return s.conns.Load() }

// Open is the number of accepted connections the peer has not yet closed
// (a conn leaves the count once the sentinel reads EOF or an error on it).
func (s *Sentinel) Open() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// Close stops the listener, closes open connections, and waits for goroutines.
func (s *Sentinel) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		for c := range s.active {
			_ = c.Close()
		}
		s.mu.Unlock()
		_ = s.ln.Close()
		s.wg.Wait()
	})
	return nil
}
