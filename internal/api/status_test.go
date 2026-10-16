package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/supervisor"
)

var statusKeys = []string{"state", "exit_ip", "country", "latency_ms", "last_error", "since", "checked_at"}

func statusOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	st, ok := m["status"].(map[string]any)
	if !ok {
		t.Fatalf("no status object in %v", m)
	}
	for _, k := range statusKeys {
		if _, ok := st[k]; !ok {
			t.Fatalf("status lacks key %q: %v", k, st)
		}
	}
	if len(st) != len(statusKeys) {
		t.Fatalf("status has extra keys: %v", st)
	}
	return st
}

func TestSlotGetStatus(t *testing.T) {
	a := newAuthed(t)
	slotFixture(t, a, 1, false)

	// Nothing known yet: state STOPPED, every other field null.
	st := statusOf(t, decodeMap(t, a.call("GET", "/api/slots/1", "")))
	if st["state"] != "STOPPED" {
		t.Errorf("state = %v, want STOPPED", st["state"])
	}
	for _, k := range statusKeys[1:] {
		if st[k] != nil {
			t.Errorf("%s = %v, want null", k, st[k])
		}
	}
	// An id that was never saved has a status too.
	statusOf(t, decodeMap(t, a.call("GET", "/api/slots/3", "")))

	since := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	checked := since.Add(30 * time.Second)
	a.ctrl.setStatus(1, supervisor.StateUp, "probe ok: exit 198.51.100.7", since, &supervisor.ProbeStatus{
		CheckedAt: checked, ExitIP: "198.51.100.7", Country: "Sweden", LatencyMS: 42})
	st = statusOf(t, decodeMap(t, a.call("GET", "/api/slots/1", "")))
	if st["state"] != "UP" || st["exit_ip"] != "198.51.100.7" || st["country"] != "Sweden" ||
		st["latency_ms"] != float64(42) || st["last_error"] != nil ||
		st["since"] != since.Format(time.RFC3339Nano) || st["checked_at"] != checked.Format(time.RFC3339Nano) {
		t.Fatalf("status = %v", st)
	}

	// A failed probe: no exit, the error is shown.
	a.ctrl.setStatus(1, supervisor.StateDegraded, "kill switch", since, &supervisor.ProbeStatus{
		CheckedAt: checked, Error: "health: probe: dial timeout"})
	st = statusOf(t, decodeMap(t, a.call("GET", "/api/slots/1", "")))
	if st["state"] != "DEGRADED" || st["exit_ip"] != nil || st["latency_ms"] != nil ||
		st["last_error"] != "health: probe: dial timeout" {
		t.Fatalf("degraded status = %v", st)
	}

	// An ERROR slot without a probe shows its message as last_error.
	a.ctrl.setStatus(1, supervisor.StateError, "listen: address in use", since, nil)
	st = statusOf(t, decodeMap(t, a.call("GET", "/api/slots/1", "")))
	if st["last_error"] != "listen: address in use" || st["checked_at"] != nil {
		t.Fatalf("error status = %v", st)
	}
}

