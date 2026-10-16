package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSetupFreshStore(t *testing.T) {
	e := newTestEnv(t)
	rec := e.do("POST", "/api/setup", `{"password":"hunter22-long"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	s := e.store.Get().Settings
	if !strings.HasPrefix(s.AdminPasswordHash, "$argon2id$") {
		t.Fatalf("admin hash = %q", s.AdminPasswordHash)
	}
	if len(s.SessionSecret) < 32 {
		t.Fatalf("session secret not generated: len %d", len(s.SessionSecret))
	}
	if strings.Contains(e.logs.String(), "hunter22-long") {
		t.Fatal("password leaked into logs")
	}
}

func TestSetupSecondTimeConflicts(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	before := e.store.Get().Settings.AdminPasswordHash
	rec := e.do("POST", "/api/setup", `{"password":"another-password"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup status %d, want 409", rec.Code)
	}
	if e.store.Get().Settings.AdminPasswordHash != before {
		t.Fatal("second setup changed the admin hash")
	}
}

func TestSetupConcurrentOnlyOneWins(t *testing.T) {
	e := newTestEnv(t)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) { // one client each, so the race is on the store, not the limiter
			defer wg.Done()
			remote := withRemote(fmt.Sprintf("192.0.2.%d:1", n+1))
			codes <- e.do("POST", "/api/setup", `{"password":"hunter22-long"}`, remote).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		switch c {
		case http.StatusNoContent:
			ok++
		case http.StatusConflict:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d setups succeeded, want 1", ok)
	}
}

func TestSetupShortPassword(t *testing.T) {
	e := newTestEnv(t)
	rec := e.do("POST", "/api/setup", `{"password":"short7!"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	errs := decodeFieldErrors(t, rec)
	if len(errs) != 1 || errs[0].Field != "password" {
		t.Fatalf("errors = %+v", errs)
	}
	if e.store.Get().Settings.AdminPasswordHash != "" {
		t.Fatal("hash persisted despite invalid password")
	}
}

func TestSetupBadBody(t *testing.T) {
	e := newTestEnv(t)
	for _, body := range []string{`not json`, `{"password":`, `{"password":"` + strings.Repeat("a", 2000) + `"}`} {
		rec := e.do("POST", "/api/setup", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %.20q: status %d, want 400", body, rec.Code)
		}
	}
}

// TestSetupRateLimited: setup draws on the shared auth limit after the
// 409 check and before decode or argon2, so a 409 spends no budget.
func TestSetupRateLimited(t *testing.T) {
	const ip = "198.51.100.40:1234"
	e := newTestEnv(t)
	var hashes atomic.Int32
	e.srv.freeOSMemory = func() { hashes.Add(1) }
	post := func(body string) *httptest.ResponseRecorder {
		return e.do("POST", "/api/setup", body, withRemote(ip))
	}
	for i := 0; i < loginLimit; i++ {
		if rec := post(`not json`); rec.Code != http.StatusBadRequest {
			t.Fatalf("bad attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	rec := post(`{"password":"hunter22-long"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("6th attempt: %d Retry-After=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if h := e.store.Get().Settings.AdminPasswordHash; h != "" {
		t.Fatalf("admin hash stored despite 429: %q", h)
	}
	if n := hashes.Load(); n != 0 {
		t.Fatalf("argon2 ran %d times, want 0", n)
	}
}

// TestSetupConflictSpendsNoBudget: a 409 (already set up) never touches the limiter.
func TestSetupConflictSpendsNoBudget(t *testing.T) {
	const ip = "198.51.100.41:1234"
	e := newTestEnv(t)
	e.setup("hunter22-long")
	for i := 0; i < loginLimit+2; i++ {
		if rec := e.do("POST", "/api/setup", `not json`, withRemote(ip)); rec.Code != http.StatusConflict {
			t.Fatalf("attempt %d: %d, want 409", i, rec.Code)
		}
	}
	if rec := e.do("POST", "/api/login", `{"password":"hunter22-long"}`, withRemote(ip)); rec.Code != http.StatusOK {
		t.Fatalf("login after 409s: %d %s", rec.Code, rec.Body)
	}
}

// A setup whose config write fails before the rename (temp file cannot be created) must
// not lock setup: 500, no hash in memory, and a retry once the disk is back succeeds.
func TestSetupWriteFailureKeepsSetupOpen(t *testing.T) {
	e := newTestEnv(t)
	restore := replaceDirWithFile(t, e.dir)
	rec := e.do("POST", "/api/setup", `{"password":"hunter22-long"}`)
	restore()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("setup on a failing disk: status %d body %s, want 500", rec.Code, rec.Body)
	}
	if h := e.store.Get().Settings.AdminPasswordHash; h != "" {
		t.Fatalf("admin hash set after a failed write: %q", h)
	}
	if rec := e.do("POST", "/api/setup", `{"password":"hunter22-long"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("second setup: status %d body %s, want 204", rec.Code, rec.Body)
	}
	if h := e.store.Get().Settings.AdminPasswordHash; h == "" {
		t.Fatal("second setup did not set the admin hash")
	}
}
