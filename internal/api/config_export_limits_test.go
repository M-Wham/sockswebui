package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExportLimiter(t *testing.T) {
	wrongExport := exportBody("wrong-password", "no-secrets", nil)
	wrongLogin := `{"password":"wrong-password"}`
	for _, sixth := range []string{"login", "export", "export-oversize"} {
		t.Run(sixth, func(t *testing.T) {
			x := newExportEnv(t)
			clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
			x.srv.loginLimiter.now = clk.now
			ip := withRemote("198.51.100.7:5555")
			for i := 0; i < loginLimit; i++ {
				var code int
				if i%2 == 0 {
					code = x.export(wrongExport, ip).Code
				} else {
					code = x.do("POST", "/api/login", wrongLogin, ip).Code
				}
				if code != http.StatusBadRequest && code != http.StatusUnauthorized {
					t.Fatalf("attempt %d: %d", i+1, code)
				}
			}
			x.frees.Store(0)
			var code int
			var retry string
			switch sixth {
			case "login":
				rec := x.do("POST", "/api/login", `{"password":"`+exportAdminPW+`"}`, ip)
				code, retry = rec.Code, rec.Header().Get("Retry-After")
			case "export":
				rec := x.export(exportBody(exportAdminPW, "no-secrets", nil), ip)
				code, retry = rec.Code, rec.Header().Get("Retry-After")
			case "export-oversize":
				rec := x.export(exportBody(exportAdminPW, "encrypted", ptr(strings.Repeat("a", maxExportBody))), ip)
				code, retry = rec.Code, rec.Header().Get("Retry-After")
			}
			if code != http.StatusTooManyRequests || retry == "" {
				t.Fatalf("6th %s: %d Retry-After %q, want 429 with Retry-After", sixth, code, retry)
			}
			if n := x.frees.Load(); n != 0 {
				t.Errorf("hash ran %d times on a 429", n)
			}
			// Another client is unaffected.
			if rec := x.export(exportBody(exportAdminPW, "no-secrets", nil), withRemote("198.51.100.8:1")); rec.Code != http.StatusOK {
				t.Errorf("other IP: %d", rec.Code)
			}
		})
	}
}

func TestExportLogs(t *testing.T) {
	const wrongAdmin = "ExportCanary-WrongAdmin-77"
	x := newExportEnv(t)
	var contents []string
	check := func(t *testing.T, wantLines int, mode string) {
		t.Helper()
		logs := x.logs.String()
		if n := strings.Count(logs, `msg="config exported"`); n != wantLines {
			t.Errorf("config exported lines = %d, want %d:\n%s", n, wantLines, logs)
		}
		if wantLines == 1 && !strings.Contains(logs, `msg="config exported" mode=`+mode+" remote=192.0.2.1\n") {
			t.Errorf("missing config exported mode=%s:\n%s", mode, logs)
		}
		var slotLogs strings.Builder
		for id := 1; id <= 4; id++ {
			slotLogs.WriteString(x.call("GET", fmt.Sprintf("/api/slots/%d/logs", id), "").Body.String())
		}
		for _, s := range append(append([]string{exportPassphrase, exportAdminPW, wrongAdmin}, x.secrets...), contents...) {
			if strings.Contains(logs, s) {
				t.Errorf("slog contains %q", s)
			}
			if strings.Contains(slotLogs.String(), s) {
				t.Errorf("/api/slots/*/logs contains %q", s)
			}
		}
	}
	failures := []string{
		exportBody(wrongAdmin, "encrypted", ptr(exportPassphrase)),
		exportBody(wrongAdmin, "no-secrets", nil),
		exportBody(exportAdminPW, "bogus", ptr(exportPassphrase)),
		exportBody(exportAdminPW, "no-secrets", ptr(exportPassphrase)),
		exportBody(exportAdminPW, "encrypted", ptr(exportPassphrase[:11])),
		exportBody(exportAdminPW, "encrypted", ptr(exportPassphrase+strings.Repeat("z", maxExportBody))),
	}
	for i, b := range failures {
		x.srv.loginLimiter = newRateLimiter(loginLimit, loginWindow, loginMaxClients)
		if rec := x.export(b); rec.Code == http.StatusOK {
			t.Fatalf("failure case %d succeeded", i)
		}
	}
	check(t, 0, "")
	for _, mode := range []string{"encrypted", "no-secrets"} {
		x.logs.Reset()
		var pass *string
		if mode == "encrypted" {
			pass = ptr(exportPassphrase)
		}
		rec := x.export(exportBody(exportAdminPW, mode, pass))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", mode, rec.Code, rec.Body)
		}
		var f struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		contents = append(contents, string(f.Payload), x.store.Get().Endpoints[0].PeerPublicKey)
		check(t, 1, mode)
	}
}

// holdHashSlots fills every hashSlots slot; the returned func frees one slot per call.
func holdHashSlots(t *testing.T) func() {
	t.Helper()
	for i := 0; i < cap(hashSlots); i++ {
		select {
		case hashSlots <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatal("could not take a hash slot")
		}
	}
	held := cap(hashSlots)
	t.Cleanup(func() {
		for ; held > 0; held-- {
			<-hashSlots
		}
	})
	return func() { held--; <-hashSlots }
}

func TestExportKDFSemaphore(t *testing.T) {
	x := newExportEnv(t)
	release := holdHashSlots(t)
	kdf := x.srv.exportKDF(context.Background())
	type res struct {
		key []byte
		err error
	}
	done := make(chan res, 1)
	go func() {
		k, err := kdf([]byte(exportPassphrase), make([]byte, 16), 3, 16384, 2)
		done <- res{k, err}
	}()
	select {
	case <-done:
		t.Fatal("key derivation ran while the hash slot was held")
	case <-time.After(150 * time.Millisecond):
	}
	if n := x.frees.Load(); n != 0 {
		t.Fatalf("FreeOSMemory called %d times before derivation", n)
	}
	release()
	var r res
	select {
	case r = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("derivation did not finish after a slot was released")
	}
	if r.err != nil || len(r.key) != 32 {
		t.Fatalf("kdf: %d bytes, err %v", len(r.key), r.err)
	}
	if n := x.frees.Load(); n != 1 {
		t.Errorf("FreeOSMemory calls = %d, want 1", n)
	}
	if len(hashSlots) != 0 {
		t.Errorf("hashSlots in use = %d, want 0 (derivation freed its slot)", len(hashSlots))
	}

	// A cancelled request stops waiting without deriving.
	_ = holdHashSlots(t)
	ctx, cancel := context.WithCancel(context.Background())
	kdf = x.srv.exportKDF(ctx)
	errc := make(chan error, 1)
	go func() { _, err := kdf([]byte(exportPassphrase), make([]byte, 16), 3, 16384, 2); errc <- err }()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled kdf err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled kdf still waiting")
	}
	if n := x.frees.Load(); n != 1 {
		t.Errorf("FreeOSMemory calls = %d after cancel, want 1", n)
	}
}
