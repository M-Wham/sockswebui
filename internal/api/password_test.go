package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	enc, err := hashPassword(context.Background(), "correct horse", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("encoded = %q", enc)
	}
	if ok, err := verifyPassword(context.Background(), "correct horse", enc); err != nil || !ok {
		t.Fatalf("verify correct: ok=%v err=%v", ok, err)
	}
	if ok, err := verifyPassword(context.Background(), "wrong horse", enc); err != nil || ok {
		t.Fatalf("verify wrong: ok=%v err=%v", ok, err)
	}
	enc2, _ := hashPassword(context.Background(), "correct horse", testParams)
	if enc == enc2 {
		t.Fatal("two hashes of the same password are identical (salt not random)")
	}
}

func TestPasswordDefaultParamsArePiSized(t *testing.T) {
	if defaultParams.MemoryKiB != 16*1024 || defaultParams.Time != 3 || defaultParams.Threads != 2 || defaultParams.KeyLen != 32 || defaultParams.SaltLen != 16 {
		t.Fatalf("defaultParams = %+v", defaultParams)
	}
}

func TestPasswordVerifyRejectsMalformed(t *testing.T) {
	for _, enc := range []string{
		"",
		"$argon2i$v=19$m=1024,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=1024,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=x,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1024,t=1,p=1$!!!$aGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1024,t=1,p=0$c2FsdA$aGFzaA",
	} {
		if ok, err := verifyPassword(context.Background(), "pw", enc); ok || err == nil {
			t.Errorf("verify(%q): ok=%v err=%v, want false + error", enc, ok, err)
		}
	}
}

func TestVerifyLegacyParams(t *testing.T) {
	old := argonParams{Time: 1, MemoryKiB: 64 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16}
	h, err := hashPassword(context.Background(), "legacy-pw", old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h, "m=65536,t=1,p=2") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if ok, err := verifyPassword(context.Background(), "legacy-pw", h); err != nil || !ok {
		t.Fatalf("legacy verify = %v, %v", ok, err)
	}
	if ok, err := verifyPassword(context.Background(), "wrong", h); err != nil || ok {
		t.Fatalf("wrong pw verify = %v, %v", ok, err)
	}
}

// trackedBody is a request body that records whether the handler read it.
type trackedBody struct {
	r    *strings.Reader
	read *atomic.Bool
}

func (b trackedBody) Read(p []byte) (int, error) {
	b.read.Store(true)
	return b.r.Read(p)
}

// stubArgon replaces the argon2id primitive with one that reports entry and blocks until
// release is closed, then derives the real key. It is restored at cleanup.
func stubArgon(t *testing.T) (entered chan struct{}, release chan struct{}) {
	t.Helper()
	orig := argonIDKey
	entered, release = make(chan struct{}, 64), make(chan struct{})
	argonIDKey = func(pw, salt []byte, tm, m uint32, th uint8, kl uint32) []byte {
		entered <- struct{}{}
		<-release
		return orig(pw, salt, tm, m, th, kl)
	}
	t.Cleanup(func() { argonIDKey = orig })
	return entered, release
}

type loginResult struct {
	rec  *httptest.ResponseRecorder
	read bool
}

