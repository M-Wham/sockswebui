package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostAllowListGuard(t *testing.T) {
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	})
	h := HostGuard(next, NewNameList("pibox", "ui.example"))
	rows := []struct {
		host string
		want int
	}{
		{"127.0.0.1:8080", 204},
		{"[::1]:8080", 204},
		{"localhost", 204},
		{"pibox.local:8080", 204},
		{"ui.example", 204},
		{"evil.example", 403},
		{"ui.example.evil.test", 403},
		{"", 403},
	}
	for _, r := range rows {
		for _, method := range []string{"GET", "POST"} {
			called = 0
			req := httptest.NewRequest(method, "/api/login", strings.NewReader("{}"))
			req.Host = r.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != r.want {
				t.Errorf("%s Host %q: status %d, want %d", method, r.host, rec.Code, r.want)
			}
			if r.want == 403 {
				if called != 0 {
					t.Errorf("%s Host %q: next ran on a refused host", method, r.host)
				}
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("403 body not JSON: %q", rec.Body.String())
				}
				if body["error"] != "host not allowed" {
					t.Errorf("error = %q", body["error"])
				}
				if !strings.Contains(body["hint"], "--ui-host") || !strings.Contains(body["hint"], "localhost") {
					t.Errorf("hint does not name the accepted forms: %q", body["hint"])
				}
				if strings.Contains(rec.Body.String(), "pibox") {
					t.Error("403 body leaks the device hostname")
				}
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("content-type %q", ct)
				}
			} else if called != 1 {
				t.Errorf("%s Host %q: next called %d times", method, r.host, called)
			}
		}
	}
}

func TestSSEKeepaliveIgnoresNonPositive(t *testing.T) {
	for _, d := range []int64{0, -1} {
		s := &Server{sseKeepalive: 7}
		WithSSEKeepalive(time.Duration(d))(s)
		if s.sseKeepalive != 7 {
			t.Errorf("WithSSEKeepalive(%d) changed interval to %v", d, s.sseKeepalive)
		}
	}
}
