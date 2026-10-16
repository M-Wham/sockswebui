package main

import (
	"net"
	"net/http"
	"sync"
)

// newConns tracks connections that have been accepted but have not yet sent a
// request (http.StateNew). http.Server.Shutdown only treats those as idle once they
// are 5 s old, so a client's spare dial or a browser preconnect would otherwise
// stall a graceful shutdown until its deadline. closeAll closes them; any conn
// accepted afterwards is closed as soon as the server reports it.
type newConns struct {
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closing bool
}

// hook is the http.Server ConnState callback.
func (n *newConns) hook(c net.Conn, st http.ConnState) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if st != http.StateNew {
		delete(n.conns, c)
		return
	}
	if n.closing {
		c.Close()
		return
	}
	if n.conns == nil {
		n.conns = make(map[net.Conn]struct{})
	}
	n.conns[c] = struct{}{}
}

// closeAll closes every conn still waiting for its first request and makes the hook
// close any later ones immediately. Active requests are left to drain.
func (n *newConns) closeAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closing = true
	for c := range n.conns {
		c.Close()
	}
	n.conns = nil
}
