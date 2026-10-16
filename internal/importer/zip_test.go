package importer

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// zipEntry is one entry to write into a test zip. Names ending in "/" become directories.
type zipEntry struct {
	Name string
	Data []byte
}

// buildZip writes entries into an in-memory zip (deflate) and returns its bytes.
func buildZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: e.Name, Method: zip.Deflate})
		if err != nil {
			t.Fatalf("create %q: %v", e.Name, err)
		}
		if strings.HasSuffix(e.Name, "/") {
			continue
		}
		if _, err := fw.Write(e.Data); err != nil {
			t.Fatalf("write %q: %v", e.Name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// confEntries returns n Mullvad-style .conf entries with fresh fake keys.
func confEntries(t *testing.T, n int) []zipEntry {
	t.Helper()
	out := make([]zipEntry, n)
	for i := range out {
		out[i] = zipEntry{
			Name: fmt.Sprintf("mullvad_wg/se-got-wg-%03d.conf", i+1),
			Data: []byte(confText(t, validOpts(t))),
		}
	}
	return out
}

func TestExpandZip(t *testing.T) {
	confs := confEntries(t, 3)
	entries := append([]zipEntry{{Name: "mullvad_wg/"}}, confs...)
	files, err := ExpandZip(buildZip(t, entries...))
	if err != nil {
		t.Fatalf("ExpandZip: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3: %+v", len(files), files)
	}
	for i, f := range files {
		if f.Name != confs[i].Name {
			t.Errorf("file %d name = %q, want %q", i, f.Name, confs[i].Name)
		}
		if !bytes.Equal(f.Data, confs[i].Data) {
			t.Errorf("file %d data mismatch", i)
		}
		if f.Err != "" {
			t.Errorf("file %d unexpected Err %q", i, f.Err)
		}
	}
}

func TestExpandZipAccepts500(t *testing.T) {
	entries := make([]zipEntry, MaxZipEntries)
	for i := range entries {
		entries[i] = zipEntry{Name: fmt.Sprintf("se-got-wg-%03d.conf", i), Data: []byte("x")}
	}
	files, err := ExpandZip(buildZip(t, entries...))
	if err != nil {
		t.Fatalf("500 entries rejected: %v", err)
	}
	if len(files) != MaxZipEntries {
		t.Fatalf("got %d files, want %d", len(files), MaxZipEntries)
	}
}

func TestExpandZipRejects(t *testing.T) {
	conf := []byte(confText(t, validOpts(t)))
	tooMany := make([]zipEntry, MaxZipEntries+1)
	for i := range tooMany {
		tooMany[i] = zipEntry{Name: fmt.Sprintf("se-got-wg-%03d.conf", i), Data: []byte("x")}
	}
	inner := buildZip(t, zipEntry{Name: "a.conf", Data: conf})
	bomb := bytes.Repeat([]byte{0}, MaxZipBytes+1)

	cases := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"too many entries", buildZip(t, tooMany...), "too many entries"},
		{"dotdot", buildZip(t, zipEntry{"../evil.conf", conf}), "unsafe path"},
		{"absolute", buildZip(t, zipEntry{"/abs.conf", conf}), "unsafe path"},
		{"backslash dotdot", buildZip(t, zipEntry{`a\..\evil.conf`, conf}), "unsafe path"},
		{"nested zip name", buildZip(t, zipEntry{"ok.conf", conf}, zipEntry{"inner.zip", inner}), "nested archive"},
		{"nested zip content", buildZip(t, zipEntry{"se-got-wg-001.conf", inner}), "nested archive"},
		{"zip bomb", buildZip(t, zipEntry{"big.conf", bomb}), "exceeds"},
		{"not a zip", []byte("definitely not a zip file"), "not a zip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := ExpandZip(tc.data)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			if files != nil {
				t.Errorf("expected no files, got %d", len(files))
			}
		})
	}
}

func TestExpandZipRejectsBombIsSmall(t *testing.T) {
	data := buildZip(t, zipEntry{"big.conf", bytes.Repeat([]byte{0}, MaxZipBytes+1)})
	if len(data) > 64<<10 {
		t.Fatalf("bomb fixture is %d bytes; expected a small compressed payload", len(data))
	}
}

func TestExpandZipNonConf(t *testing.T) {
	confs := confEntries(t, 2)
	data := buildZip(t, confs[0], zipEntry{"README.txt", []byte("hello")}, confs[1])
	files, err := ExpandZip(data)
	if err != nil {
		t.Fatalf("ExpandZip: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}
	if files[1].Name != "README.txt" || files[1].Err != ErrNotConf {
		t.Errorf("README.txt = %+v, want Err %q", files[1], ErrNotConf)
	}
	for _, i := range []int{0, 2} {
		if files[i].Err != "" || len(files[i].Data) == 0 {
			t.Errorf("file %d (%s) should be a usable .conf: %+v", i, files[i].Name, files[i].Err)
		}
	}
}

func TestExpandZipRejectsTooManyRawEntries(t *testing.T) {
	// Directory entries are skipped by the 500-file count, but the raw entry table is still capped.
	entries := make([]zipEntry, 4*MaxZipEntries+1)
	for i := range entries {
		entries[i] = zipEntry{Name: fmt.Sprintf("d%04d/", i)}
	}
	files, err := ExpandZip(buildZip(t, entries...))
	if err == nil || !strings.Contains(err.Error(), "too many entries") || files != nil {
		t.Fatalf("err = %v, files = %d; want too many entries", err, len(files))
	}
}

func TestExpandZipRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "se-got-wg-001.conf", Method: zip.Store}
	h.SetMode(fs.ModeSymlink | 0o777)
	fw, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("/etc/passwd")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := ExpandZip(buf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "symlink") || files != nil {
		t.Fatalf("err = %v, files = %d; want symlink rejection", err, len(files))
	}
}
