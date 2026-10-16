package testrig

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// UDPSentinel is a host UDP tripwire: a socket on 127.0.0.1 that counts every
// datagram it receives. Tests assert Datagrams() == 0 to prove no traffic
// reached it through a real OS socket.
type UDPSentinel struct {
	pc        net.PacketConn
	datagrams atomic.Int64
	done      chan struct{}
	closeOnce sync.Once
}

// NewUDPSentinel starts a UDP sentinel and registers Close with t.Cleanup.
func NewUDPSentinel(t testing.TB) *UDPSentinel {
	t.Helper()
	s, err := StartUDPSentinel()
	if err != nil {
		t.Fatalf("testrig: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// StartUDPSentinel is NewUDPSentinel without testing.TB: the caller owns Close.
func StartUDPSentinel() (*UDPSentinel, error) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("udp sentinel listen: %w", err)
	}
	s := &UDPSentinel{pc: pc, done: make(chan struct{})}
	go s.serve()
	return s, nil
}

func (s *UDPSentinel) serve() {
	defer close(s.done)
	buf := make([]byte, 64<<10)
	for {
		if _, _, err := s.pc.ReadFrom(buf); err != nil {
			return // closed
		}
		s.datagrams.Add(1)
	}
}

// Addr is the sentinel's 127.0.0.1:port address.
func (s *UDPSentinel) Addr() string { return s.pc.LocalAddr().String() }

// Datagrams is the number of datagrams received.
func (s *UDPSentinel) Datagrams() int64 { return s.datagrams.Load() }

// Close stops the socket and waits for the reader goroutine. Idempotent.
func (s *UDPSentinel) Close() error {
	s.closeOnce.Do(func() {
		_ = s.pc.Close()
		<-s.done
	})
	return nil
}
