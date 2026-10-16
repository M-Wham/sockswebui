package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

const (
	// MaxZipEntries is the maximum number of non-directory entries in one zip.
	MaxZipEntries = 500
	// maxRawZipEntries caps the raw entry table, directories included.
	maxRawZipEntries = 4 * MaxZipEntries
	// MaxZipBytes caps the total decompressed size of a zip, matching the 5 MiB request limit.
	MaxZipBytes = 5 << 20
	// ErrNotConf is the per-file reason for a zip entry that is not a .conf file.
	ErrNotConf = "not a .conf file"
)

// File is one entry expanded from an uploaded zip. A non-empty Err marks an entry the batch
// must report as `error: <Err>` instead of parsing; Data is then nil.
type File struct {
	Name string
	Data []byte
	Err  string
}

// archiveExts are entry name suffixes treated as nested archives.
var archiveExts = []string{".zip", ".jar", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar"}

// archiveMagics are content prefixes treated as nested archives (zip local/empty/spanned, gzip,
// 7z, rar, bzip2, xz).
var archiveMagics = [][]byte{
	[]byte("PK\x03\x04"), []byte("PK\x05\x06"), []byte("PK\x07\x08"),
	{0x1f, 0x8b}, []byte("7z\xbc\xaf\x27\x1c"), []byte("Rar!\x1a\x07"), []byte("BZh"),
	{0xfd, '7', 'z', 'X', 'Z', 0x00},
}

// ExpandZip safely expands an uploaded zip held in memory. Directory entries are
// skipped. The whole zip is rejected (nil files, error with a specific reason) if it is not a
// zip, has more than MaxZipEntries entries (or 4x that counting directories), contains an
// unsafe path (zip-slip), a symlink entry or a nested archive, or decompresses to more than
// MaxZipBytes. Sizes are enforced while reading, never
// trusted from headers. Entries not ending in .conf are returned with Err = ErrNotConf.
// Pure: nothing is written to disk.
func ExpandZip(data []byte) ([]File, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("not a zip archive: %w", err)
	}
	if len(r.File) > maxRawZipEntries {
		return nil, fmt.Errorf("zip has too many entries: %d (max %d including directories)", len(r.File), maxRawZipEntries)
	}
	var entries []*zip.File
	for _, f := range r.File {
		if isDirEntry(f) {
			continue
		}
		entries = append(entries, f)
	}
	if len(entries) > MaxZipEntries {
		return nil, fmt.Errorf("zip has too many entries: %d (max %d)", len(entries), MaxZipEntries)
	}
	for _, f := range entries {
		if !safeEntryName(f.Name) {
			return nil, fmt.Errorf("zip entry %q has an unsafe path", f.Name)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("zip entry %q is a symlink", f.Name)
		}
		if hasArchiveExt(f.Name) {
			return nil, fmt.Errorf("zip entry %q is a nested archive", f.Name)
		}
	}
	files := make([]File, 0, len(entries))
	remaining := int64(MaxZipBytes)
	for _, f := range entries {
		b, err := readEntry(f, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(b))
		if hasArchiveMagic(b) {
			return nil, fmt.Errorf("zip entry %q is a nested archive", f.Name)
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".conf") {
			files = append(files, File{Name: f.Name, Err: ErrNotConf})
			continue
		}
		files = append(files, File{Name: f.Name, Data: b})
	}
	return files, nil
}

// readEntry decompresses f, failing once more than limit bytes have been produced.
func readEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("zip entry %q: %w", f.Name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("zip decompressed size exceeds %d bytes", MaxZipBytes)
	}
	return b, nil
}

// isDirEntry reports whether f is a directory entry (trailing slash or dir mode).
func isDirEntry(f *zip.File) bool {
	return strings.HasSuffix(f.Name, "/") || strings.HasSuffix(f.Name, `\`) || f.FileInfo().IsDir()
}

// safeEntryName rejects empty, absolute, drive-letter and parent-traversal names, treating
// both / and \ as separators.
func safeEntryName(name string) bool {
	if name == "" || strings.ContainsRune(name, 0) {
		return false
	}
	if name[0] == '/' || name[0] == '\\' {
		return false
	}
	if len(name) >= 2 && name[1] == ':' {
		return false
	}
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return false
		}
	}
	return true
}

func hasArchiveExt(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range archiveExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func hasArchiveMagic(b []byte) bool {
	for _, m := range archiveMagics {
		if bytes.HasPrefix(b, m) {
			return true
		}
	}
	return false
}
