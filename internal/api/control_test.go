package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

const testSocksPass = "socks-pass-Zq81x"

// slotFixture creates a WG endpoint and saves slot id routed through it with
// SOCKS auth; it returns the endpoint id and its private key.
func slotFixture(t *testing.T, a *authed, id int, enabled bool) (epID, priv string) {
	t.Helper()
	priv = fakeKey(t)
	ep := a.create(wgBody(fmt.Sprintf("se-got-wg-00%d", id), priv, fakeKey(t), fakeKey(t)))
	epID = ep["id"].(string)
	body := fmt.Sprintf(`{"name":"s%d","enabled":%t,"bind_addr":"127.0.0.1","port":%d,"route":[%q],`+
		`"killswitch":"block","socks_auth":{"username":"u","password":%q}}`, id, enabled, 1080+id, epID, testSocksPass)
	if rec := a.call("PUT", fmt.Sprintf("/api/slots/%d", id), body); rec.Code != http.StatusOK {
		t.Fatalf("PUT slot %d: %d %s", id, rec.Code, rec.Body)
	}
	return epID, priv
}

func storedSlot(t *testing.T, st *config.Store, id int) config.Slot {
	t.Helper()
	for _, sl := range st.Get().Slots {
		if sl.ID == id {
			return sl
		}
	}
	t.Fatalf("slot %d not stored", id)
	return config.Slot{}
}

func TestSlotStartStop(t *testing.T) {
	a := newAuthed(t)
	slotFixture(t, a, 1, false)

	if rec := a.do("POST", "/api/slots/1/start", "", withCookie(a.cookie)); rec.Code != http.StatusForbidden {
		t.Fatalf("start without CSRF: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/api/slots/1/stop", "", withCookie(a.cookie)); rec.Code != http.StatusForbidden {
		t.Fatalf("stop without CSRF: %d %s", rec.Code, rec.Body)
	}
	if starts, stops, _ := a.ctrl.calls(); len(starts)+len(stops) != 0 {
		t.Fatalf("controller called without CSRF: starts %v stops %v", starts, stops)
	}
	for _, p := range []string{"/api/slots/9/start", "/api/slots/x/stop", "/api/slots/2/start", "/api/slots/2/stop"} {
		if rec := a.call("POST", p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d %s, want 404", p, rec.Code, rec.Body)
		}
	}

	rec := a.call("POST", "/api/slots/1/start", "")
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if m := decodeMap(t, rec); m["id"] != float64(1) || m["state"] != string(supervisor.StateStarting) {
		t.Errorf("start body: %v", m)
	}
	if starts, _, _ := a.ctrl.calls(); !slices.Equal(starts, []int{1}) {
		t.Fatalf("Start calls = %v, want [1]", starts)
	}
	if !storedSlot(t, a.store, 1).Enabled {
		t.Error("start did not persist enabled=true")
	}

	rec = a.call("POST", "/api/slots/1/stop", "")
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	if _, stops, _ := a.ctrl.calls(); !slices.Equal(stops, []int{1}) {
		t.Fatalf("Stop calls = %v, want [1]", stops)
	}
	if storedSlot(t, a.store, 1).Enabled {
		t.Error("stop did not persist enabled=false")
	}
	if strings.Contains(rec.Body.String(), testSocksPass) {
		t.Error("stop response leaks the SOCKS password")
	}
}

// A WG endpoint held by another running slot refuses the start with a 409
// field error naming the slot and endpoint.
func TestSlotStartStopEndpointInUse(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 2, false)
	a.ctrl.startErr = fmt.Errorf("slot 2: %w", &supervisor.EndpointInUseError{Slot: 1, Endpoint: epID})

	rec := a.call("POST", "/api/slots/2/start", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("start: %d %s, want 409", rec.Code, rec.Body)
	}
	errs := decodeFieldErrors(t, rec)
	if len(errs) != 1 || errs[0].Field != "route[0]" ||
		!strings.Contains(errs[0].Message, epID) || !strings.Contains(errs[0].Message, "slot 1") {
		t.Fatalf("field errors = %+v", errs)
	}
	if storedSlot(t, a.store, 2).Enabled {
		t.Error("refused start persisted enabled=true")
	}
}

