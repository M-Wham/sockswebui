//go:build memload

package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// memMetrics are the runtime/metrics series sampled every memSampleEvery.
var memMetrics = []string{
	"/memory/classes/heap/objects:bytes",  // live + not-yet-swept heap objects
	"/memory/classes/heap/unused:bytes",   // spans in use, slack
	"/memory/classes/heap/free:bytes",     // free spans not yet returned to the OS
	"/memory/classes/heap/stacks:bytes",   // goroutine stacks
	"/memory/classes/total:bytes",         // everything the runtime mapped
	"/memory/classes/heap/released:bytes", // returned to the OS (not RSS)
	"/gc/heap/goal:bytes",
	"/sched/goroutines:goroutines",
}

const memSampleEvery = 20 * time.Millisecond

type memSample struct {
	at   time.Duration
	rss  int64 // kB, /proc/self/status VmRSS
	vals []uint64
}

type memRecorder struct {
	out   string
	mu    sync.Mutex
	lines []string
	phase string
	start time.Time
	stop  chan struct{}
	done  chan struct{}
	peak  memSample // sample with the highest RSS
	maxes []uint64
	prof  []byte // heap profile taken when heap objects peaked
	profH uint64
}

func procStatusKB(field string) int64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field+":"); ok {
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return n
		}
	}
	return -1
}

func (r *memRecorder) begin(phase string) {
	// clear_refs "5" resets VmHWM to the current RSS (Linux 4.0+), so each phase has its own peak.
	_ = os.WriteFile("/proc/self/clear_refs", []byte("5"), 0)
	r.phase, r.start = phase, time.Now()
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	r.peak, r.maxes, r.prof, r.profH = memSample{}, make([]uint64, len(memMetrics)), nil, 0
	go r.loop()
}

func (r *memRecorder) loop() {
	defer close(r.done)
	s := make([]metrics.Sample, len(memMetrics))
	for i, n := range memMetrics {
		s[i].Name = n
	}
	tick := time.NewTicker(memSampleEvery)
	defer tick.Stop()
	lastProf := time.Time{}
	for {
		metrics.Read(s)
		cur := memSample{at: time.Since(r.start), rss: procStatusKB("VmRSS"), vals: make([]uint64, len(s))}
		for i := range s {
			cur.vals[i] = s[i].Value.Uint64()
			r.maxes[i] = max(r.maxes[i], cur.vals[i])
		}
		if cur.rss > r.peak.rss {
			r.peak = cur
		}
		// Heap profile (as of the last GC) whenever heap objects reach a new high, at most every 500 ms.
		if h := cur.vals[0]; h > r.profH+r.profH/20 && time.Since(lastProf) > 500*time.Millisecond {
			var buf bytes.Buffer
			if pprof.Lookup("heap").WriteTo(&buf, 0) == nil {
				r.prof, r.profH, lastProf = buf.Bytes(), h, time.Now()
			}
		}
		select {
		case <-r.stop:
			return
		case <-tick.C:
		}
	}
}

func (r *memRecorder) end(t *testing.T) {
	close(r.stop)
	<-r.done
	hwm := procStatusKB("VmHWM")
	mib := func(v uint64) string { return fmt.Sprintf("%.1f", float64(v)/(1<<20)) }
	p := r.peak.vals
	if p == nil {
		p = make([]uint64, len(memMetrics))
	}
	line := fmt.Sprintf("MEMLOAD phase=%s secs=%.1f vmhwm_kb=%d rss_end_kb=%d | at-peak-rss: rss_kb=%d heap_objects_mib=%s "+
		"heap_unused_mib=%s heap_free_mib=%s stacks_mib=%s runtime_total_mib=%s heap_goal_mib=%s goroutines=%d | "+
		"phase-max: heap_objects_mib=%s heap_goal_mib=%s runtime_total_mib=%s goroutines=%d",
		r.phase, time.Since(r.start).Seconds(), hwm, procStatusKB("VmRSS"), r.peak.rss,
		mib(p[0]), mib(p[1]), mib(p[2]), mib(p[3]), mib(p[4]), mib(p[6]), p[7],
		mib(r.maxes[0]), mib(r.maxes[6]), mib(r.maxes[4]), r.maxes[7])
	fmt.Println(line)
	t.Log(line)
	r.mu.Lock()
	r.lines = append(r.lines, line)
	r.mu.Unlock()
	if r.prof != nil {
		writeFile(t, r.out, "heap-"+r.phase+".pb.gz", r.prof)
	}
}

func (r *memRecorder) note(s string) {
	fmt.Println("MEMLOAD " + s)
	r.mu.Lock()
	r.lines = append(r.lines, s)
	r.mu.Unlock()
}

func (r *memRecorder) writeSummary(t *testing.T) {
	var buf bytes.Buffer
	_ = pprof.Lookup("allocs").WriteTo(&buf, 0)
	writeFile(t, r.out, "allocs-final.pb.gz", buf.Bytes())
	hdr := fmt.Sprintf("GOMAXPROCS=%d GOGC=%q GOMEMLIMIT=%q go=%s MemProfileRate=%d",
		runtime.GOMAXPROCS(0), os.Getenv("GOGC"), os.Getenv("GOMEMLIMIT"), runtime.Version(), runtime.MemProfileRate)
	writeFile(t, r.out, "summary.txt", []byte(hdr+"\n"+strings.Join(r.lines, "\n")+"\n"))
}
