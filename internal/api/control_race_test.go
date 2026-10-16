package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// TestStartImportRace lands a no-secrets config import between the start handler's
// secrets check and ctrl.Start. The slot must not run with secrets missing (fail closed):
// the start gets 409, the slot stays STOPPED and stays disabled.
func TestStartImportRace(t *testing.T) {
	x := newImportEnv(t)
	slotFixture(t, x.authed, 1, false)
	file := sealFile(t, x.payload(), config.ExportModeNoSecrets)

	ran := false
	x.srv.startChecked = func() {
		if ran {
			return
		}
		ran = true
		resp := decodeImportResult(t, x.importConfig(file, exportAdminPW, nil))
		if len(resp.SecretsMissing.Endpoints) == 0 {
			t.Fatalf("import left no secrets missing: %+v", resp)
		}
	}

	rec := x.authed.call("POST", "/api/slots/1/start", "")
	if !ran {
		t.Fatal("startChecked hook did not run")
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "secrets missing") {
		t.Errorf("start: %d %s, want 409 secrets missing", rec.Code, rec.Body)
	}
	if st := x.srv.slotStatus(1); st.State != supervisor.StateStopped {
		t.Errorf("slot state = %s, want STOPPED", st.State)
	}
	if storedSlot(t, x.store, 1).Enabled {
		t.Error("stored slot enabled after race")
	}
}
