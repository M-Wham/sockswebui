package api

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// TestImportRoundTrip exports from device A through the API and imports into device B, a
// second store and server with its own admin hash, session secret and UI address.
func TestImportRoundTrip(t *testing.T) {
	for _, mode := range []string{"encrypted", "no-secrets"} {
		t.Run(mode, func(t *testing.T) {
			a := newExportEnv(t)
			a.create(sessBody("u-{session}", "pw", sess10))
			if err := a.store.Update(func(c *config.Config) error { c.Slots[0].Enabled = true; return nil }); err != nil {
				t.Fatal(err)
			}
			var pass *string
			if mode == "encrypted" {
				pass = ptr(exportPassphrase)
			}
			rec := a.export(exportBody(exportAdminPW, mode, pass))
			if rec.Code != http.StatusOK {
				t.Fatalf("export: %d %s", rec.Code, rec.Body)
			}
			file := rec.Body.Bytes()
			want := a.store.Get()

			b := newImportEnv(t)
			if err := b.store.Update(func(c *config.Config) error {
				c.Settings.UIBindAddr = "127.0.0.1:9090"
				c.Settings.StartOnEnable = false
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			slotFixture(t, b.authed, 3, false) // B's own config, which the import must replace
			bBefore := b.store.Get()

			decodeImportResult(t, b.importConfig(file, exportAdminPW, pass))
			got := b.store.Get()

			wantEPs, wantSlots := want.Endpoints, want.Slots
			if mode == "no-secrets" {
				p := config.StripSecrets(config.ExportPayload{Endpoints: want.Endpoints, Slots: want.Slots})
				wantEPs, wantSlots = p.Endpoints, p.Slots
			}
			wantSlots = append([]config.Slot(nil), wantSlots...)
			for i := range wantSlots {
				if !wantSlots[i].Enabled && i == 0 {
					t.Fatal("fixture: A's slot 1 should be enabled")
				}
				wantSlots[i].Enabled = false
			}
			if !reflect.DeepEqual(got.Endpoints, wantEPs) {
				t.Errorf("endpoints:\n got %+v\nwant %+v", got.Endpoints, wantEPs)
			}
			if !reflect.DeepEqual(got.Slots, wantSlots) {
				t.Errorf("slots:\n got %+v\nwant %+v", got.Slots, wantSlots)
			}
			if got.Endpoints[2].Session.CurrentID != want.Endpoints[2].Session.CurrentID {
				t.Errorf("current_id %q, want A's %q", got.Endpoints[2].Session.CurrentID, want.Endpoints[2].Session.CurrentID)
			}
			if got.Settings != bBefore.Settings || got.Settings.UIBindAddr != "127.0.0.1:9090" || got.Settings.StartOnEnable {
				t.Errorf("B's settings changed: %+v -> %+v", bBefore.Settings, got.Settings)
			}
			if got.Settings.AdminPasswordHash == want.Settings.AdminPasswordHash {
				t.Fatal("fixture: A and B share an admin hash")
			}
			// B's session cookie and CSRF token still work.
			if rec := b.call("GET", "/api/settings", ""); rec.Code != http.StatusOK {
				t.Errorf("B session after import: %d", rec.Code)
			}
			if rec := b.call("POST", "/api/slots/1/stop", ""); rec.Code != http.StatusOK {
				t.Errorf("B CSRF after import: %d %s", rec.Code, rec.Body)
			}
		})
	}
}
