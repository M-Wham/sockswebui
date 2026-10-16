//go:build testrig

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// startDevRun runs the entrypoint with --dev-testrig and returns its base URL.
func startDevRun(t *testing.T, dataDir string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--dev-testrig", "--data-dir", dataDir, "--ui-addr", "127.0.0.1:0"},
			io.Discard, io.Discard, testFS())
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after cancel")
		}
		onListen = nil
	})
	select {
	case addr := <-ready:
		return "http://" + addr
	case code := <-done:
		t.Fatalf("run exited early with code %d", code)
	case <-time.After(15 * time.Second):
		t.Fatal("server did not start")
	}
	return ""
}

// waitSlotUp reads /api/events until slot id is UP.
func waitSlotUp(t *testing.T, base string, c *http.Cookie, id int, timeout time.Duration) {
	t.Helper()
	waitSlotState(t, base, c, id, "UP", timeout)
}

// waitSlotState reads /api/events until slot id reports want.
func waitSlotState(t *testing.T, base string, c *http.Cookie, id int, want string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/events", nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	var seen []string
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var frame struct {
			Slot  int    `json:"slot"`
			State string `json:"state"`
			Slots []struct {
				ID    int    `json:"id"`
				State string `json:"state"`
			} `json:"slots"`
		}
		if json.Unmarshal([]byte(data), &frame) != nil {
			continue
		}
		seen = append(seen, data)
		if frame.Slot == id && frame.State == want {
			return
		}
		for _, s := range frame.Slots {
			if s.ID == id && s.State == want {
				return
			}
		}
	}
	t.Fatalf("slot %d never reached %s; frames: %v", id, want, seen)
}

