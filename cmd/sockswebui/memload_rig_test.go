//go:build memload

package main

// Rig side of TestMemLoad: the WireGuard peer and the bulk server run in a child
// process (this test binary re-executed with MEMLOAD_RIG_CHILD=1), so the parent's
// VmHWM and heap profiles hold only product code. Testrig owns the peer's networking.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const (
	memloadBulkPort = 9000
	memloadMarker   = "MEMLOAD-ENDPOINTS "
)

// TestMemLoadRigChild is the child process: one WG peer with four client keys and a
// bulk server on TunnelIP:9000. It prints the endpoints, then runs until stdin closes.
func TestMemLoadRigChild(t *testing.T) {
	if os.Getenv("MEMLOAD_RIG_CHILD") != "1" {
		t.Skip("child process of TestMemLoad only")
	}
	peer, err := testrig.StartWGPeer("memrig")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	eps := []config.Endpoint{peer.ClientConfig()}
	for i := 0; i < 3; i++ {
		ep, err := peer.AddClientKey(netip.AddrFrom4([4]byte{10, 64, 0, byte(3 + i)}))
		if err != nil {
			t.Fatal(err)
		}
		eps = append(eps, ep)
	}
	for i := range eps {
		eps[i].ID, eps[i].Name = fmt.Sprintf("memwg%d", i+1), fmt.Sprintf("memwg%d", i+1)
	}
	ln, err := peer.Net().ListenTCPAddrPort(netip.AddrPortFrom(peer.TunnelIP(), memloadBulkPort))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serveBulk(ln)
	b, _ := json.Marshal(eps)
	fmt.Printf("%s%s\n", memloadMarker, b)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// serveBulk answers each conn's 16-byte request (download, upload byte counts):
// it writes download bytes and reads upload bytes, then closes.
func serveBulk(ln net.Listener) {
	zero := make([]byte, 64<<10)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var hdr [16]byte
			if _, err := io.ReadFull(c, hdr[:]); err != nil {
				return
			}
			down, up := int64(binary.BigEndian.Uint64(hdr[:8])), int64(binary.BigEndian.Uint64(hdr[8:]))
			done := make(chan struct{})
			go func() { _, _ = io.CopyN(io.Discard, c, up); close(done) }()
			for down > 0 {
				n := min(down, int64(len(zero)))
				if _, err := c.Write(zero[:n]); err != nil {
					return
				}
				down -= n
			}
			<-done
		}()
	}
}

// startRigChild launches the child and returns its endpoints and a stop func.
// MEMLOAD_RIG_CPUS (e.g. "1-7") pins the child with taskset so a single-CPU
// parent does not share its core with the rig.
func startRigChild(t *testing.T) ([]config.Endpoint, func()) {
	t.Helper()
	args := []string{os.Args[0], "-test.run", "^TestMemLoadRigChild$", "-test.v"}
	if cpus := os.Getenv("MEMLOAD_RIG_CPUS"); cpus != "" {
		args = append([]string{"taskset", "-c", cpus}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "MEMLOAD_RIG_CHILD=1", "GOMAXPROCS=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop := func() { _ = stdin.Close(); _ = cmd.Wait() }
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if js, ok := strings.CutPrefix(sc.Text(), memloadMarker); ok {
			var eps []config.Endpoint
			if err := json.Unmarshal([]byte(js), &eps); err != nil {
				stop()
				t.Fatal(err)
			}
			go func() { _, _ = io.Copy(io.Discard, stdout) }()
			return eps, stop
		}
	}
	stop()
	t.Fatal("rig child exited before printing endpoints")
	return nil, nil
}

// socksTransfer CONNECTs through the SOCKS5 slot at proxy (no auth) to target and
// moves down bytes in and up bytes out via the bulk protocol.
func socksTransfer(proxy, target string, down, up int64) error {
	c, err := net.DialTimeout("tcp", proxy, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	host, ps, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(ps)
	ip := netip.MustParseAddr(host).As4()
	req := []byte{5, 1, 0, 5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	if _, err := c.Write(req); err != nil {
		return err
	}
	var rep [12]byte // 2 method reply + 10 CONNECT reply (IPv4 bound addr)
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return fmt.Errorf("socks reply: %w", err)
	}
	if rep[1] != 0 || rep[3] != 0 {
		return fmt.Errorf("socks refused: % x", rep)
	}
	var hdr [16]byte
	binary.BigEndian.PutUint64(hdr[:8], uint64(down))
	binary.BigEndian.PutUint64(hdr[8:], uint64(up))
	if _, err := c.Write(hdr[:]); err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		_, err := io.CopyN(c, zeroReader{}, up)
		errc <- err
	}()
	n, err := io.Copy(io.Discard, c)
	if werr := <-errc; werr != nil {
		return werr
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	if n != down {
		return fmt.Errorf("got %d bytes, want %d", n, down)
	}
	return nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
