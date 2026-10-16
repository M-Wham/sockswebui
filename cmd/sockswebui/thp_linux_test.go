//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

// The package init must turn transparent huge pages off for the whole process, so a host
// with THP "always" cannot push idle RSS past the < 64 MB RSS budget. The test
// binary runs the same init as the release binary.
func TestTHPDisabledForProcess(t *testing.T) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("no /proc/self/status: %v", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "THP_enabled:"); ok {
			if got := strings.TrimSpace(v); got != "0" {
				t.Fatalf("THP_enabled = %s, want 0", got)
			}
			return
		}
	}
	t.Skip("kernel does not report THP_enabled (before Linux 5.0)")
}