func TestDevTestrigSeedsAndReachesUp(t *testing.T) {
	if !devTestrigCompiled {
		t.Fatal("devTestrigCompiled must be true under -tags testrig")
	}
	devRigSlotPort = freePort(t)
	dir := t.TempDir()
	base := startDevRun(t, dir)
	c := setupAndLogin(t, base)

	res, body := doReq(t, "GET", base+"/api/endpoints", "", c)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("endpoints: %d %s", res.StatusCode, body)
	}
	for _, id := range []string{`"rigWG"`, `"rigSOCKS"`, `"` + testrig.DevRigWG + `"`, `"` + testrig.DevRigSOCKS + `"`} {
		if !strings.Contains(body, id) {
			t.Errorf("endpoints missing %s: %s", id, body)
		}
	}
	res, body = doReq(t, "GET", base+"/api/slots/1", "", c)
	var sl struct {
		Enabled bool          `json:"enabled"`
		Port    int           `json:"port"`
		Route   []string      `json:"route"`
		Health  config.Health `json:"health"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &sl) != nil {
		t.Fatalf("slot 1: %d %s", res.StatusCode, body)
	}
	if !sl.Enabled || sl.Port != devRigSlotPort || strings.Join(sl.Route, ",") != testrig.DevRigWG ||
		sl.Health.URL != "http://echo.rig.invalid/" {
		t.Errorf("slot 1 = %+v", sl)
	}
	waitSlotUp(t, base, c, 1, 20*time.Second)
}

// A store that already has endpoints is never overwritten by the rig seed.
func TestDevTestrigSkipsSeedWhenEndpointsExist(t *testing.T) {
	dir := t.TempDir()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	keep := config.Endpoint{ID: "mine", Name: "mine", Kind: config.KindSOCKS5, Host: "192.0.2.1", Port: 1080}
	if err := store.Update(func(c *config.Config) error {
		c.Endpoints = append(c.Endpoints, keep)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	devRigSlotPort = freePort(t)
	base := startDevRun(t, dir)
	c := setupAndLogin(t, base)
	_, body := doReq(t, "GET", base+"/api/endpoints", "", c)
	if !strings.Contains(body, `"mine"`) || strings.Contains(body, "rigWG") || strings.Contains(body, testrig.DevRigWG) {
		t.Errorf("endpoints = %s", body)
	}
	_, body = doReq(t, "GET", base+"/api/slots/1", "", c)
	if strings.Contains(body, `"configured":true`) {
		t.Errorf("slot 1 seeded over existing config: %s", body)
	}
}

// POST /api/dev/rig/pause|resume (testrig build only) toggle the rig WG peer
// behind the normal admin session + CSRF; the seeded slot goes DEGRADED and
// then UP again (kill-switch recovery; Playwright smoke).
func TestDevRigPauseResumeRoutes(t *testing.T) {
	devRigSlotPort = freePort(t)
	base := startDevRun(t, t.TempDir())
	setupAndLogin(t, base)
	c, tok := loginCSRF(t, base)
	pause, resume := base+"/api/dev/rig/pause", base+"/api/dev/rig/resume"

	if code := postCSRF(t, pause, nil, ""); code != http.StatusUnauthorized {
		t.Errorf("pause without session = %d, want 401", code)
	}
	if code := postCSRF(t, pause, c, ""); code != http.StatusForbidden {
		t.Errorf("pause without CSRF = %d, want 403", code)
	}
	waitSlotUp(t, base, c, 1, 20*time.Second)
	if code := postCSRF(t, pause, c, tok); code != http.StatusNoContent {
		t.Fatalf("pause = %d, want 204", code)
	}
	waitSlotState(t, base, c, 1, "DEGRADED", 15*time.Second)
	if code := postCSRF(t, resume, c, tok); code != http.StatusNoContent {
		t.Fatalf("resume = %d, want 204", code)
	}
	waitSlotState(t, base, c, 1, "UP", 30*time.Second)
}

// The seeded dev slot probes fast enough that DEGRADED shows within seconds.
func TestDevRigSlotHealthIsFast(t *testing.T) {
	h := devRigSlotN(1).Health
	if h.IntervalS*h.FailThreshold > 4 {
		t.Errorf("dev slot health %+v: DEGRADED would take > 4s", h)
	}
}

func startDevRunArgs(t *testing.T, dataDir string, extra ...string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	onListen = func(addr string) { ready <- addr }
	done := make(chan int, 1)
	args := append([]string{"--dev-testrig", "--data-dir", dataDir, "--ui-addr", "127.0.0.1:0"}, extra...)
	go func() { done <- run(ctx, args, io.Discard, io.Discard, testFS()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after cancel")
		}
		onListen = nil
	})
	select {
	case addr := <-ready:
		return "http://" + addr
	case code := <-done:
		t.Fatalf("run exited early with code %d", code)
	case <-time.After(15 * time.Second):
		t.Fatal("server did not start")
	}
	return ""
}

func TestDevRigFourSlots(t *testing.T) {
	devRigSlotPort = freePort(t)
	dir := t.TempDir()
	base := startDevRunArgs(t, dir, "--dev-testrig-slots", "4")
	c := setupAndLogin(t, base)

	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	idRe := regexp.MustCompile(`^ep-[0-9a-f]{16}$`)
	for _, e := range cfg.Endpoints {
		if !idRe.MatchString(e.ID) {
			t.Errorf("seeded endpoint %q id %q is not importable", e.Name, e.ID)
		}
	}
	wantRoutes := []string{testrig.DevRigWG, testrig.DevRigWG2, testrig.DevRigWG3, testrig.DevRigWG4}
	ports := map[int]bool{}
	got := map[int]config.Slot{}
	for _, s := range cfg.Slots {
		got[s.ID] = s
	}
	for i, r := range wantRoutes {
		s, ok := got[i+1]
		if !ok {
			t.Fatalf("slot %d missing", i+1)
		}
		if !s.Enabled || strings.Join(s.Route, ",") != r || s.Killswitch != config.KillswitchBlock {
			t.Errorf("slot %d = %+v", i+1, s)
		}
		if ports[s.Port] {
			t.Errorf("slot %d reuses port %d", s.ID, s.Port)
		}
		ports[s.Port] = true
	}
	for id := 1; id <= 4; id++ {
		waitSlotUp(t, base, c, id, 30*time.Second)
	}
}

func TestDevRigDefaultOneSlot(t *testing.T) {
	devRigSlotPort = freePort(t)
	dir := t.TempDir()
	base := startDevRun(t, dir)
	setupAndLogin(t, base)
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	n := 0
	for _, s := range cfg.Slots {
		if s.Enabled || s.Port != 0 {
			n++
			if s.ID != 1 || strings.Join(s.Route, ",") != testrig.DevRigWG {
				t.Errorf("unexpected slot %+v", s)
			}
		}
	}
	if n != 1 {
		t.Errorf("seeded %d slots, want 1", n)
	}
	if len(cfg.Endpoints) != 3 {
		// rigWG, rigWG2, rigSOCKS
		t.Errorf("endpoints = %d", len(cfg.Endpoints))
	}
}
