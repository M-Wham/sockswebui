package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/testrig"
)

type denyVector struct {
	Addr    string `json:"addr"`
	Want    string `json:"want"`
	Blocked string `json:"blocked,omitempty"` // lan rows: "deny" under block_lan_targets
}

func loadDenyVectors(t *testing.T) []denyVector {
	t.Helper()
	b, err := os.ReadFile("testdata/devicedeny-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v []denyVector
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// wantDenyRows is the device-route deny set; the vectors file must
// carry exactly these deny rows so the Go and web lists cannot drift.
var wantDenyRows = []string{
	"fe80::1%eth0", "::ffff:7f00:1%eth0",
	"0.0.0.0", "::", "::ffff:0.0.0.0", "0.1.2.3",
	"127.0.0.1", "127.255.0.1", "::1", "::ffff:127.0.0.1", "::127.0.0.1",
	"169.254.169.254", "fec0::1",
	"fd00:ec2::254", "100.100.100.200",
	"224.0.0.1", "ff02::1", "240.0.0.1", "255.255.255.255",
	"100::1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1", "3fff::1",
	"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe",
	"2002:7f00:1::",
	"2001:0:4136:e378:8000:63bf:80ff:fffe",
}

func TestDeniedOnDeviceTable(t *testing.T) {
	vecs := loadDenyVectors(t)
	var gotDeny []string
	counts := map[string]int{}
	for _, v := range vecs {
		counts[v.Want]++
		ip, err := netip.ParseAddr(v.Addr)
		if err != nil {
			t.Fatalf("vector %q: %v", v.Addr, err)
		}
		got := deniedOnDevice(ip, policyLANAllowed)
		switch v.Want {
		case "deny":
			gotDeny = append(gotDeny, v.Addr)
			if !got {
				t.Errorf("deniedOnDevice(%s) = false, want deny", v.Addr)
			}
			// Zone rows must fall at step 1 on the original address; the
			// mapped one would otherwise hide behind the loopback row.
			if strings.Contains(v.Addr, "%") {
				if s := deniedAt(ip, policyLANAllowed); s != stepZone {
					t.Errorf("%s denied at step %d, want step 1 (zone before Unmap)", v.Addr, s)
				}
			}
			if strings.HasPrefix(v.Addr, "64:ff9b::") {
				if s := deniedAt(ip, policyLANAllowed); s != stepNAT64 {
					t.Errorf("%s denied at step %d, want step 3 (NAT64 embedded IPv4)", v.Addr, s)
				}
			}
		case "allow":
			if got {
				t.Errorf("deniedOnDevice(%s) = true, want allow", v.Addr)
			}
			if inTable(lanClass, ip.Unmap()) {
				t.Errorf("%s is in the LAN class, want global", v.Addr)
			}
		case "lan":
			if got {
				t.Errorf("deniedOnDevice(%s) = true, want allowed (lan, block_lan_targets false)", v.Addr)
			}
			if !inTable(lanClass, ip.Unmap()) {
				t.Errorf("%s is not in the LAN class table", v.Addr)
			}
		default:
			t.Fatalf("vector %q: unknown want %q", v.Addr, v.Want)
		}
	}
	want := append([]string(nil), wantDenyRows...)
	sort.Strings(want)
	sort.Strings(gotDeny)
	if strings.Join(want, ",") != strings.Join(gotDeny, ",") {
		t.Errorf("deny rows drifted from the deny set:\n got %v\nwant %v", gotDeny, want)
	}
	if counts["allow"] != 3 || counts["lan"] != 8 {
		t.Errorf("allow/lan row counts = %d/%d, want 3/8", counts["allow"], counts["lan"])
	}

	// Metadata pins sit inside the LAN class yet are denied (step 4 before 5).
	for _, a := range []string{"fd00:ec2::254", "100.100.100.200"} {
		ip := netip.MustParseAddr(a)
		if !inTable(lanClass, ip) || deniedAt(ip, policyLANAllowed) != stepAlways {
			t.Errorf("%s: want inside LAN class and denied at step 4", a)
		}
	}
	if deniedOnDevice(netip.Addr{}, policyLANAllowed) != true {
		t.Error("invalid address allowed")
	}
}

// permitAll is the internal test hook that lets deviceDialChecked reach a
// loopback rig.
func permitAll(netip.Addr) error { return nil }

// loopbackDenyRows are the deny rows that would reach a 127.0.0.1 listener
// (or stay on the host) if the check were missing; only these are dialed so
// a broken check can never leave the machine.
var loopbackDenyRows = []string{
	"127.0.0.1", "127.255.0.1", "::ffff:127.0.0.1", "::ffff:7f00:1%eth0",
	"0.0.0.0", "::ffff:0.0.0.0", "::1", "::",
}

func TestDirectDeny(t *testing.T) {
	sentinel := testrig.NewSentinel(t)
	_, port, _ := net.SplitHostPort(sentinel.Addr())
	ch := build(t, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, a := range loopbackDenyRows {
		c, err := ch.DialContext(ctx, "tcp", net.JoinHostPort(a, port))
		if err == nil {
			_ = c.Close()
			t.Errorf("direct dial %s succeeded, want ErrDeniedTarget", a)
			continue
		}
		if !errors.Is(err, ErrDeniedTarget) {
			t.Errorf("direct dial %s: %v, want ErrDeniedTarget", a, err)
		}
	}

	t.Run("DNS all denied", func(t *testing.T) {
		orig := deviceLookup
		t.Cleanup(func() { deviceLookup = orig })
		deviceLookup = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		c, err := ch.DialContext(ctx, "tcp", net.JoinHostPort("deny.rig.invalid", port))
		if err == nil {
			_ = c.Close()
			t.Fatal("hostname resolving to loopback dialed")
		}
		if !errors.Is(err, ErrDeniedTarget) {
			t.Fatalf("err = %v, want ErrDeniedTarget", err)
		}
	})

	t.Run("DNS mixed answers dial only allowed", func(t *testing.T) {
		orig := deviceLookup
		t.Cleanup(func() { deviceLookup = orig })
		allowed := netip.MustParseAddr("192.0.0.9") // LAN class: allowed by default
		deviceLookup = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), allowed}, nil
		}
		// The hook wraps the production check, records what passed and then
		// halts, so no socket is opened toward the allowed address.
		errHalt := errors.New("halt before dial")
		var passed []netip.Addr
		dial := deviceDialChecked(func(ip netip.Addr) error {
			if err := checkDirect(ip); err != nil {
				return err
			}
			passed = append(passed, ip)
			return errHalt
		})
		_, err := dial(ctx, "tcp", net.JoinHostPort("mixed.rig.invalid", port))
		if !errors.Is(err, ErrDeniedTarget) || !errors.Is(err, errHalt) {
			t.Fatalf("err = %v, want ErrDeniedTarget for loopback and halt for allowed", err)
		}
		if len(passed) != 1 || passed[0] != allowed {
			t.Fatalf("IPs reaching dial = %v, want [%s]", passed, allowed)
		}
	})

	time.Sleep(50 * time.Millisecond)
	if n := sentinel.Conns(); n != 0 {
		t.Fatalf("sentinel conns = %d, want 0", n)
	}
}

