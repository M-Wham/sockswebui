package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func openStream(t *testing.T, ts *httptest.Server, a *authed) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.AddCookie(a.cookie)
	res, err := ts.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	return res, cancel
}

func TestEventsNoSession(t *testing.T) {
	a := newAuthed(t)
	for i := 0; i < 40; i++ {
		if rec := a.do("GET", "/api/events", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("no cookie: %d, want 401", rec.Code)
		}
	}
	if n := a.ctrl.bus.Subscribers(); n != 0 {
		t.Fatalf("subscribers = %d, want 0", n)
	}
	// Anonymous requests must not have consumed slots.
	ts := httptest.NewServer(a.h)
	defer ts.Close()
	res, cancel := openStream(t, ts, a)
	defer cancel()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("authed stream: %d", res.StatusCode)
	}
}

func TestEventsSubscriberCap(t *testing.T) {
	a := newAuthed(t)
	ts := httptest.NewServer(a.h)
	defer ts.Close()

	type stream struct {
		res    *http.Response
		cancel context.CancelFunc
	}
	open := func(n int) []stream {
		var out []stream
		for i := 0; i < n; i++ {
			res, cancel := openStream(t, ts, a)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("stream %d: %d", i+1, res.StatusCode)
			}
			out = append(out, stream{res, cancel})
		}
		return out
	}
	closeAll := func(ss []stream) {
		for _, s := range ss {
			s.cancel()
			s.res.Body.Close()
		}
		waitSubscribers(t, a.ctrl.bus, 0)
	}

	ss := open(maxEventStreams)
	waitSubscribers(t, a.ctrl.bus, maxEventStreams)
	res, cancel := openStream(t, ts, a)
	buf := new(strings.Builder)
	b := make([]byte, 512)
	n, _ := res.Body.Read(b)
	buf.Write(b[:n])
	res.Body.Close()
	cancel()
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(buf.String(), `{"error":"too many event streams"}`) {
		t.Fatalf("17th: %d %q", res.StatusCode, buf)
	}
	if n := a.ctrl.bus.Subscribers(); n != maxEventStreams {
		t.Fatalf("subscribers = %d after rejection", n)
	}

	// Closing one stream frees a slot.
	ss[0].cancel()
	ss[0].res.Body.Close()
	waitSubscribers(t, a.ctrl.bus, maxEventStreams-1)
	more := open(1)
	closeAll(append(ss[1:], more...))

	// Sequential churn does not leak slots.
	for i := 0; i < 100; i++ {
		closeAll(open(1))
	}
	closeAll(open(maxEventStreams))
}
