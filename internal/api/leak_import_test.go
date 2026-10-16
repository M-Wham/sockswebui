package api

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// canaryKey returns a valid base64 32-byte WireGuard key whose text starts with the
// distinctive prefix (base64 alphabet only), so a leak is unmistakable in any haystack.
func canaryKey(t *testing.T, prefix string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := make([]byte, 42-len(prefix))
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	k := prefix + string(b) + "A=" // 43rd char 'A' keeps the unused low bits zero
	if raw, err := base64.StdEncoding.Strict().DecodeString(k); err != nil || len(raw) != 32 {
		t.Fatalf("canary key %q invalid: %v", k, err)
	}
	return k
}

func zipBytes(t *testing.T, ups ...upload) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, u := range ups {
		fw, err := zw.Create(u.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(u.data))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

type importSweep struct {
	ids     []string // endpoint ids created by the import
	secrets []string // private/preshared keys that went into the uploads
}

// sweepImport drives POST /api/endpoints/import through the leak recorder.
func (l *leakRecorder) sweepImport() importSweep {
	t := l.a.t
	t.Helper()
	var sw importSweep
	key := func(prefix string) string {
		k := canaryKey(t, prefix)
		sw.secrets = append(sw.secrets, k)
		return k
	}
	single := importConf(key("LeakCanaryImpPrivA"), key("LeakCanaryImpPskA"), fakeKey(t), "198.51.100.20:51820")
	other := importConf(key("LeakCanaryImpPrivB"), key("LeakCanaryImpPskB"), fakeKey(t), "203.0.113.20:51820")
	zipped := importConf(key("LeakCanaryImpPrivZ"), key("LeakCanaryImpPskZ"), fakeKey(t), "198.51.100.21:51820")
	// Malformed: valid private key but the peer has no Endpoint.
	bad := strings.Replace(importConf(key("LeakCanaryImpPrivX"), key("LeakCanaryImpPskX"), fakeKey(t), "x:1"),
		"Endpoint = x:1\n", "", 1)
	zb := zipBytes(t, upload{"mullvad_wg/de-fra-wg-002.conf", zipped}, upload{"mullvad_wg/../evil.conf", single})
	zipOK := zipBytes(t, upload{"mullvad_wg/de-fra-wg-002.conf", zipped})

	body, ct := multipartBody(t, "files",
		upload{"se-got-wg-001.conf", single}, upload{"home.conf", other}, upload{"bad.conf", bad},
		upload{"mullvad.zip", zipOK}, upload{"slip.zip", zb}, upload{"dup.conf", single})
	rec := l.importCall(body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	var res struct {
		Results []importResultJSON `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, r := range res.Results {
		statuses[r.Status]++
		if r.ID != "" {
			sw.ids = append(sw.ids, r.ID)
		}
	}
	if statuses["imported"] != 3 || statuses["skipped-duplicate"] != 1 || statuses["error"] != 2 {
		t.Fatalf("import statuses = %v (%s)", statuses, rec.Body)
	}
	l.importCall(body, ct) // re-import: all duplicates/errors
	return sw
}

func (l *leakRecorder) importCall(body, ct string) *httptest.ResponseRecorder {
	l.a.t.Helper()
	rec := l.a.importRaw(body, ct, withCookie(l.a.cookie), withCSRF(l.a.csrf))
	l.record("POST", "/api/endpoints/import", rec)
	return rec
}

// countWireguard counts stored endpoints whose private key is one of keys.
func countWireguard(eps []config.Endpoint, keys []string) int {
	n := 0
	for _, e := range eps {
		for _, k := range keys {
			if e.PrivateKey == k {
				n++
			}
		}
	}
	return n
}
