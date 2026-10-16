package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// TestExportRequiresPassphrase guards the rule "no export with plaintext
// secrets": whatever mode and passphrase a client sends, a 200 export either
// needs the passphrase to open or is a no-secrets file, and never carries a secret.
func TestExportRequiresPassphrase(t *testing.T) {
	x := newExportEnv(t)
	modes := []string{"encrypted", "no-secrets", "plain", "plaintext", "none", "raw", "json", "", "Encrypted"}
	passes := []*string{nil, ptr(""), ptr(exportPassphrase)}
	n := 0
	for _, mode := range modes {
		for _, pass := range passes {
			n++
			name := fmt.Sprintf("%q/pass=%v", mode, pass != nil && *pass != "")
			rec := x.export(exportBody(exportAdminPW, mode, pass), withRemote(fmt.Sprintf("192.0.2.%d:1", n)))
			if rec.Code != http.StatusOK {
				continue
			}
			body := rec.Body.String()
			for _, s := range x.secrets {
				if strings.Contains(body, s) {
					t.Errorf("%s: 200 export contains secret %q in plaintext", name, s)
				}
			}
			_, got, err := config.OpenExport(rec.Body.Bytes(), nil, testKDF)
			if err == nil && got != config.ExportModeNoSecrets {
				t.Errorf("%s: opened without a passphrase as mode %q", name, got)
			}
			if err != nil && pass == nil {
				t.Errorf("%s: 200 without a passphrase is not a no-secrets file: %v", name, err)
			}
		}
	}
}
