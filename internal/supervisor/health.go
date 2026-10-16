package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

// DefaultProbeTimeout bounds one health probe (dial + TLS + response).
const DefaultProbeTimeout = 10 * time.Second

// maxProbeBody caps how much of the probe response is read.
const maxProbeBody = 64 << 10

// Dialer is the slot's egress path; egress.Chain satisfies it. It is the
// ONLY way a probe reaches the network: only internal/egress opens sockets,
// and a probe must prove the same path the slot's traffic takes.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Result is one successful probe through the full chain.
// ExitIP and Country are best-effort: empty when the body does not carry them.
type Result struct {
	ExitIP  string
	Country string
	Latency time.Duration
}

// TimeoutFor is the per-probe timeout for h: DefaultProbeTimeout, capped at
// the probe interval so a probe never outlives its slot in the schedule.
func TimeoutFor(h config.Health) time.Duration {
	iv := time.Duration(h.IntervalS) * time.Second
	if iv > 0 && iv < DefaultProbeTimeout {
		return iv
	}
	return DefaultProbeTimeout
}

// newProbeClient returns a client whose every connection is dialed by d.
// Proxy is nil (no environment proxy), keep-alives are off and HTTP/2 is not
// forced, so a rebuilt chain never reuses a connection from an old one.
// TLS for https URLs runs over the conn d returns.
func newProbeClient(d Dialer, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:            d.DialContext,
			Proxy:                  nil,
			ForceAttemptHTTP2:      false,
			DisableKeepAlives:      true,
			TLSHandshakeTimeout:    timeout,
			ResponseHeaderTimeout:  timeout,
			MaxResponseHeaderBytes: maxProbeBody,
		},
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // a redirect is not a healthy exit
		},
	}
}

// Probe GETs rawURL through d and reports exit IP, country and latency.
// Any dial, TLS, timeout or non-2xx outcome is an error; a 2xx response
// whose body is not the expected JSON still succeeds (latency only).
// There is no fallback path: if d fails, the probe fails.
func Probe(ctx context.Context, d Dialer, rawURL string, timeout time.Duration) (Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("health: bad url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return Result{}, errors.New("health: url must be an http or https URL with a host")
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("health: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := newProbeClient(d, timeout)
	defer client.CloseIdleConnections()
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("health: probe: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	latency := time.Since(start)
	if err != nil {
		return Result{}, fmt.Errorf("health: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, fmt.Errorf("health: probe status %d", resp.StatusCode)
	}
	if latency <= 0 {
		latency = time.Nanosecond
	}
	res := Result{Latency: latency}
	var info struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
	}
	if json.Unmarshal(body, &info) == nil {
		res.ExitIP, res.Country = info.IP, info.Country
	}
	return res, nil
}
