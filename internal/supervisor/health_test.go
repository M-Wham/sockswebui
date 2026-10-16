package supervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/egress"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// wgChain builds route [wgA] against a fresh testrig peer.
func wgChain(t *testing.T) (*testrig.WGPeer, egress.Chain) {
	t.Helper()
	p := testrig.NewWGPeer(t, "wgA")
	ep := p.ClientConfig()
	ep.ID = "wgA"
	ch, err := egress.Build([]string{"wgA"}, []config.Endpoint{ep})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return p, ch
}

// listenOnPeer opens a TCP listener on the peer's netstack at TunnelIP:port.
func listenOnPeer(t *testing.T, p *testrig.WGPeer, port uint16) net.Listener {
	t.Helper()
	ln, err := p.Net().ListenTCPAddrPort(netip.AddrPortFrom(p.TunnelIP(), port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestProbeViaChain(t *testing.T) {
	p, ch := wgChain(t)
	countryLn := listenOnPeer(t, p, 8080)
	done := testrig.ServeEchoCountry(countryLn, "wgA", "Sweden")
	t.Cleanup(func() { _ = countryLn.Close(); <-done })

	cases := []struct {
		name, url, country string
	}{
		{"plain echo", "http://" + testrig.EchoHost + "/", ""},
		{"echo with country", "http://" + testrig.EchoHost + ":8080/", "Sweden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Probe(context.Background(), ch, tc.url, 5*time.Second)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if res.ExitIP != p.ClientIP().String() {
				t.Errorf("ExitIP = %q, want %q (client tunnel addr)", res.ExitIP, p.ClientIP())
			}
			if res.Country != tc.country {
				t.Errorf("Country = %q, want %q", res.Country, tc.country)
			}
			if res.Latency <= 0 {
				t.Errorf("Latency = %v, want > 0", res.Latency)
			}
		})
	}
}

func TestProbeNonJSONBodyCountsAsSuccess(t *testing.T) {
	p, ch := wgChain(t)
	ln := listenOnPeer(t, p, 8081)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	res, err := Probe(context.Background(), ch, "http://"+testrig.EchoHost+":8081/", 5*time.Second)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.ExitIP != "" || res.Country != "" || res.Latency <= 0 {
		t.Fatalf("res = %+v, want empty ip/country and latency > 0", res)
	}
}

func TestProbeNon2xxIsFailure(t *testing.T) {
	p, ch := wgChain(t)
	ln := listenOnPeer(t, p, 8082)
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	if _, err := Probe(context.Background(), ch, "http://"+testrig.EchoHost+":8082/", 5*time.Second); err == nil {
		t.Fatal("Probe on 404 succeeded, want error")
	}
}

// failingDialer is a chain whose every dial fails.
type failingDialer struct{ calls int }

var errNoRoute = errors.New("stub: no route")

func (d *failingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.calls++
	return nil, errNoRoute
}

func TestProbeNoFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	s := testrig.NewSentinel(t)
	d := &failingDialer{}

	_, err := Probe(context.Background(), d, "http://"+s.Addr()+"/", 2*time.Second)
	if err == nil {
		t.Fatal("Probe via failing chain succeeded, want error")
	}
	if !errors.Is(err, errNoRoute) {
		t.Errorf("err = %v, want it to wrap the chain's dial error", err)
	}
	if d.calls == 0 {
		t.Error("chain was never dialed")
	}
	time.Sleep(50 * time.Millisecond) // let any stray connection land
	if s.Bytes() != 0 || s.Conns() != 0 {
		t.Fatalf("direct sentinel saw %d conns / %d bytes, want 0/0", s.Conns(), s.Bytes())
	}

	c := newProbeClient(d, time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", c.Transport)
	}
	if tr == http.DefaultTransport || tr.Proxy != nil {
		t.Fatal("probe transport must not be DefaultTransport and must have Proxy == nil")
	}
	if tr.DialContext == nil || tr.DialTLSContext != nil {
		t.Fatal("probe transport must dial only via the chain's DialContext")
	}
	if c.Timeout != time.Second {
		t.Fatalf("client Timeout = %v, want 1s", c.Timeout)
	}
}

func TestProbeTimeout(t *testing.T) {
	p, ch := wgChain(t)
	ln := listenOnPeer(t, p, 8083)
	held := make(chan net.Conn, 16)
	go func() { // accept and never answer
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held <- c
		}
	}()
	t.Cleanup(func() {
		for {
			select {
			case c := <-held:
				_ = c.Close()
			default:
				return
			}
		}
	})

	h := config.Health{IntervalS: 1, URL: "http://" + testrig.EchoHost + ":8083/", FailThreshold: 2}
	timeout := TimeoutFor(h)
	if timeout <= 0 || timeout > time.Duration(h.IntervalS)*time.Second {
		t.Fatalf("TimeoutFor = %v, want 0 < t <= interval", timeout)
	}
	start := time.Now()
	_, err := Probe(context.Background(), ch, h.URL, timeout)
	el := time.Since(start)
	if err == nil {
		t.Fatal("Probe on silent target succeeded, want error")
	}
	if el > timeout+500*time.Millisecond {
		t.Fatalf("Probe took %v, want <= %v", el, timeout)
	}
}

func TestTimeoutFor(t *testing.T) {
	if got := TimeoutFor(config.Health{IntervalS: 30}); got != DefaultProbeTimeout {
		t.Fatalf("TimeoutFor(30s) = %v, want %v", got, DefaultProbeTimeout)
	}
	if got := TimeoutFor(config.Health{IntervalS: 0}); got != DefaultProbeTimeout {
		t.Fatalf("TimeoutFor(0) = %v, want %v", got, DefaultProbeTimeout)
	}
}

func TestProbeBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://x/", "http:///nohost", "://"} {
		if _, err := Probe(context.Background(), &failingDialer{}, u, time.Second); err == nil ||
			errors.Is(err, errNoRoute) {
			t.Errorf("Probe(%q) err = %v, want URL error before dialing", u, err)
		}
	}
}
