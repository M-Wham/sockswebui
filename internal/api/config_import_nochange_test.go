package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// flipCiphertext flips one bit of the decoded ciphertext and re-encodes it, so the file
// stays well-formed and only authentication fails.
func flipCiphertext(t *testing.T, raw []byte) []byte {
	t.Helper()
	return mutateFile(t, raw, func(m map[string]any) {
		ct, err := base64.StdEncoding.DecodeString(m["payload"].(string))
		if err != nil {
			t.Fatal(err)
		}
		ct[len(ct)/2] ^= 0x01
		m["payload"] = base64.StdEncoding.EncodeToString(ct)
	})
}

func TestImportNothingChanges(t *testing.T) {
	x := newImportEnv(t)
	x.ctrl.setState(1, supervisor.StateUp) // a running slot that must not be stopped
	enc := sealFile(t, x.payload(), "encrypted")
	ns := sealFile(t, x.payload(), "no-secrets")
	badID := mutateFile(t, ns, func(m map[string]any) {
		obj(m, "payload")["endpoints"].([]any)[0].(map[string]any)["id"] = "ep-xyz"
	})
	uiPort := mutateFile(t, ns, func(m map[string]any) {
		obj(m, "payload")["slots"].([]any)[0].(map[string]any)["port"] = 8787 // this device's UI port
	})

	type tc struct {
		name      string
		file      []byte
		pass      *string
		code      int
		wantField string
	}
	cases := []tc{
		{"WrongPassphrase", enc, ptr(exportPassphrase + "x"), 400, ""},
		{"FlippedByte", flipCiphertext(t, enc), ptr(exportPassphrase), 400, ""},
		{"EditedKDFT", mutateFile(t, enc, func(m map[string]any) { obj(m, "kdf")["t"] = 4 }), ptr(exportPassphrase), 400, ""},
		{"EditedVersion", mutateFile(t, enc, func(m map[string]any) { m["format_version"] = 2 }), ptr(exportPassphrase), 400, ""},
		{"EditedMode", mutateFile(t, enc, func(m map[string]any) { m["mode"] = "no-secrets" }), ptr(exportPassphrase), 400, ""},
		{"InvalidEndpointID", badID, nil, 400, "endpoints[0].id"},
		{"UIPortConflict", uiPort, nil, 400, "slots[0].port"},
	}
	bodies := map[string][]byte{}
	before := x.snap()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := x.importConfig(c.file, exportAdminPW, c.pass)
			if rec.Code != c.code {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body, c.code)
			}
			bodies[c.name] = rec.Body.Bytes()
			if c.wantField != "" {
				found := false
				for _, fe := range decodeFieldErrors(t, rec) {
					found = found || fe.Field == c.wantField
				}
				if !found {
					t.Errorf("no field error on %s: %s", c.wantField, rec.Body)
				}
			}
			x.assertSame(t, before)
		})
	}
	if !bytes.Equal(bodies["WrongPassphrase"], bodies["FlippedByte"]) {
		t.Errorf("wrong passphrase and flipped byte differ:\n%s\n%s", bodies["WrongPassphrase"], bodies["FlippedByte"])
	}
	var eb errorBody
	if err := json.Unmarshal(bodies["WrongPassphrase"], &eb); err != nil || eb.Error != config.ErrDecrypt.Error() || len(eb.Errors) != 0 {
		t.Errorf("decrypt body = %s", bodies["WrongPassphrase"])
	}

	t.Run("BackupWriteFailure", func(t *testing.T) {
		// Mirrors the store: build runs, then the backup write fails before beforeCommit.
		x.srv.replaceConfig = func(build func(config.Config) (config.Config, error), beforeCommit func()) (string, error) {
			if _, err := build(x.store.Get()); err != nil {
				t.Errorf("build: %v", err)
			}
			return "", errors.New("write pre-import backup: disk full")
		}
		defer func() { x.srv.replaceConfig = nil }()
		if rec := x.importConfig(ns, exportAdminPW, nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s, want 500", rec.Code, rec.Body)
		}
		x.assertSame(t, before)
	})
	t.Run("BackupUnwritableDir", func(t *testing.T) {
		// The real store and writeAtomic run; the backup write fails because a regular
		// file sits where the data dir was (ENOTDIR holds even for root).
		restore := replaceDirWithFile(t, x.dir)
		rec := x.importConfig(ns, exportAdminPW, nil)
		restore()
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s, want 500", rec.Code, rec.Body)
		}
		x.assertSame(t, before)
	})
}

// replaceDirWithFile moves dir aside and puts a regular file at its path, so any write
// into dir fails even for root. The returned func puts dir back; it is also registered
// with t.Cleanup and is safe to call twice.
func replaceDirWithFile(t *testing.T, dir string) func() {
	t.Helper()
	aside := dir + ".aside"
	if err := os.Rename(dir, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := false
	restore := func() {
		if done {
			return
		}
		done = true
		if err := os.Remove(dir); err != nil {
			t.Errorf("remove placeholder: %v", err)
		}
		if err := os.Rename(aside, dir); err != nil {
			t.Errorf("restore data dir: %v", err)
		}
	}
	t.Cleanup(restore)
	return restore
}