func TestSlotLogs(t *testing.T) {
	a := newAuthed(t)
	_, priv := slotFixture(t, a, 1, false)
	a.ctrl.logs[1] = []supervisor.LogLine{
		{Time: time.Unix(1, 0).UTC(), Level: "INFO", Msg: "slot state", Text: "msg=\"slot state\" to=UP"},
		{Time: time.Unix(2, 0).UTC(), Level: "ERROR", Msg: "leak " + priv, Text: "pw=" + testSocksPass},
	}
	if rec := a.do("GET", "/api/slots/1/logs", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("logs without session: %d", rec.Code)
	}
	if rec := a.call("GET", "/api/slots/7/logs", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("logs unknown slot: %d", rec.Code)
	}
	rec := a.call("GET", "/api/slots/1/logs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Lines []supervisor.LogLine `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Lines) != 2 || body.Lines[0].Msg != "slot state" || body.Lines[1].Level != "ERROR" {
		t.Fatalf("lines = %+v", body.Lines)
	}
	for _, sec := range []string{priv, testSocksPass} {
		if strings.Contains(rec.Body.String(), sec) {
			t.Fatalf("logs response contains a secret: %s", rec.Body)
		}
	}
	rec = a.call("GET", "/api/slots/3/logs", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"lines":[]`) {
		t.Fatalf("empty logs: %d %s", rec.Code, rec.Body)
	}
}

func TestSlotSaveRestarts(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 1, true)
	_, _, before := a.ctrl.calls()
	body := fmt.Sprintf(`{"name":"renamed","enabled":true,"bind_addr":"127.0.0.1","port":1090,"route":[%q],`+
		`"killswitch":"block","socks_auth":{"username":"u"}}`, epID)
	if rec := a.call("PUT", "/api/slots/1", body); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	a.ctrl.mu.Lock()
	defer a.ctrl.mu.Unlock()
	if len(a.ctrl.applies) != before+1 {
		t.Fatalf("Apply calls = %d, want %d", len(a.ctrl.applies), before+1)
	}
	got := a.ctrl.applies[len(a.ctrl.applies)-1]
	if len(got.Slots) != 1 || got.Slots[0].Port != 1090 || len(got.Endpoints) != 1 {
		t.Fatalf("Apply got %+v, want the saved config", got.Slots)
	}
}

// slotPutBody is slotFixture's PUT body for slot id with the given enabled flag.
func slotPutBody(id int, enabled bool, epID string) string {
	return fmt.Sprintf(`{"name":"s%d","enabled":%t,"bind_addr":"127.0.0.1","port":%d,"route":[%q],`+
		`"killswitch":"block","socks_auth":{"username":"u"}}`, id, enabled, 1080+id, epID)
}

// Enabling a slot via PUT whose WG endpoint is held by another running slot
// is refused like POST start: 409, nothing persisted, no Apply.
func TestSlotPutEnableEndpointInUse(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 2, false)
	a.ctrl.checkErr = &supervisor.EndpointInUseError{Slot: 1, Endpoint: epID}
	_, _, appliesBefore := a.ctrl.calls()
	path := filepath.Join(a.dir, config.ConfigFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rec := a.call("PUT", "/api/slots/2", slotPutBody(2, true, epID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT: %d %s, want 409", rec.Code, rec.Body)
	}
	if m := decodeMap(t, rec); m["error"] != "wireguard endpoint in use" {
		t.Errorf("error = %v", m["error"])
	}
	errs := decodeFieldErrors(t, rec)
	if len(errs) != 1 || errs[0].Field != "route[0]" ||
		!strings.Contains(errs[0].Message, epID) || !strings.Contains(errs[0].Message, "slot 1") {
		t.Fatalf("field errors = %+v", errs)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("refused PUT changed the store file")
	}
	if storedSlot(t, a.store, 2).Enabled {
		t.Error("refused PUT persisted enabled=true")
	}
	if _, _, n := a.ctrl.calls(); n != appliesBefore {
		t.Errorf("Apply called %d times on refused PUT", n-appliesBefore)
	}

	// Same body shape as the start endpoint's 409.
	a.ctrl.startErr = a.ctrl.checkErr
	srec := a.call("POST", "/api/slots/2/start", "")
	if srec.Code != http.StatusConflict || srec.Body.String() != rec.Body.String() {
		t.Errorf("start 409 %d %s differs from PUT 409 %s", srec.Code, srec.Body, rec.Body)
	}
}

func TestSlotPutEnableNoConflict(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 2, false)
	a.ctrl.mu.Lock()
	a.ctrl.checks = nil
	a.ctrl.mu.Unlock()

	if rec := a.call("PUT", "/api/slots/2", slotPutBody(2, false, epID)); rec.Code != http.StatusOK {
		t.Fatalf("PUT disabled: %d %s", rec.Code, rec.Body)
	}
	a.ctrl.mu.Lock()
	n := len(a.ctrl.checks)
	a.ctrl.mu.Unlock()
	if n != 0 {
		t.Fatalf("CheckStart called %d times for enabled=false", n)
	}

	_, _, appliesBefore := a.ctrl.calls()
	if rec := a.call("PUT", "/api/slots/2", slotPutBody(2, true, epID)); rec.Code != http.StatusOK {
		t.Fatalf("PUT enabled: %d %s", rec.Code, rec.Body)
	}
	if !storedSlot(t, a.store, 2).Enabled {
		t.Error("PUT did not persist enabled=true")
	}
	if _, _, n := a.ctrl.calls(); n != appliesBefore+1 {
		t.Errorf("Apply calls = %d, want %d", n, appliesBefore+1)
	}
	a.ctrl.mu.Lock()
	defer a.ctrl.mu.Unlock()
	if len(a.ctrl.checks) != 1 || a.ctrl.checks[0].ID != 2 || !a.ctrl.checks[0].Enabled {
		t.Fatalf("CheckStart calls = %+v, want one for slot 2", a.ctrl.checks)
	}
}
