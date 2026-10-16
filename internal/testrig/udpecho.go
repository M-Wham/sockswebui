package testrig

import "net"

// ServeUDPEcho writes every datagram read from pc back to its sender until pc
// is closed. It returns a channel closed once the goroutine has exited.
// Typical use: pc from a WGPeer's Net().ListenUDPAddrPort, so the echo lives
// inside the tunnel.
func ServeUDPEcho(pc net.PacketConn) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64<<10)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return // closed
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	return done
}
