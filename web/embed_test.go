package web

import (
	"io/fs"
	"testing"
)

func TestDistFSRootReadable(t *testing.T) {
	fsys := DistFS()
	if fsys == nil {
		t.Fatal("DistFS returned nil")
	}
	if _, err := fs.ReadDir(fsys, "."); err != nil {
		t.Fatalf("read embedded root: %v", err)
	}
	if _, err := fs.Stat(fsys, ".keep"); err != nil {
		t.Fatalf("placeholder .keep not embedded at root: %v", err)
	}
}
