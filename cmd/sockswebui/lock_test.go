package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runNoListen runs a second instance on dir and asserts it exits 1 without listening.
func runNoListen(t *testing.T, dir string) string {
	t.Helper()
	listened := make(chan string, 1)
	onListen = func(addr string) { listened <- addr }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	code := run(ctx, []string{"--data-dir", dir, "--ui-addr", "127.0.0.1:0"}, io.Discard, &stderr, testFS())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr.String())
	}
	select {
	case addr := <-listened:
		t.Fatalf("second instance listened on %s", addr)
	default:
	}
	return stderr.String()
}

// dirSnapshot renders every file name and content in dir (nothing is written by a refused run).
func dirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		data, _ := os.ReadFile(dir + "/" + e.Name())
		fmt.Fprintf(&b, "%s=%q;", e.Name(), data)
	}
	return b.String()
}

func TestSecondInstanceRefused(t *testing.T) {
	dir := t.TempDir()
	startRun(t, dir, testFS())
	before := dirSnapshot(t, dir)
	out := runNoListen(t, dir)
	want := fmt.Sprintf("pid=%d as recorded", os.Getpid())
	if !strings.Contains(out, "locked by another sockswebui process") || !strings.Contains(out, want) {
		t.Errorf("stderr = %q, want it to name the holder (%q)", out, want)
	}
	if after := dirSnapshot(t, dir); after != before {
		t.Errorf("data dir changed by the refused instance:\nbefore %q\nafter  %q", before, after)
	}

	t.Run("lock unsupported", func(t *testing.T) {
		orig := lockDir
		lockDir = func(string) (func() error, string, error) {
			return nil, "", fmt.Errorf("cannot lock data dir: %w; the data dir must be on a local filesystem",
				syscall.ENOLCK)
		}
		t.Cleanup(func() { lockDir = orig })
		dir := t.TempDir()
		out := runNoListen(t, dir)
		if !strings.Contains(out, "local filesystem") {
			t.Errorf("stderr = %q, want the local-filesystem hint", out)
		}
		if _, err := os.Stat(dir + "/config.json"); !os.IsNotExist(err) {
			t.Errorf("config.json written although the lock failed (stat err=%v)", err)
		}
	})
}
