package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// TestRestoredBackupOpensSetup: copying a pre-import backup over config.json and booting
// opens first-run setup, because backups omit the admin hash and session secret
// . Quarantined .bad-* bytes are never touched.
func TestRestoredBackupOpensSetup(t *testing.T) {
	dir := t.TempDir()
	t.Run("first boot", func(t *testing.T) {
		setupAndLogin(t, startRun(t, dir, testFS()))
	})
	s, err := config.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	backup, err := s.ReplaceWithBackup(s.Get())
	if err != nil {
		t.Fatalf("ReplaceWithBackup: %v", err)
	}
	bad := filepath.Join(dir, "config.json.bad-20261001T000000Z")
	badBytes := []byte(`{"settings":{"admin_password_hash":"keep me","session_secret":"keep me too"}`)
	if err := os.WriteFile(bad, badBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, backup))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	base := startRun(t, dir, testFS())
	if res, body := doReq(t, "POST", base+"/api/login", `{"password":"correct horse battery"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("old password login: status %d body %q, want 409", res.StatusCode, body)
	}
	if res, body := doReq(t, "POST", base+"/api/setup", `{"password":"new admin password"}`); res.StatusCode != http.StatusNoContent {
		t.Fatalf("setup after restore: status %d body %q, want 204", res.StatusCode, body)
	}
	if res, body := doReq(t, "POST", base+"/api/login", `{"password":"new admin password"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("new password login: status %d body %q", res.StatusCode, body)
	}
	if got, err := os.ReadFile(bad); err != nil || !bytes.Equal(got, badBytes) {
		t.Fatalf(".bad-* file changed (err=%v)", err)
	}
}
