package importer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

// assertNoSecrets fails if any key material from opts appears in a result's text fields.
func assertNoSecrets(t *testing.T, results []Result, opts ...confOpts) {
	t.Helper()
	b, err := json.Marshal(struct {
		R []string
		W [][]string
	}{collectReasons(results), collectWarnings(results)})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		for _, k := range []string{o.PrivateKey, o.PresharedKey, o.PublicKey} {
			if k != "" && strings.Contains(string(b), k) {
				t.Fatalf("result text leaks key material")
			}
		}
	}
}

func collectReasons(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Reason)
	}
	return out
}

func collectWarnings(rs []Result) [][]string {
	var out [][]string
	for _, r := range rs {
		out = append(out, r.Warnings)
	}
	return out
}

func confFile(t *testing.T, name string, o confOpts) File {
	t.Helper()
	return File{Name: name, Data: []byte(confText(t, o))}
}

func TestImportBatch(t *testing.T) {
	o1, oHome, oZ1, oZ2 := validOpts(t), validOpts(t), validOpts(t), validOpts(t)
	oBad := validOpts(t)
	oBad.PublicKey = ""
	bundle := buildZip(t,
		zipEntry{"mullvad/se-got-wg-002.conf", []byte(confText(t, oZ1))},
		zipEntry{"mullvad/de-fra-wg-003.conf", []byte(confText(t, oZ2))},
	)
	uploads := []File{
		confFile(t, "se-got-wg-001.conf", o1),
		confFile(t, "home.conf", oHome),
		confFile(t, "bad.conf", oBad),
		{Name: "bundle.zip", Data: bundle},
	}
	results, added := Import(nil, uploads)

	want := []struct{ file, status string }{
		{"se-got-wg-001.conf", StatusImported},
		{"home.conf", StatusImported},
		{"bad.conf", StatusError},
		{"bundle.zip/mullvad/se-got-wg-002.conf", StatusImported},
		{"bundle.zip/mullvad/de-fra-wg-003.conf", StatusImported},
	}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d: %+v", len(results), len(want), results)
	}
	for i, w := range want {
		r := results[i]
		if r.File != w.file || r.Status != w.status {
			t.Errorf("result %d = %s/%s, want %s/%s (reason %q)", i, r.File, r.Status, w.file, w.status, r.Reason)
		}
		switch r.Status {
		case StatusImported:
			if r.Endpoint == nil || r.Reason != "" {
				t.Errorf("result %d: imported without endpoint or with reason %q", i, r.Reason)
			}
		case StatusError:
			if r.Reason == "" || r.Endpoint != nil {
				t.Errorf("result %d: error must have reason and no endpoint", i)
			}
		}
	}
	if len(results[1].Warnings) != 0 || len(results[0].Warnings) != 0 {
		t.Errorf("warnings: mullvad=%v home=%v; want none", results[0].Warnings, results[1].Warnings)
	}
	if !strings.Contains(results[2].Reason, "PublicKey") {
		t.Errorf("bad.conf reason = %q", results[2].Reason)
	}
	if len(added) != 4 {
		t.Fatalf("added = %d, want 4", len(added))
	}
	for i, r := range []int{0, 1, 3, 4} {
		if results[r].Endpoint != &added[i] {
			t.Errorf("result %d endpoint does not point at added[%d]", r, i)
		}
	}
	if added[2].MullvadRelay != "se-got-wg-002" || added[3].Country != "de" || added[1].Name != "home" {
		t.Errorf("parsed fields wrong: %+v / %+v / %+v", added[1].Name, added[2].MullvadRelay, added[3].Country)
	}
	for _, e := range added {
		if e.ID != "" {
			t.Errorf("Import must not assign ids, got %q", e.ID)
		}
	}
	assertNoSecrets(t, results, o1, oHome, oZ1, oZ2, oBad)
}