func TestDeviceDialCheckedLink(t *testing.T) {
	sentinel := testrig.NewSentinel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := deviceDialChecked(permitAll)(ctx, "tcp", sentinel.Addr())
	if err != nil {
		t.Fatalf("permit-all dial: %v", err)
	}
	_ = c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for sentinel.Conns() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := sentinel.Conns(); n != 1 {
		t.Fatalf("permit-all: sentinel conns = %d, want 1", n)
	}

	if _, err := deviceDialChecked(checkDirect)(ctx, "tcp", sentinel.Addr()); !errors.Is(err, ErrDeniedTarget) {
		t.Fatalf("production check: err = %v, want ErrDeniedTarget", err)
	}
	if _, err := build(t, nil, nil).DialContext(ctx, "tcp", sentinel.Addr()); !errors.Is(err, ErrDeniedTarget) {
		t.Fatalf("Build(nil, nil): err = %v, want ErrDeniedTarget", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := sentinel.Conns(); n != 1 {
		t.Fatalf("sentinel conns = %d after denied dials, want 1", n)
	}
}

// lanClassRows are the eight step-5 (LAN class) rows plus the NAT64
// form of 10.0.0.1, which must follow the flag via step 3.
var lanClassRows = []string{
	"10.0.0.1", "172.16.0.1", "192.168.1.1", "fd12::1",
	"100.64.0.1", "192.0.0.9", "198.18.0.1", "64:ff9b:1::1",
	"64:ff9b::a00:1",
}

// bothWaysDenied stay denied whatever block_lan_targets says.
var bothWaysDenied = []string{"fd00:ec2::254", "100.100.100.200", "127.0.0.1", "169.254.169.254"}

func TestDirectLANClass(t *testing.T) {
	allowCheck, blockCheck := directCheck(false), directCheck(true)
	for _, a := range lanClassRows {
		ip := netip.MustParseAddr(a)
		if err := allowCheck(ip); err != nil {
			t.Errorf("default policy: check(%s) = %v, want nil", a, err)
		}
		if err := blockCheck(ip); !errors.Is(err, ErrDeniedTarget) {
			t.Errorf("blockLAN: check(%s) = %v, want ErrDeniedTarget", a, err)
		}
	}
	if s := deniedAt(netip.MustParseAddr("64:ff9b::a00:1"), policyBlockLAN); s != stepNAT64 {
		t.Errorf("64:ff9b::a00:1 under blockLAN denied at step %d, want step 3", s)
	}
	for _, a := range bothWaysDenied {
		ip := netip.MustParseAddr(a)
		for _, block := range []bool{false, true} {
			if err := directCheck(block)(ip); !errors.Is(err, ErrDeniedTarget) {
				t.Errorf("blockLAN=%v: check(%s) = %v, want ErrDeniedTarget", block, a, err)
			}
		}
	}

	t.Run("vectors under blockLAN", func(t *testing.T) {
		for _, v := range loadDenyVectors(t) {
			ip := netip.MustParseAddr(v.Addr)
			got := deniedOnDevice(ip, policyBlockLAN)
			switch v.Want {
			case "deny":
				if !got {
					t.Errorf("blockLAN: %s allowed, want deny", v.Addr)
				}
			case "allow":
				if got {
					t.Errorf("blockLAN: %s denied, want allow (global)", v.Addr)
				}
			case "lan":
				if v.Blocked != "deny" {
					t.Errorf("lan row %s: blocked = %q, want \"deny\"", v.Addr, v.Blocked)
				}
				if !got {
					t.Errorf("blockLAN: %s allowed, want deny", v.Addr)
				}
			}
		}
	})

	t.Run("Direct chain installs the policy", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ch, err := Direct(true)
		if err != nil {
			t.Fatal(err)
		}
		defer ch.Close()
		// Denied before any socket opens, so no LAN host is ever contacted.
		for _, a := range lanClassRows {
			c, err := ch.DialContext(ctx, "tcp", net.JoinHostPort(a, "80"))
			if err == nil {
				_ = c.Close()
				t.Errorf("Direct(true) dialed %s", a)
			} else if !errors.Is(err, ErrDeniedTarget) {
				t.Errorf("Direct(true) %s: %v, want ErrDeniedTarget", a, err)
			}
		}
		d, err := Direct(false)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if _, err := d.DialContext(ctx, "tcp", "127.0.0.1:80"); !errors.Is(err, ErrDeniedTarget) {
			t.Errorf("Direct(false) 127.0.0.1: %v, want ErrDeniedTarget", err)
		}
	})
}
