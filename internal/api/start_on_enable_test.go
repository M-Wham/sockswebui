package api

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

func setStartOnEnable(t *testing.T, a *authed, on bool) {
	t.Helper()
	if err := a.store.Update(func(c *config.Config) error { c.Settings.StartOnEnable = on; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsStartOnEnableAPI(t *testing.T) {
	a := newAuthed(t)
	if m := decodeMap(t, a.call("GET", "/api/settings", "")); m["start_on_enable"] != true {
		t.Fatalf("GET start_on_enable = %v, want true", m["start_on_enable"])
	}
	bind := fmt.Sprintf(`"ui_bind_addr":%q`, config.DefaultUIBindAddr)
	rec := a.call("PUT", "/api/settings", `{`+bind+`,"start_on_enable":false}`)
	if rec.Code != http.StatusOK || decodeMap(t, rec)["start_on_enable"] != false {
		t.Fatalf("PUT false: %d %s", rec.Code, rec.Body)
	}
	if a.store.Get().Settings.StartOnEnable {
		t.Error("PUT false not stored")
	}
	// A body without the field leaves it alone.
	rec = a.call("PUT", "/api/settings", `{`+bind+`}`)
	if rec.Code != http.StatusOK || decodeMap(t, rec)["start_on_enable"] != false {
		t.Fatalf("PUT absent: %d %s", rec.Code, rec.Body)
	}
	rec = a.call("PUT", "/api/settings", `{`+bind+`,"start_on_enable":true}`)
	if rec.Code != http.StatusOK || decodeMap(t, rec)["start_on_enable"] != true || !a.store.Get().Settings.StartOnEnable {
		t.Fatalf("PUT true: %d %s", rec.Code, rec.Body)
	}
}

func TestSlotPutEnableStarts(t *testing.T) {
	put := func(a *authed, epID string, enabled bool) int {
		return a.call("PUT", "/api/slots/1", slotPutBody(1, enabled, epID)).Code
	}
	t.Run("stopped slot starts", func(t *testing.T) {
		a := newAuthed(t)
		epID, _ := slotFixture(t, a, 1, false)
		if code := put(a, epID, true); code != http.StatusOK {
			t.Fatalf("PUT: %d", code)
		}
		if starts, _, _ := a.ctrl.calls(); !slices.Equal(starts, []int{1}) {
			t.Fatalf("Start calls = %v, want [1]", starts)
		}
		if a.ctrl.states[1] != supervisor.StateStarting {
			t.Errorf("state = %v, want STARTING", a.ctrl.states[1])
		}
		seq := a.ctrl.callSeq()
		if seq[len(seq)-2] != "apply" || seq[len(seq)-1] != "start 1" {
			t.Errorf("order = %v, want apply then start", seq)
		}
	})
	t.Run("error slot starts", func(t *testing.T) {
		a := newAuthed(t)
		epID, _ := slotFixture(t, a, 1, false)
		a.ctrl.setState(1, supervisor.StateError)
		if code := put(a, epID, true); code != http.StatusOK {
			t.Fatalf("PUT: %d", code)
		}
		if starts, _, _ := a.ctrl.calls(); !slices.Equal(starts, []int{1}) {
			t.Fatalf("Start calls = %v, want [1]", starts)
		}
		if a.ctrl.states[1] != supervisor.StateStarting {
			t.Errorf("state = %v, want STARTING", a.ctrl.states[1])
		}
	})
	t.Run("disable stops", func(t *testing.T) {
		a := newAuthed(t)
		epID, _ := slotFixture(t, a, 1, true)
		a.ctrl.setState(1, supervisor.StateUp)
		if code := put(a, epID, false); code != http.StatusOK {
			t.Fatalf("PUT: %d", code)
		}
		if _, stops, _ := a.ctrl.calls(); !slices.Equal(stops, []int{1}) {
			t.Fatalf("Stop calls = %v, want [1]", stops)
		}
		if a.ctrl.states[1] != supervisor.StateStopped {
			t.Errorf("state = %v, want STOPPED", a.ctrl.states[1])
		}
	})
	t.Run("no transition no start", func(t *testing.T) {
		a := newAuthed(t)
		epID, _ := slotFixture(t, a, 1, true)
		a.ctrl.setState(1, supervisor.StateStopped)
		startsBefore, _, _ := a.ctrl.calls() // the fixture save itself started the slot
		if code := put(a, epID, true); code != http.StatusOK {
			t.Fatalf("PUT: %d", code)
		}
		if starts, stops, _ := a.ctrl.calls(); len(starts) != len(startsBefore) || len(stops) != 0 {
			t.Fatalf("starts %v stops %v, want no new calls (enabled did not change)", starts, stops)
		}
	})
	t.Run("shared wg endpoint refused", func(t *testing.T) {
		a := newAuthed(t)
		epID, _ := slotFixture(t, a, 1, false)
		a.ctrl.checkErr = &supervisor.EndpointInUseError{Slot: 2, Endpoint: epID}
		rec := a.call("PUT", "/api/slots/1", slotPutBody(1, true, epID))
		if rec.Code != http.StatusConflict {
			t.Fatalf("PUT: %d %s, want 409", rec.Code, rec.Body)
		}
		if !hasField(decodeFieldErrors(t, rec), "route[0]") {
			t.Errorf("no route[0] field error: %s", rec.Body)
		}
		if starts, _, _ := a.ctrl.calls(); len(starts) != 0 {
			t.Errorf("Start called: %v", starts)
		}
		if storedSlot(t, a.store, 1).Enabled {
			t.Error("refused PUT persisted enabled=true")
		}
	})
	t.Run("endpoint taken between check and start", func(t *testing.T) {
		a := newAuthed(t)
		epID, _ := slotFixture(t, a, 1, false)
		a.ctrl.startErr = &supervisor.EndpointInUseError{Slot: 2, Endpoint: epID}
		rec := a.call("PUT", "/api/slots/1", slotPutBody(1, true, epID))
		if rec.Code != http.StatusConflict {
			t.Fatalf("PUT: %d %s, want 409", rec.Code, rec.Body)
		}
		if storedSlot(t, a.store, 1).Enabled {
			t.Error("late 409 left enabled=true persisted")
		}
		last := a.ctrl.applies[len(a.ctrl.applies)-1]
		if last.Slots[0].Enabled {
			t.Error("rollback was not applied to the controller")
		}
	})
}

// Stop and rotate wait for startMu like start does.
func TestStopRotateHoldStartMu(t *testing.T) {
	a := newAuthed(t)
	slotFixture(t, a, 1, true)
	sess := a.create(sessBody("u-{session}", "pw", sess10))["id"].(string)
	for name, call := range map[string]func() int{
		"stop":   func() int { return a.call("POST", "/api/slots/1/stop", "").Code },
		"rotate": func() int { return a.call("POST", "/api/endpoints/"+sess+"/rotate", "").Code },
	} {
		a.srv.startMu.Lock()
		done := make(chan int, 1)
		go func() { done <- call() }()
		select {
		case code := <-done:
			t.Fatalf("%s finished (%d) while startMu was held", name, code)
		case <-time.After(100 * time.Millisecond):
		}
		a.srv.startMu.Unlock()
		if code := <-done; code != http.StatusOK {
			t.Errorf("%s: %d", name, code)
		}
	}
}

func TestSlotPutEnableNoStartWhenOff(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 1, false)
	setStartOnEnable(t, a, false)
	a.ctrl.setState(1, supervisor.StateStopped)
	if rec := a.call("PUT", "/api/slots/1", slotPutBody(1, true, epID)); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	if !storedSlot(t, a.store, 1).Enabled {
		t.Error("enabled not stored")
	}
	if a.ctrl.states[1] != supervisor.StateStopped {
		t.Errorf("state = %v, want unchanged STOPPED", a.ctrl.states[1])
	}
	a.ctrl.setState(1, supervisor.StateUp)
	if rec := a.call("PUT", "/api/slots/1", slotPutBody(1, false, epID)); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if a.ctrl.states[1] != supervisor.StateUp {
		t.Errorf("state = %v, want unchanged UP", a.ctrl.states[1])
	}
	if starts, stops, _ := a.ctrl.calls(); len(starts)+len(stops) != 0 {
		t.Errorf("starts %v stops %v, want none", starts, stops)
	}
}

func TestSlotPutEnableMissingSecrets(t *testing.T) {
	a := newAuthed(t)
	epID, _ := slotFixture(t, a, 1, false)
	omitSecrets(t, a, epID, 1, []string{config.OmittedPrivateKey}, nil)
	rec := a.call("PUT", "/api/slots/1", slotPutBody(1, true, epID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT: %d %s, want 400", rec.Code, rec.Body)
	}
	if starts, _, _ := a.ctrl.calls(); len(starts) != 0 {
		t.Errorf("Start called: %v", starts)
	}
	if storedSlot(t, a.store, 1).Enabled {
		t.Error("enabled persisted")
	}
}

// POST start and stop set enabled, whatever start_on_enable says, and the
// start endpoint starts the slot exactly once.
func TestSlotStartStopSetEnabled(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("start_on_enable=%t", on), func(t *testing.T) {
			a := newAuthed(t)
			slotFixture(t, a, 1, false)
			setStartOnEnable(t, a, on)
			if rec := a.call("POST", "/api/slots/1/start", ""); rec.Code != http.StatusAccepted {
				t.Fatalf("start: %d %s", rec.Code, rec.Body)
			}
			if !storedSlot(t, a.store, 1).Enabled {
				t.Error("start did not persist enabled=true")
			}
			if starts, _, _ := a.ctrl.calls(); !slices.Equal(starts, []int{1}) {
				t.Errorf("Start calls = %v, want exactly [1]", starts)
			}
			if rec := a.call("POST", "/api/slots/1/stop", ""); rec.Code != http.StatusOK {
				t.Fatalf("stop: %d %s", rec.Code, rec.Body)
			}
			if storedSlot(t, a.store, 1).Enabled {
				t.Error("stop did not persist enabled=false")
			}
			if _, stops, _ := a.ctrl.calls(); !slices.Equal(stops, []int{1}) {
				t.Errorf("Stop calls = %v, want exactly [1]", stops)
			}
		})
	}
}
