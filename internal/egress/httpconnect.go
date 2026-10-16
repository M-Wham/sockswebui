package egress

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/M-Wham/sockswebui/internal/config"
)

func httpHop(ep config.Endpoint, prev dialFunc) dialFunc {
	return proxyHop(ep, prev, httpConnect)
}

// httpConnect issues an HTTP/1.1 CONNECT for target (hostname verbatim) with
// optional Proxy-Authorization: Basic. Any status but 200 is an error; the
// error carries only the proxy's status line, never the credentials.
func httpConnect(c net.Conn, ep config.Endpoint, target string) (net.Conn, error) {
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if user, pass := RenderCredentials(ep); user != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CONNECT refused: HTTP %d", resp.StatusCode)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufferedConn returns bytes the proxy sent right after its 200 before
// reading from the socket again.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
