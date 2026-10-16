package socks

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clientKeyTable maps a remote address to its limiter key:
// IPv4 and IPv4-mapped IPv6 key by full IPv4 address; IPv6 keys by its /64 with any zone
// dropped; an unparsable address keys as the raw string.
// Keep this table byte-for-byte identical to the copy in internal/api so the
// two key rules cannot drift.
var clientKeyTable = []struct {
	remote string
	key    string
}{
	{"192.0.2.10:40000", "192.0.2.10"},
	{"192.0.2.10:1", "192.0.2.10"},
	{"[2001:db8::1]:5000", "2001:db8::/64"},
	{"[2001:db8::2]:5000", "2001:db8::/64"},
	{"[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:5000", "2001:db8:1:2::/64"},
	{"[2001:db8:1:3::1]:5000", "2001:db8:1:3::/64"},
	{"[::1]:5000", "::/64"},
	{"[::ffff:192.0.2.1]:5000", "192.0.2.1"},
	{"[::ffff:192.0.2.2]:5000", "192.0.2.2"},
	{"[fe80::1%eth0]:5000", "fe80::/64"},
	{"[fe80::1%eth1]:5000", "fe80::/64"},
	{"192.0.2.10", "192.0.2.10"},
	{"not-an-address", "not-an-address"},
	{"host.example:80", "host.example:80"},
	{"", ""},
}

// TestSourceKeyIPv6Prefix: a source is one IPv4 address or one IPv6 /64, so
// rotating addresses inside one /64 cannot dodge the auth throttle.
func TestSourceKeyIPv6Prefix(t *testing.T) {
	for _, tc := range clientKeyTable {
		if got := sourceKey(tc.remote); got != tc.key {
			t.Errorf("sourceKey(%q) = %q, want %q", tc.remote, got, tc.key)
		}
	}
	th := newThrottle()
	for i := 1; i <= authFailLimit; i++ {
		remote := "[2001:db8:1:2::" + strconv.Itoa(i) + "]:5000"
		if _, ok := th.reserve(sourceKey(remote)); !ok {
			t.Fatalf("failure %d from %s throttled early", i, remote)
		}
	}
	if _, ok := th.reserve(sourceKey("[2001:db8:1:2:ffff:ffff:ffff:b%eth0]:5000")); ok {
		t.Fatalf("failure %d from the same /64 was not throttled", authFailLimit+1)
	}
	if _, ok := th.reserve(sourceKey("[2001:db8:1:3::1]:5000")); !ok {
		t.Fatal("other /64 throttled")
	}
	if _, ok := th.reserve(sourceKey("[::ffff:192.0.2.1]:5000")); !ok {
		t.Fatal("IPv4-mapped source throttled")
	}
}

// spyCreds counts how often credentials are compared.
type spyCreds struct {
	inner checker
	calls atomic.Int64
}

func (s *spyCreds) match(user, pass []byte) bool {
	s.calls.Add(1)
	return s.inner.match(user, pass)
}

// throttleEnv is an auth-enabled server with a spied checker and a fake clock.
type throttleEnv struct {
	s    *Server
	addr string
	spy  *spyCreds
	mu   sync.Mutex
	now  time.Time
}

