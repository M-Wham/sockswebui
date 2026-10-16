package egress

import (
	"slices"
	"testing"
)

func TestInnerMTU(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured []int // per WG hop, route order
		want       []int
	}{
		{"first hop keeps own MTU", []int{1420}, []int{1420}},
		{"first hop custom MTU unchanged", []int{1380}, []int{1380}},
		{"first hop unset → default", []int{0}, []int{1420}},
		{"outer 1420, inner unset", []int{1420, 0}, []int{1420, 1340}},
		{"outer 1420, inner importer default", []int{1420, 1420}, []int{1420, 1340}},
		{"outer 1420, inner override 1280", []int{1420, 1280}, []int{1420, 1280}},
		{"outer 1280, inner default", []int{1280, 1420}, []int{1280, 1200}},
		{"outer 1280, inner unset", []int{1280, 0}, []int{1280, 1200}},
		{"inner larger than outer clamps", []int{1420, 1500}, []int{1420, 1340}},
		{"third hop", []int{1420, 1420, 1420}, []int{1420, 1340, 1260}},
		{"third hop after override", []int{1420, 1280, 0}, []int{1420, 1280, 1200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wgMTUs(tc.configured); !slices.Equal(got, tc.want) {
				t.Fatalf("wgMTUs(%v) = %v, want %v", tc.configured, got, tc.want)
			}
		})
	}
	if got := innerMTU(1420, 0); got != 1340 {
		t.Fatalf("innerMTU(1420, 0) = %d, want 1340", got)
	}
}
