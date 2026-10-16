package api

import (
	"net/http"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// TestImportRejectsInjectedProxyHost: a config import file whose proxy host carries CR/LF
// is rejected and changes nothing.
func TestImportRejectsInjectedProxyHost(t *testing.T) {
	x := newImportEnv(t)
	p := x.payload()
	for i := range p.Endpoints {
		if p.Endpoints[i].Kind == config.KindSOCKS5 {
			p.Endpoints[i].Host = "a.test\r\nX-Evil: 1"
		}
	}
	before := x.snap()
	rec := x.importConfig(sealFile(t, p, "encrypted"), exportAdminPW, ptr(exportPassphrase))
	if rec.Code == http.StatusOK {
		t.Fatalf("got 200 %s, want a rejection", rec.Body)
	}
	x.assertSame(t, before)
}