func newThrottleEnv(t *testing.T) *throttleEnv {
	t.Helper()
	s, err := New(Options{Dialer: newFakeDialer(), Username: "alice", Password: "s3cret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e := &throttleEnv{s: s, now: time.Unix(1_700_000_000, 0)}
	e.spy = &spyCreds{inner: s.creds}
	s.creds = e.spy
	s.throttle.now = func() time.Time {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.now
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	e.addr = ln.Addr().String()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()
	t.Cleanup(func() {
		_ = s.Close()
		<-done
	})
	return e
}

func (e *throttleEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// authFrom runs greeting + RFC 1929 auth from local address src and returns the status.
func authFrom(t *testing.T, addr, src, user, pass string) byte {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial from %s: %v", src, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if m := greet(t, c, 0x02); m != 0x02 {
		t.Fatalf("method = %#x", m)
	}
	return userPass(t, c, user, pass)
}

func TestAuthThrottleBlocksAfterLimit(t *testing.T) {
	e := newThrottleEnv(t)
	for i := range authFailLimit {
		if st := authFrom(t, e.addr, "127.0.0.2", "alice", "wrong"); st != 0x01 {
			t.Fatalf("attempt %d status = %#x", i, st)
		}
	}
	if got := e.spy.calls.Load(); got != authFailLimit {
		t.Fatalf("compares after %d failures = %d", authFailLimit, got)
	}
	// Throttled: right password is refused and credentials are not compared.
	if st := authFrom(t, e.addr, "127.0.0.2", "alice", "s3cret"); st != 0x01 {
		t.Fatalf("throttled status = %#x, want 0x01", st)
	}
	if got := e.spy.calls.Load(); got != authFailLimit {
		t.Fatalf("throttled handshake compared credentials (calls = %d)", got)
	}
	// Another source is unaffected.
	if st := authFrom(t, e.addr, "127.0.0.1", "alice", "s3cret"); st != 0x00 {
		t.Fatalf("127.0.0.1 status = %#x, want 0x00", st)
	}
	// After the window the throttled source authenticates again.
	e.advance(authFailWindow + time.Second)
	if st := authFrom(t, e.addr, "127.0.0.2", "alice", "s3cret"); st != 0x00 {
		t.Fatalf("after window status = %#x, want 0x00", st)
	}
}

func TestAuthThrottleConcurrent(t *testing.T) {
	e := newThrottleEnv(t)
	const n = 20
	var wg sync.WaitGroup
	statuses := make(chan byte, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- authFrom(t, e.addr, "127.0.0.2", "alice", "wrong")
		}()
	}
	wg.Wait()
	close(statuses)
	for st := range statuses {
		if st != 0x01 {
			t.Errorf("status = %#x, want 0x01", st)
		}
	}
	if got := e.spy.calls.Load(); got != authFailLimit {
		t.Fatalf("compared %d times, want exactly %d", got, authFailLimit)
	}
}

// Successes do not consume budget.
func TestAuthThrottleSuccessesNeverThrottle(t *testing.T) {
	e := newThrottleEnv(t)
	for i := range 100 {
		if st := authFrom(t, e.addr, "127.0.0.2", "alice", "s3cret"); st != 0x00 {
			t.Fatalf("success %d status = %#x, want 0x00", i, st)
		}
	}
}

// Each server has its own table.
func TestAuthThrottleIndependentServers(t *testing.T) {
	a, b := newThrottleEnv(t), newThrottleEnv(t)
	for range authFailLimit {
		authFrom(t, a.addr, "127.0.0.2", "alice", "wrong")
	}
	if st := authFrom(t, a.addr, "127.0.0.2", "alice", "s3cret"); st != 0x01 {
		t.Fatalf("server A status = %#x, want 0x01", st)
	}
	if st := authFrom(t, b.addr, "127.0.0.2", "alice", "s3cret"); st != 0x00 {
		t.Fatalf("server B status = %#x, want 0x00", st)
	}
}

func TestAuthThrottleTableFlood(t *testing.T) {
	e := newThrottleEnv(t)
	for i := range 2000 {
		e.s.throttle.fail("flood-" + strconv.Itoa(i))
	}
	if n := e.s.throttle.size(); n > authFailMaxKeys {
		t.Fatalf("table size = %d, want <= %d", n, authFailMaxKeys)
	}
	if st := authFrom(t, e.addr, "127.0.0.1", "alice", "s3cret"); st != 0x00 {
		t.Fatalf("fresh key status = %#x, want 0x00", st)
	}
	if n := e.s.throttle.size(); n > authFailMaxKeys {
		t.Fatalf("table size after auth = %d, want <= %d", n, authFailMaxKeys)
	}
}

// Eviction: a key at its limit outlives under-limit keys when the table is full.
func TestAuthThrottleEvictsUnderLimitFirst(t *testing.T) {
	th := newThrottle()
	for range authFailLimit {
		th.fail("attacker")
	}
	for i := range 2 * authFailMaxKeys {
		th.fail("spray-" + strconv.Itoa(i))
	}
	if _, ok := th.reserve("attacker"); ok {
		t.Fatal("at-limit key was evicted by a spray of under-limit keys")
	}
}
