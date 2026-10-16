//go:build memload

package main

// TestMemLoad measures where peak memory comes from (plan C8). It runs the real entrypoint
// (run) with 4 WireGuard slots against a rig in a child process, then goes through phases:
// boot (all UP + idle), login (setup + argon2 logins), load (concurrent relays moving tens
// of MB per slot), mixed (load + logins) and idle-after. VmHWM is reset (/proc/self/clear_refs "5") at each phase start,
// so every phase reports its own peak; the process peak is the max over phases.
//
// A "mixed" phase repeats the load with logins running at the same time.
//
//	go test -tags memload -run '^TestMemLoad$' -v -timeout 10m ./cmd/sockswebui
//
// Knobs (env): MEMLOAD_CONNS per slot (2), MEMLOAD_MB down per conn (50), MEMLOAD_UP_MB (0),
// MEMLOAD_LOGINS (6), MEMLOAD_LOGIN_PAR (2), MEMLOAD_OUT (profiles + summary dir),
// MEMLOAD_RIG_CPUS (taskset for the rig child). scripts/rss-check.sh --load wraps this.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

func init() { runtime.MemProfileRate = 32 << 10 } // finer than the 512 KiB default

// loginSeq gives every login its own 127.0.1.x source so the per-client auth limiter never refuses one.
var loginSeq atomic.Int32

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v >= 0 {
		return v
	}
	return def
}

func TestMemLoad(t *testing.T) {
	conns, mb, upMB := envInt("MEMLOAD_CONNS", 2), envInt("MEMLOAD_MB", 50), envInt("MEMLOAD_UP_MB", 0)
	logins, par := envInt("MEMLOAD_LOGINS", 6), max(envInt("MEMLOAD_LOGIN_PAR", 2), 1)
	out := os.Getenv("MEMLOAD_OUT")
	if out == "" {
		out = t.TempDir()
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &memRecorder{out: out}
	defer rec.writeSummary(t)

	eps, stopRig := startRigChild(t)
	defer stopRig()
	dir := t.TempDir()
	ports := seedMemConfig(t, dir, eps)
	mgrc := make(chan *supervisor.Manager, 1)
	onAutostart = func(m *supervisor.Manager) { mgrc <- m }
	defer func() { onAutostart = nil }()

	rec.begin("boot")
	base := startRun(t, dir, testFS())
	mgr := <-mgrc
	waitAllUp(t, mgr, 4, 30*time.Second)
	time.Sleep(10 * time.Second)
	rec.end(t)

	rec.begin("login")
	doLogins(t, base, logins, par, true)
	rec.end(t)

	rec.begin("load")
	start := time.Now()
	runLoad(t, ports, conns, int64(mb)<<20, int64(upMB)<<20)
	secs := time.Since(start).Seconds()
	total := float64(len(ports)*conns*(mb+upMB)) * 8 / secs
	rec.note(fmt.Sprintf("load: %d slots x %d conns x %d MB down + %d MB up in %.1fs = %.0f Mbit/s aggregate",
		len(ports), conns, mb, upMB, secs, total*1.048576))
	rec.end(t)

	// mixed: the same load with logins at the same time (worst case: argon2 on top of relay buffers).
	rec.begin("mixed")
	var lw sync.WaitGroup
	lw.Add(1)
	go func() { defer lw.Done(); time.Sleep(time.Second); doLogins(t, base, logins, par, false) }()
	runLoad(t, ports, conns, int64(mb)<<20, int64(upMB)<<20)
	lw.Wait()
	rec.end(t)

	rec.begin("idle-after")
	time.Sleep(15 * time.Second)
	rec.end(t)
}

// seedMemConfig writes 4 endpoints and 4 enabled slots (one WG hop each) on free ports.
func seedMemConfig(t *testing.T, dir string, eps []config.Endpoint) []int {
	t.Helper()
	if err := ensureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	st, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ports []int
	err = st.Update(func(c *config.Config) error {
		c.Endpoints = eps
		c.Slots = nil
		for i, ep := range eps {
			p := memFreePort(t)
			ports = append(ports, p)
			c.Slots = append(c.Slots, config.Slot{
				ID: i + 1, Name: fmt.Sprintf("mem%d", i+1), Enabled: true, BindAddr: "127.0.0.1", Port: p,
				Route: []string{ep.ID}, Killswitch: config.KillswitchBlock,
				Health: config.Health{IntervalS: 30, URL: "http://echo.rig.invalid/", FailThreshold: 2},
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ports
}

func memFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitAllUp(t *testing.T, m *supervisor.Manager, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		up := 0
		for _, s := range m.Snapshot() {
			if s.State == supervisor.StateUp {
				up++
			}
		}
		if up >= n {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("slots not UP: %+v", m.Snapshot())
}

// doLogins runs setup (if setup), then n logins, par at a time, each from its own loopback
// address so the per-client auth limiter does not refuse them.
func doLogins(t *testing.T, base string, n, par int, setup bool) {
	t.Helper()
	const pw = `{"password":"memload password one"}`
	if setup {
		if res, body := doReq(t, "POST", base+"/api/setup", pw); res.StatusCode/100 != 2 {
			t.Fatalf("setup: %d %s", res.StatusCode, body)
		}
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			src := &net.TCPAddr{IP: net.IPv4(127, 0, 1, byte(loginSeq.Add(1)))}
			cl := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{LocalAddr: src}).DialContext}}
			res, err := cl.Post(base+"/api/login", "application/json", strings.NewReader(pw))
			if err != nil {
				t.Errorf("login %d: %v", i, err)
				return
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Errorf("login %d: status %d", i, res.StatusCode)
			}
		}(i)
	}
	wg.Wait()
}

// runLoad runs conns transfers on every slot at once.
func runLoad(t *testing.T, ports []int, conns int, down, up int64) {
	t.Helper()
	target := net.JoinHostPort("10.64.0.1", strconv.Itoa(memloadBulkPort))
	var wg sync.WaitGroup
	for _, p := range ports {
		for c := 0; c < conns; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := socksTransfer(net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), target, down, up); err != nil {
					t.Errorf("slot port %d: %v", p, err)
				}
			}()
		}
	}
	wg.Wait()
}

// writeFile writes b under out; failures are test errors.
func writeFile(t *testing.T, out, name string, b []byte) {
	if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
		t.Error(err)
	}
}
