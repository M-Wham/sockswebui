package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// putWait bounds how long the start hook waits for the concurrent PUTs. Without startMu in
// the PUT handlers they finish well inside it; with it they park on the lock and the hook
// gives up, letting start finish first.
const putWait = 300 * time.Millisecond

// TestStartUpdateRace fires a slot PUT (reroute slot 1 onto an endpoint whose secrets are
// missing, enabled=false) and an endpoint PUT on that endpoint between start's locked re-check
// and ctrl.Start. The PUTs must serialize with the start:
// ctrl.Start never runs on an unstartable applied config, start answers 202 (it won) or 409
// (the PUTs won), and the slot ends disabled.
func TestStartUpdateRace(t *testing.T) {
	a := newAuthed(t)
	slotFixture(t, a, 1, false)
	missingID := missingSecretsEndpoint(t, a)

	slotBody := fmt.Sprintf(`{"name":"s1","enabled":false,"bind_addr":"127.0.0.1","port":1081,"route":[%q],`+
		`"killswitch":"block","socks_auth":{"username":"u","password":%q}}`, missingID, testSocksPass)
	epBody := wgBody("se-got-wg-009-renamed", "", "", fakeKey(t))
	puts := make(chan *httptest.ResponseRecorder, 2)
	var done []*httptest.ResponseRecorder // PUTs that finished inside the hook
	ran := false
	a.srv.startRechecked = func() {
		if ran {
			return
		}
		ran = true
		go func() { puts <- a.call("PUT", "/api/slots/1", slotBody) }()
		go func() { puts <- a.call("PUT", "/api/endpoints/"+missingID, epBody) }()
		deadline := time.After(putWait)
		for len(done) < 2 {
			select {
			case rec := <-puts:
				done = append(done, rec)
			case <-deadline:
				return
			}
		}
	}

	rec := a.call("POST", "/api/slots/1/start", "")
	if !ran {
		t.Fatal("startRechecked hook did not run")
	}
	for len(done) < 2 {
		done = append(done, <-puts)
	}
	for _, put := range done {
		if put.Code != http.StatusOK {
			t.Errorf("concurrent PUT: %d %s", put.Code, put.Body)
		}
	}
	switch rec.Code {
	case http.StatusAccepted, http.StatusConflict:
	default:
		t.Errorf("start: %d %s, want 202 or 409", rec.Code, rec.Body)
	}
	assertNoUnstartableStart(t, a.ctrl, 1)
	if rec.Code == http.StatusConflict {
		if st := a.srv.slotStatus(1); st.State != supervisor.StateStopped {
			t.Errorf("slot state = %s after 409, want STOPPED", st.State)
		}
	}
	if storedSlot(t, a.store, 1).Enabled {
		t.Error("stored slot enabled after race")
	}
}

// missingSecretsEndpoint creates a WireGuard endpoint whose private key is listed as omitted,
// as a no-secrets config import leaves it.
func missingSecretsEndpoint(t *testing.T, a *authed) string {
	t.Helper()
	id := a.create(wgBody("se-got-wg-009", fakeKey(t), "", fakeKey(t)))["id"].(string)
	err := a.store.Update(func(c *config.Config) error {
		i := endpointIndex(c.Endpoints, id)
		c.Endpoints[i].PrivateKey = ""
		c.Endpoints[i].SecretsOmitted = []string{config.OmittedPrivateKey}
		return nil
	})
	if err != nil {
		t.Fatalf("mark secrets missing: %v", err)
	}
	return id
}

// assertNoUnstartableStart fails if ctrl.Start(id) ran while the config last applied to the
// controller had slot id missing secrets.
func assertNoUnstartableStart(t *testing.T, f *fakeCtrl, id int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	applied := 0
	for _, op := range f.seq {
		switch op {
		case "apply":
			applied++
		case fmt.Sprintf("start %d", id):
			if applied == 0 {
				continue
			}
			cfg := f.applies[applied-1]
			i := slices.IndexFunc(cfg.Slots, func(s config.Slot) bool { return s.ID == id })
			if i >= 0 && config.SlotSecretsMissing(cfg, cfg.Slots[i]) {
				t.Errorf("ctrl.Start(%d) ran on an applied config with secrets missing (seq %v)", id, f.seq)
			}
		}
	}
}
