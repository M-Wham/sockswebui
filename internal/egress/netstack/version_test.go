package netstack

import (
	"runtime/debug"
	"testing"
)

// forkedFrom is the upstream version this package was copied from (see doc.go).
const forkedFrom = "v0.0.0-20260522210424-ecfc5a8d5446"

// TestUpstreamVersionUnchanged fails when go.mod moves golang.zx2c4.com/wireguard,
// so the copy cannot silently drift from upstream. On failure: diff upstream
// tun/netstack against this package, port what matters, then update forkedFrom
// and doc.go (or drop the copy if upstream fixed Close).
func TestUpstreamVersionUnchanged(t *testing.T) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info")
	}
	for _, d := range bi.Deps {
		if d.Path == "golang.zx2c4.com/wireguard" {
			if d.Version != forkedFrom {
				t.Fatalf("wireguard is %s, netstack copy is from %s: re-sync internal/egress/netstack", d.Version, forkedFrom)
			}
			return
		}
	}
	t.Fatal("golang.zx2c4.com/wireguard not in build info")
}
