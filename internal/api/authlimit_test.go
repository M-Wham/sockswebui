package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// clientKeyTable maps a remote address to its limiter key:
// IPv4 and IPv4-mapped IPv6 key by full IPv4 address; IPv6 keys by its /64 with any zone
// dropped; an unparsable address keys as the raw string.
// Keep this table byte-for-byte identical to the copy in internal/socks so the
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

func TestClientKeyUnmapsV4Mapped(t *testing.T) {
	for _, tc := range clientKeyTable {
		r := httptest.NewRequest("POST", "/api/login", nil)
		r.RemoteAddr = tc.remote
		if got := clientKey(r); got != tc.key {
			t.Errorf("clientKey(%q) = %q, want %q", tc.remote, got, tc.key)
		}
	}
}

// Login, export and import draw on one budget per client.
func TestLimitAuthBucketsShareBudget(t *testing.T) {
	e := newTestEnv(t)
	routes := []string{"login", "export", "import", "login", "export"}
	call := func(route, remote string) (bool, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/"+route, nil)
		r.RemoteAddr = remote
		return e.srv.limitAuth(rec, r, route), rec
	}
	for i, route := range routes {
		if ok, rec := call(route, "192.0.2.10:4000"); !ok {
			t.Fatalf("attempt %d (%s): denied with %d", i+1, route, rec.Code)
		}
	}
	ok, rec := call("import", "[::ffff:192.0.2.10]:4001")
	if ok || rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt across routes: ok=%v status %d, want 429", ok, rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	if !strings.Contains(e.logs.String(), "route=import") {
		t.Errorf("429 log line lacks route=import: %s", e.logs.String())
	}
	if ok, _ := call("login", "198.51.100.7:1234"); !ok {
		t.Fatal("other client denied")
	}
}

// TestRateLimitIPv6Prefix: a client is one IPv4 address or one IPv6 /64.
func TestRateLimitIPv6Prefix(t *testing.T) {
	e := newTestEnv(t)
	e.setup("hunter22-long")
	login := func(remote string) int {
		return e.do("POST", "/api/login", `{"password":"wrong-password"}`, withRemote(remote)).Code
	}
	for i := 1; i <= loginLimit; i++ {
		remote := "[2001:db8:1:2::" + strconv.Itoa(i) + "]:5000"
		if code := login(remote); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d from %s: status %d, want 401", i, remote, code)
		}
	}
	if code := login("[2001:db8:1:2:ffff:ffff:ffff:6]:5000"); code != http.StatusTooManyRequests {
		t.Fatalf("6th address in one /64: status %d, want 429", code)
	}
	if code := login("[2001:db8:1:3::1]:5000"); code != http.StatusUnauthorized {
		t.Fatalf("other /64: status %d, want 401", code)
	}

	keys := map[string]string{
		"[::ffff:192.0.2.1]:1": "192.0.2.1",
		"[::ffff:192.0.2.2]:1": "192.0.2.2",
		"192.0.2.7:1":          "192.0.2.7",
		"not-an-address":       "not-an-address",
	}
	for remote, want := range keys {
		r, _ := http.NewRequest("POST", "/api/login", nil)
		r.RemoteAddr = remote
		if got := clientKey(r); got != want {
			t.Errorf("clientKey(%q) = %q, want %q", remote, got, want)
		}
	}
}
