package socks

// perSourceCap resolves Options.MaxConnsPerSource: an explicit
// positive value wins; 0 means half of maxConns, at least 1 (128 at the default 256).
func perSourceCap(maxConns, perSource int) int {
	if perSource > 0 {
		return perSource
	}
	return max(1, maxConns/2)
}

// sourceCounts counts live client conns per sourceKey, so one source cannot
// hold every slot of the listener. It is guarded
// by Server.mu. An entry exists only while its source has live conns, so the map
// holds at most MaxConns keys and needs no eviction.
type sourceCounts struct {
	limit int
	n     map[string]int
}

func newSourceCounts(limit int) *sourceCounts {
	return &sourceCounts{limit: limit, n: map[string]int{}}
}

// acquire takes one slot for key unless key is at its cap.
func (sc *sourceCounts) acquire(key string) bool {
	if sc.n[key] >= sc.limit {
		return false
	}
	sc.n[key]++
	return true
}

// release returns one slot for key; the entry is deleted when it reaches zero.
func (sc *sourceCounts) release(key string) {
	if sc.n[key] <= 1 {
		delete(sc.n, key)
		return
	}
	sc.n[key]--
}