func TestSlotListStatus(t *testing.T) {
	a := newAuthed(t)
	slotFixture(t, a, 1, false)
	slotFixture(t, a, 2, false)
	a.ctrl.setStatus(2, supervisor.StateUp, "ok", time.Now(), &supervisor.ProbeStatus{
		CheckedAt: time.Now(), ExitIP: "203.0.113.9", Country: "Norway", LatencyMS: 7})
	rec := a.call("GET", "/api/slots", "")
	var body struct {
		Slots []map[string]any `json:"slots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Slots) != 4 {
		t.Fatalf("list: %v %s", err, rec.Body)
	}
	for _, sl := range body.Slots {
		statusOf(t, sl)
	}
	if st := body.Slots[1]["status"].(map[string]any); st["state"] != "UP" || st["exit_ip"] != "203.0.113.9" {
		t.Fatalf("slot 2 status = %v", st)
	}
	if st := body.Slots[0]["status"].(map[string]any); st["state"] != "STOPPED" || st["exit_ip"] != nil {
		t.Fatalf("slot 1 status = %v", st)
	}
}

// A PUT body that carries a status object (a GET echoed back) is accepted and the
// status is neither stored nor changed.
func TestSlotPutIgnoresStatus(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 1, false)
	a.ctrl.setStatus(1, supervisor.StateUp, "ok", time.Now(), &supervisor.ProbeStatus{
		CheckedAt: time.Now(), ExitIP: "198.51.100.7", LatencyMS: 5})
	body := strings.Replace(slotPutBody(1, false, epID), `{"name"`,
		`{"status":{"state":"ERROR","exit_ip":"6.6.6.6","last_error":"forged","latency_ms":1},"name"`, 1)
	rec := a.call("PUT", "/api/slots/1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if st := statusOf(t, decodeMap(t, rec)); st["state"] != "UP" || st["exit_ip"] != "198.51.100.7" || st["last_error"] != nil {
		t.Fatalf("PUT response status = %v", st)
	}
	raw, _ := json.Marshal(a.store.Get())
	for _, bad := range []string{"forged", "6.6.6.6"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("status stored in config: %q", bad)
		}
	}
	if st := statusOf(t, decodeMap(t, a.call("GET", "/api/slots/1", ""))); st["state"] != "UP" {
		t.Fatalf("GET after PUT status = %v", st)
	}
}

// The first frame on connect is a snapshot with every slot's state and last health.
func TestEventsSnapshotOnConnect(t *testing.T) {
	a := newAuthed(t)
	slotFixture(t, a, 1, false)
	slotFixture(t, a, 2, false)
	since := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	a.ctrl.setStatus(1, supervisor.StateUp, "ok", since, &supervisor.ProbeStatus{
		CheckedAt: since.Add(time.Minute), ExitIP: "198.51.100.7", Country: "Sweden", LatencyMS: 42})
	a.ctrl.setStatus(2, supervisor.StateError, "listen: address in use", since, nil)
	ts := httptest.NewServer(a.h)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.AddCookie(a.cookie)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	f := nextEvent(t, bufio.NewReader(res.Body))
	if f.event != "snapshot" {
		t.Fatalf("first frame = %+v", f)
	}
	var snap struct {
		Slots []map[string]any `json:"slots"`
	}
	if err := json.Unmarshal([]byte(f.data), &snap); err != nil || len(snap.Slots) != 2 {
		t.Fatalf("snapshot %q: %v", f.data, err)
	}
	s1, s2 := snap.Slots[0], snap.Slots[1]
	if s1["id"] != float64(1) || s1["state"] != "UP" {
		t.Fatalf("slot 1 = %v", s1)
	}
	st := statusOf(t, s1)
	if st["state"] != "UP" || st["exit_ip"] != "198.51.100.7" || st["latency_ms"] != float64(42) || st["country"] != "Sweden" {
		t.Fatalf("slot 1 status = %v", st)
	}
	if st := statusOf(t, s2); st["state"] != "ERROR" || st["last_error"] != "listen: address in use" || st["exit_ip"] != nil {
		t.Fatalf("slot 2 status = %v", st)
	}
}

// last_error is built from upstream error text, which can echo the proxy
// credentials. It is scrubbed of every configured secret and of session IDs and
// rendered usernames, in GET responses and in the snapshot alike.
func TestStatusLastErrorRedacted(t *testing.T) {
	a := newAuthed(t)
	_, wgPriv := slotFixture(t, a, 1, false)
	hp := a.create(`{"name":"sticky","kind":"http","host":"203.0.113.5","port":3128,` +
		`"username":"cust-{session}","password":"UpstreamPW-x9","session":{"charset":"alnum","length":10}}`)
	epID := hp["id"].(string)
	ep := a.store.Get().Endpoints
	var sid, renderedUser, renderedPW string
	for _, e := range ep {
		if e.ID == epID {
			sid = e.Session.CurrentID
			renderedUser = strings.ReplaceAll(e.Username, "{session}", sid)
			renderedPW = strings.ReplaceAll(e.Password, "{session}", sid)
		}
	}
	if sid == "" {
		t.Fatal("no session id was generated")
	}
	if rec := a.call("PUT", "/api/slots/2", `{"name":"s2","enabled":false,"bind_addr":"127.0.0.1","port":1090,"route":["`+epID+`"],"killswitch":"block"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT slot 2: %d %s", rec.Code, rec.Body)
	}
	leaky := "proxy auth failed for " + renderedUser + ":" + renderedPW + " session " + sid + " key " + wgPriv + " pw " + testSocksPass
	a.ctrl.setStatus(2, supervisor.StateError, "build route: "+leaky, time.Now(), nil)
	a.ctrl.setStatus(1, supervisor.StateDegraded, "kill switch", time.Now(), &supervisor.ProbeStatus{
		CheckedAt: time.Now(), Error: leaky})

	check := func(where, text string) {
		t.Helper()
		for name, s := range map[string]string{"session id": sid, "rendered user": renderedUser,
			"rendered password": renderedPW, "wg key": wgPriv, "socks password": testSocksPass} {
			if strings.Contains(text, s) {
				t.Errorf("%s leaks %s: %s", where, name, text)
			}
		}
		if !strings.Contains(text, "[REDACTED]") {
			t.Errorf("%s: no redaction mark in %s", where, text)
		}
	}
	for _, p := range []string{"/api/slots/1", "/api/slots/2", "/api/slots"} {
		check("GET "+p, a.call("GET", p, "").Body.String())
	}
	ts := httptest.NewServer(a.h)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.AddCookie(a.cookie)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	check("snapshot", nextEvent(t, bufio.NewReader(res.Body)).data)
}

func TestStatusExitIPNotMangled(t *testing.T) {
	red := strings.NewReplacer("7", "[REDACTED]")
	st := &supervisor.SlotStatus{State: supervisor.StateUp, Probe: &supervisor.ProbeStatus{
		CheckedAt: time.Now(), ExitIP: "185.7.1.2", Country: "SE"}}
	v := toStatusView(st, red)
	if v.ExitIP == nil || *v.ExitIP != "185.7.1.2" {
		t.Errorf("exit_ip mangled: %v", v.ExitIP)
	}
	st = &supervisor.SlotStatus{State: supervisor.StateDegraded, Probe: &supervisor.ProbeStatus{
		CheckedAt: time.Now(), Error: "auth failed for cust-7"}}
	v = toStatusView(st, red)
	if v.LastError == nil || strings.Contains(*v.LastError, "7") {
		t.Errorf("last_error not scrubbed: %v", v.LastError)
	}
}
