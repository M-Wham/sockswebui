package api

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// openSessionStream opens /api/events with the session cookie and reads the snapshot.
func openSessionStream(t *testing.T, ts *httptest.Server, c *http.Cookie) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/api/events", nil)
	req.AddCookie(c)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	br := bufio.NewReader(res.Body)
	if f := readFrame(t, br); f.event != "snapshot" {
		t.Fatalf("first frame %+v", f)
	}
	return res, br
}

// expectClosed fails unless the stream ends within a few keepalive intervals.
func expectClosed(t *testing.T, br *bufio.Reader) {
	t.Helper()
	got := make(chan error, 1)
	go func() {
		for {
			if _, err := br.ReadString('\n'); err != nil {
				got <- err
				return
			}
		}
	}()
	select {
	case err := <-got:
		if err != io.EOF {
			t.Logf("stream ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after the session was revoked")
	}
}

func TestEventsEndOnSessionRevoke(t *testing.T) {
	setup := func(t *testing.T) (*authed, *authed, *httptest.Server) {
		e, a, b := twoSessions(t)
		e.srv.sseKeepalive = 40 * time.Millisecond
		ts := httptest.NewServer(e.h)
		t.Cleanup(ts.Close)
		return &authed{testEnv: e, cookie: a.cookie, csrf: a.csrf}, b, ts
	}
	t.Run("PasswordChange", func(t *testing.T) {
		a, b, ts := setup(t)
		_, br := openSessionStream(t, ts, b.cookie)
		waitSubscribers(t, a.ctrl.bus, 1)
		body := `{"ui_bind_addr":"127.0.0.1:9999","current_password":"` + cpOld + `","new_password":"brand-new-pw"}`
		if rec := a.call("PUT", "/api/settings", body); rec.Code != http.StatusOK {
			t.Fatalf("change password: %d %s", rec.Code, rec.Body)
		}
		expectClosed(t, br)
		waitSubscribers(t, a.ctrl.bus, 0)
	})
	t.Run("Logout", func(t *testing.T) {
		a, b, ts := setup(t)
		_, br := openSessionStream(t, ts, b.cookie)
		if rec := b.call("POST", "/api/logout", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("logout: %d", rec.Code)
		}
		expectClosed(t, br)
		waitSubscribers(t, a.ctrl.bus, 0)
	})
	t.Run("Expiry", func(t *testing.T) {
		a, b, ts := setup(t)
		_, br := openSessionStream(t, ts, b.cookie)
		a.srv.sessions.mu.Lock()
		a.srv.sessions.now = func() time.Time { return time.Now().Add(sessionTTL + time.Minute) }
		a.srv.sessions.mu.Unlock()
		expectClosed(t, br)
		waitSubscribers(t, a.ctrl.bus, 0)
	})
	t.Run("FreesCapSlot", func(t *testing.T) {
		a, b, ts := setup(t)
		a.srv.sseSlots = make(chan struct{}, 1) // cap of one stream
		_, br := openSessionStream(t, ts, b.cookie)
		req, _ := http.NewRequest("GET", ts.URL+"/api/events", nil)
		req.AddCookie(a.cookie)
		full, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("GET events: %v", err)
		}
		full.Body.Close()
		if full.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("at cap: %d, want 503", full.StatusCode)
		}
		if rec := b.call("POST", "/api/logout", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("logout: %d", rec.Code)
		}
		expectClosed(t, br)
		deadline := time.Now().Add(3 * time.Second)
		for {
			res, cancel := openStream(t, ts, a)
			code := res.StatusCode
			cancel()
			res.Body.Close()
			if code == http.StatusOK {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("after revoke: %d, want 200", code)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}