// TestHashQueueBounded: with argon2 blocked, 10 concurrent logins from distinct clients →
// 8 queue (2 hashing, 6 waiting for a slot) and 2 get 503 "busy, retry shortly" with
// Retry-After: 1 before their body is read. After release the 8
// succeed, the waiter count is back to 0 and a fresh login succeeds within 1 s.
func TestHashQueueBounded(t *testing.T) {
	e := newTestEnv(t)
	const pw = "correct-horse-1"
	e.setup(pw)
	entered, release := stubArgon(t)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	const n = hashMaxWaiters + 2
	results := make(chan loginResult, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			read := &atomic.Bool{}
			body := trackedBody{r: strings.NewReader(`{"password":"` + pw + `"}`), read: read}
			req := httptest.NewRequest("POST", "/api/login", body)
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1", i+1)
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			results <- loginResult{rec, read.Load()}
		}(i)
	}

	timeout := time.After(5 * time.Second)
	for i := 0; i < n-hashMaxWaiters; i++ {
		select {
		case r := <-results:
			if r.rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("overflow login: status %d body %s, want 503", r.rec.Code, r.rec.Body)
			}
			if got := strings.TrimSpace(r.rec.Body.String()); got != `{"error":"busy, retry shortly"}` {
				t.Fatalf("overflow body = %s", got)
			}
			if ra := r.rec.Header().Get("Retry-After"); ra != "1" {
				t.Fatalf("Retry-After = %q, want 1", ra)
			}
			if r.read {
				t.Fatal("overflow login read the request body")
			}
		case <-timeout:
			t.Fatal("no 503 for the overflow logins")
		}
	}
	for i := 0; i < cap(hashSlots); i++ {
		select {
		case <-entered:
		case <-timeout:
			t.Fatal("argon2 never entered")
		}
	}
	if got := hashQueueLen(); got != hashMaxWaiters {
		t.Fatalf("waiters = %d, want %d", got, hashMaxWaiters)
	}

	close(release)
	released = true
	for i := 0; i < hashMaxWaiters; i++ {
		select {
		case r := <-results:
			if r.rec.Code != http.StatusOK {
				t.Fatalf("queued login: status %d body %s, want 200", r.rec.Code, r.rec.Body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("queued logins did not finish")
		}
	}
	if got := hashQueueLen(); got != 0 {
		t.Fatalf("waiters after drain = %d, want 0", got)
	}
	start := time.Now()
	e.login(pw)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("login after release took %v", d)
	}
}

// fillHashQueue marks the queue full for the duration of the test.
func fillHashQueue(t *testing.T) {
	t.Helper()
	hashWaiters.Add(hashMaxWaiters)
	t.Cleanup(func() { hashWaiters.Add(-hashMaxWaiters) })
}

func assertBusy(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"busy, retry shortly"}` {
		t.Fatalf("status %d Retry-After %q body %s, want 503 busy", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
}

// TestHashQueueBoundedRoutes: setup, password change, export and import all answer 503
// busy when the hash queue is full, and change nothing.
func TestHashQueueBoundedRoutes(t *testing.T) {
	t.Run("setup", func(t *testing.T) {
		e := newTestEnv(t)
		fillHashQueue(t)
		assertBusy(t, e.do("POST", "/api/setup", `{"password":"correct-horse-1"}`))
		if e.store.Get().Settings.AdminPasswordHash != "" {
			t.Fatal("setup stored a hash while busy")
		}
	})
	t.Run("password change", func(t *testing.T) {
		e, a, _ := twoSessions(t)
		before := snap(e)
		fillHashQueue(t)
		assertBusy(t, a.call("PUT", "/api/settings",
			`{"ui_bind_addr":"127.0.0.1:9999","current_password":"`+cpOld+`","new_password":"brand-new-pass"}`))
		if snap(e) != before {
			t.Fatal("password change altered settings while busy")
		}
	})
	t.Run("export", func(t *testing.T) {
		x := newExportEnv(t)
		fillHashQueue(t)
		assertBusy(t, x.export(exportBody(exportAdminPW, config.ExportModeNoSecrets, nil)))
	})
	t.Run("import", func(t *testing.T) {
		x := newImportEnv(t)
		before := x.snap()
		fillHashQueue(t)
		assertBusy(t, x.importConfig([]byte(`{}`), exportAdminPW, nil))
		x.assertSame(t, before)
	})
}

// TestHashSlotsIsOne pins the human decision of 2026-10-14: one argon2 computation at a time.
func TestHashSlotsIsOne(t *testing.T) {
	if got := cap(hashSlots); got != 1 {
		t.Fatalf("cap(hashSlots) = %d, want 1", got)
	}
}
