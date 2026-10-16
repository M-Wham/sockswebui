package api

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestImportRemovesMultipartTempFiles: parts above the 64 KiB in-memory threshold spill
// to TMPDIR; none may survive the request, on success or on rejection.
func TestImportRemovesMultipartTempFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	a := newAuthed(t)
	conf := importConf(fakeKey(t), fakeKey(t), fakeKey(t), "198.51.100.10:51820")
	pad := strings.Repeat("# padding\n", 20<<10) // ~200 KiB, forces a temp file
	res := decodeImport(t, a.importFiles(upload{"se-got-wg-001.conf", pad + conf}, upload{"big.txt", pad}))
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	assertEmptyDir(t, tmp)

	big, ct := multipartBody(t, "files", upload{"a.conf", pad}, upload{"b.conf", strings.Repeat("#", 5<<20)})
	if rec := a.importRaw(big, ct, withCookie(a.cookie), withCSRF(a.csrf)); rec.Code != 413 {
		t.Errorf("oversize: status %d, want 413", rec.Code)
	}
	assertEmptyDir(t, tmp)
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("leftover temp file %s", e.Name())
	}
}

// TestImportConcurrentDedupe: interleaved imports of overlapping files exercise the
// in-Update re-dedupe; every distinct config is stored exactly once, and each file is
// reported imported by exactly one request.
func TestImportConcurrentDedupe(t *testing.T) {
	a := newAuthed(t)
	const workers, distinct = 16, 4
	confs := make([]upload, distinct)
	for i := range confs {
		confs[i] = upload{fmt.Sprintf("se-got-wg-%03d.conf", i+1),
			importConf(fakeKey(t), fakeKey(t), fakeKey(t), fmt.Sprintf("198.51.100.%d:51820", i+1))}
	}
	type outcome struct{ imported, skipped int }
	out := make([]outcome, workers)
	var wg sync.WaitGroup
	start := make(chan struct{}) // release all workers at once to maximise interleaving
	for w := 0; w < workers; w++ {
		// Each worker uploads all confs, rotated so batches interleave differently.
		ups := append(append([]upload{}, confs[w%distinct:]...), confs[:w%distinct]...)
		body, ct := multipartBody(t, "files", ups...)
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			rec := a.importRaw(body, ct, withCookie(a.cookie), withCSRF(a.csrf))
			if rec.Code != 200 {
				t.Errorf("worker %d: %d %s", w, rec.Code, rec.Body)
				return
			}
			for _, r := range decodeImportNoFatal(t, rec.Body.Bytes()) {
				switch r.Status {
				case "imported":
					out[w].imported++
				case "skipped-duplicate":
					out[w].skipped++
				default:
					t.Errorf("worker %d: unexpected %+v", w, r)
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	total := 0
	for w, o := range out {
		total += o.imported
		if o.imported+o.skipped != distinct {
			t.Errorf("worker %d: %+v, want %d results", w, o, distinct)
		}
	}
	if total != distinct {
		t.Errorf("imported across workers = %d, want %d", total, distinct)
	}
	if n := len(a.store.Get().Endpoints); n != distinct {
		t.Errorf("stored endpoints = %d, want %d", n, distinct)
	}
}

// TestImportRededupeInsideUpdate: an endpoint stored between parsing and the store
// update must be re-detected as a duplicate inside Update, not inserted twice.
func TestImportRededupeInsideUpdate(t *testing.T) {
	a := newAuthed(t)
	dup := importConf(fakeKey(t), fakeKey(t), fakeKey(t), "198.51.100.10:51820")
	fresh := importConf(fakeKey(t), fakeKey(t), fakeKey(t), "198.51.100.11:51820")
	var racer []importResultJSON
	a.srv.importParsed = func() {
		a.srv.importParsed = nil // the interleaved import must not recurse
		racer = decodeImport(t, a.importFiles(upload{"se-got-wg-001.conf", dup}))
	}
	res := decodeImport(t, a.importFiles(upload{"se-got-wg-001.conf", dup}, upload{"se-got-wg-002.conf", fresh}))
	if len(racer) != 1 || racer[0].Status != "imported" {
		t.Fatalf("interleaved import = %+v", racer)
	}
	if len(res) != 2 || res[0].Status != "skipped-duplicate" || res[0].ID != "" ||
		res[1].Status != "imported" || res[1].ID == "" || res[1].ID == racer[0].ID {
		t.Errorf("results = %+v, want [skipped-duplicate, imported(new id)]", res)
	}
	if n := len(a.store.Get().Endpoints); n != 2 {
		t.Errorf("stored endpoints = %d, want 2", n)
	}
}

// decodeImportNoFatal is decodeImport for goroutines (no t.Fatal).
func decodeImportNoFatal(t *testing.T, b []byte) []importResultJSON {
	var body struct {
		Results []importResultJSON `json:"results"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Errorf("decode %q: %v", b, err)
	}
	return body.Results
}
