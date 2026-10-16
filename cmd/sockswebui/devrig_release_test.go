//go:build !testrig

package main

import (
	"bytes"
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
)

// These tests cover the release (untagged) build only; under -tags testrig the
// flag and routes exist and devrig_testrig_test.go covers them instead.

func TestDevTestrigNotCompiledInRelease(t *testing.T) {
	if devTestrigCompiled {
		t.Fatal("devTestrigCompiled must be false without -tags testrig")
	}
}

// Without the testrig tag, --dev-testrig is an unknown flag: exit 2.
func TestDevTestrigFlagUnknownWithoutTag(t *testing.T) {
	var errOut bytes.Buffer
	code := run(context.Background(), []string{"--dev-testrig"}, &bytes.Buffer{}, &errOut, fstest.MapFS{})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "flag provided but not defined: -dev-testrig") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

// The release build registers no dev rig controls: even a logged-in admin gets 404.
func TestDevRigRoutesAbsentWithoutTag(t *testing.T) {
	base := startRun(t, t.TempDir(), testFS())
	setupAndLogin(t, base)
	c, tok := loginCSRF(t, base)
	for _, p := range []string{"/api/dev/rig/pause", "/api/dev/rig/resume"} {
		if code := postCSRF(t, base+p, c, tok); code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", p, code)
		}
	}
}

// TestReleaseDepsExcludeTestrig: the release (untagged) binary does not link
// internal/testrig, so its loopback direct chain cannot reach production
// (release build check). Missing go toolchain is a failure, not a skip.
func TestReleaseDepsExcludeTestrig(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not on PATH: %v", err)
	}
	out, err := exec.Command(gobin, "list", "-mod=readonly", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/internal/testrig") {
			t.Fatalf("release build depends on %s", pkg)
		}
	}
}