func TestImportDedupe(t *testing.T) {
	existingOpts := validOpts(t)
	existingOpts.Endpoint = "SE-GOT-WG-001.relays.example:51820"
	existing := []config.Endpoint{{
		ID: "ep1", Kind: config.KindWireGuard,
		PeerPublicKey: existingOpts.PublicKey, Endpoint: existingOpts.Endpoint,
	}}

	dupExisting := validOpts(t)
	dupExisting.PublicKey = existingOpts.PublicKey
	dupExisting.Endpoint = "se-got-wg-001.relays.example:51820" // hostname case differs

	twice := validOpts(t)
	twiceAgain := validOpts(t)
	twiceAgain.PublicKey, twiceAgain.Endpoint = twice.PublicKey, twice.Endpoint

	otherEndpoint := validOpts(t)
	otherEndpoint.PublicKey = existingOpts.PublicKey
	otherEndpoint.Endpoint = "198.51.100.99:51820"

	results, added := Import(existing, []File{
		confFile(t, "se-got-wg-001.conf", dupExisting),
		confFile(t, "a.conf", twice),
		confFile(t, "b.conf", twiceAgain),
		confFile(t, "c.conf", otherEndpoint),
	})
	want := []string{StatusSkippedDuplicate, StatusImported, StatusSkippedDuplicate, StatusImported}
	if len(results) != len(want) {
		t.Fatalf("got %d results: %+v", len(results), results)
	}
	for i, w := range want {
		if results[i].Status != w {
			t.Errorf("result %d (%s) = %s, want %s (%s)", i, results[i].File, results[i].Status, w, results[i].Reason)
		}
		if w == StatusSkippedDuplicate && results[i].Endpoint != nil {
			t.Errorf("result %d: duplicate must not reference an endpoint", i)
		}
	}
	if len(added) != 2 {
		t.Fatalf("added = %d, want 2", len(added))
	}
	if len(existing) != 1 {
		t.Fatalf("existing slice mutated")
	}
	assertNoSecrets(t, results, existingOpts, dupExisting, twice, twiceAgain, otherEndpoint)

	t.Run("IPv6 literal forms compare equal", func(t *testing.T) {
		a, b := validOpts(t), validOpts(t)
		a.Endpoint = "[2001:DB8::1]:51820"
		b.PublicKey, b.Endpoint = a.PublicKey, "[2001:db8:0::1]:51820"
		rs, _ := Import(nil, []File{confFile(t, "a.conf", a), confFile(t, "b.conf", b)})
		if rs[0].Status != StatusImported || rs[1].Status != StatusSkippedDuplicate {
			t.Fatalf("statuses = %s, %s", rs[0].Status, rs[1].Status)
		}
	})
}

func TestImportBadZip(t *testing.T) {
	good := validOpts(t)
	slip := buildZip(t, zipEntry{"../evil.conf", []byte(confText(t, validOpts(t)))})
	results, added := Import(nil, []File{
		{Name: "slip.zip", Data: slip},
		confFile(t, "se-got-wg-001.conf", good),
		{Name: "x.txt", Data: []byte("hello")},
		{Name: "mixed.zip", Data: buildZip(t, zipEntry{"README.txt", []byte("hi")})},
		{Name: "UPPER.CONF", Data: []byte(confText(t, validOpts(t)))},
	})
	want := []struct{ file, status string }{
		{"slip.zip", StatusError},
		{"se-got-wg-001.conf", StatusImported},
		{"x.txt", StatusError},
		{"mixed.zip/README.txt", StatusError},
		{"UPPER.CONF", StatusImported},
	}
	if len(results) != len(want) {
		t.Fatalf("got %d results: %+v", len(results), results)
	}
	for i, w := range want {
		if results[i].File != w.file || results[i].Status != w.status {
			t.Errorf("result %d = %s/%s, want %s/%s", i, results[i].File, results[i].Status, w.file, w.status)
		}
		if w.status == StatusError && results[i].Reason == "" {
			t.Errorf("result %d: error without reason", i)
		}
	}
	if !strings.Contains(results[0].Reason, "unsafe path") {
		t.Errorf("zip-slip reason = %q", results[0].Reason)
	}
	if !strings.Contains(results[2].Reason, "unsupported") {
		t.Errorf("x.txt reason = %q", results[2].Reason)
	}
	if len(added) != 2 {
		t.Fatalf("added = %d, want 2", len(added))
	}
}
