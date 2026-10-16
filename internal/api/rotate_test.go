package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/supervisor"
)

type rotEnv struct {
	*authed
	now time.Time
	id  string // rotating endpoint
}

func newRotEnv(t *testing.T) *rotEnv {
	t.Helper()
	r := &rotEnv{authed: newAuthed(t), now: time.Unix(1_700_000_000, 0)}
	r.srv.rotateLimiter.now = func() time.Time { return r.now }
	r.id = r.create(sessBody("u", "PW-{session}-SECRETPW", sess10))["id"].(string)
	return r
}

func (r *rotEnv) putSlot(n int, route ...string) {
	r.t.Helper()
	if rec := r.call("PUT", fmt.Sprintf("/api/slots/%d", n), slotBody(1080+n, route...)); rec.Code != http.StatusOK {
		r.t.Fatalf("put slot %d: %d %s", n, rec.Code, rec.Body)
	}
}

func (r *rotEnv) rotate(id string) (int, map[string]any, http.Header) {
	rec := r.call("POST", "/api/endpoints/"+id+"/rotate", "")
	return rec.Code, decodeMap(r.t, rec), rec.Header()
}

func (r *rotEnv) current() string {
	for _, e := range r.store.Get().Endpoints {
		if e.ID == r.id {
			return e.Session.CurrentID
		}
	}
	return ""
}

func TestRotateAuth(t *testing.T) {
	r := newRotEnv(t)
	p := "/api/endpoints/" + r.id + "/rotate"
	if rec := r.do("POST", p, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d", rec.Code)
	}
	if rec := r.do("POST", p, "", withCookie(r.cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("no csrf: %d", rec.Code)
	}
}

func TestRotateCheckOrder(t *testing.T) {
	r := newRotEnv(t)
	plain := r.create(sessBody("u", "pw", ""))["id"].(string)

	if c, _, _ := r.rotate("ep-nope"); c != http.StatusNotFound {
		t.Errorf("unknown: %d", c)
	}
	if c, _, _ := r.rotate(plain); c != http.StatusConflict {
		t.Errorf("no template: %d", c)
	}
	// 404 and 409 did not count: valid rotate succeeds.
	if c, _, _ := r.rotate(r.id); c != http.StatusOK {
		t.Fatalf("first rotate: %d", c)
	}
	c, body, h := r.rotate(r.id)
	if c != http.StatusTooManyRequests {
		t.Fatalf("second rotate: %d", c)
	}
	if n, err := strconv.Atoi(h.Get("Retry-After")); err != nil || n != 5 {
		t.Errorf("Retry-After %q", h.Get("Retry-After"))
	}
	if body["error"] == nil {
		t.Errorf("no error body: %v", body)
	}
	// unknown id and no-template stay 404/409 while the limiter is hot elsewhere.
	if c, _, _ := r.rotate("ep-nope"); c != http.StatusNotFound {
		t.Errorf("unknown+limited: %d", c)
	}
	if c, _, _ := r.rotate(plain); c != http.StatusConflict {
		t.Errorf("no template: %d", c)
	}
	r.now = r.now.Add(2 * time.Second)
	if _, _, h := r.rotate(r.id); h.Get("Retry-After") != "3" {
		t.Errorf("Retry-After after 2s: %q", h.Get("Retry-After"))
	}
	r.now = r.now.Add(3 * time.Second)
	if c, _, _ := r.rotate(r.id); c != http.StatusOK {
		t.Errorf("after window: %d", c)
	}
}

func TestRotateSuccess(t *testing.T) {
	r := newRotEnv(t)
	other := r.create(sessBody("o", "pw", ""))["id"].(string)
	r.putSlot(1, r.id)
	r.putSlot(2, other, r.id)
	r.putSlot(3, r.id)
	r.putSlot(4, other)
	r.ctrl.states[1] = supervisor.StateUp
	r.ctrl.states[2] = supervisor.StateError
	r.ctrl.states[3] = supervisor.StateStopped
	r.ctrl.states[4] = supervisor.StateUp
	prev := r.current()
	_, _, applies := r.ctrl.calls()

	c, body, _ := r.rotate(r.id)
	if c != http.StatusOK {
		t.Fatalf("status %d %v", c, body)
	}
	cur, _ := body["current_id"].(string)
	if cur == "" || cur == prev || cur != r.current() {
		t.Errorf("current_id %q prev %q stored %q", cur, prev, r.current())
	}
	rs, _ := body["rebuilt_slots"].([]any)
	if len(rs) != 2 || rs[0] != float64(1) || rs[1] != float64(2) {
		t.Errorf("rebuilt_slots %v", body["rebuilt_slots"])
	}
	if _, _, a := r.ctrl.calls(); a != applies+1 {
		t.Errorf("applies %d -> %d", applies, a)
	}
}

func TestRotateEmptyRebuiltIsArray(t *testing.T) {
	r := newRotEnv(t)
	rec := r.call("POST", "/api/endpoints/"+r.id+"/rotate", "")
	if !strings.Contains(rec.Body.String(), `"rebuilt_slots":[]`) {
		t.Errorf("body %s", rec.Body)
	}
}

func TestRotateReaderFailure(t *testing.T) {
	r := newRotEnv(t)
	prev := r.current()
	r.srv.rand = failReader{}
	if c, _, _ := r.rotate(r.id); c != http.StatusInternalServerError {
		t.Fatalf("status %d", c)
	}
	if r.current() != prev {
		t.Error("config changed")
	}
	// failure did not count against the limit
	r.srv.rand = nil
	if c, _, _ := r.rotate(r.id); c != http.StatusOK {
		t.Errorf("retry: %d", c)
	}
}

func TestRotateNoLeak(t *testing.T) {
	r := newRotEnv(t)
	r.putSlot(1, r.id)
	r.ctrl.states[1] = supervisor.StateUp
	rec := r.call("POST", "/api/endpoints/"+r.id+"/rotate", "")
	cur := r.current()
	rendered := "PW-" + cur + "-SECRETPW"
	r.ctrl.logs[1] = append(r.ctrl.logs[1], supervisor.LogLine{Msg: "dial " + rendered, Text: rendered, Level: "info"})
	logs := r.call("GET", "/api/slots/1/logs", "").Body.String()
	for name, s := range map[string]string{"response": rec.Body.String(), "api logs": logs, "slog": r.logs.String()} {
		if strings.Contains(s, "SECRETPW") {
			t.Errorf("%s leaks password: %s", name, s)
		}
	}
}

func TestRotateApplyErrorStill200(t *testing.T) {
	r := newRotEnv(t)
	r.ctrl.applyErr = errors.New("endpoint in use")
	c, body, _ := r.rotate(r.id)
	if c != http.StatusOK || body["current_id"] != r.current() {
		t.Errorf("status %d body %v", c, body)
	}
}
