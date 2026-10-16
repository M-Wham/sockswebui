package testrig

import "testing"

func TestResolveTargetHosts(t *testing.T) {
	hosts := map[string]string{
		"echo.rig.invalid": "127.0.0.1",
		"port.rig.invalid": "127.0.0.1:4321",
		"bad.rig.invalid":  "not-an-ip",
	}
	cases := []struct {
		target, want string
		wantErr      bool
	}{
		{"10.0.0.1:80", "10.0.0.1:80", false},
		{"echo.rig.invalid:80", "127.0.0.1:80", false},
		{"port.rig.invalid:80", "127.0.0.1:4321", false}, // ip:port entry overrides the port
		{"bad.rig.invalid:80", "", true},
		{"missing.rig.invalid:80", "", true},
	}
	for _, tc := range cases {
		got, err := resolveTarget(hosts, tc.target)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("resolveTarget(%q) = %q, %v; want %q, err=%v", tc.target, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestAllocSourceIPNoWrapCollision(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 600; i++ {
		ip := allocSourceIP()
		if !ip.IsLoopback() || ip.As4()[3] < 2 || ip.As4()[3] == 255 {
			t.Fatalf("allocSourceIP = %s, want 127.x.y.2-254", ip)
		}
		if seen[ip.String()] {
			t.Fatalf("allocSourceIP repeated %s after %d calls", ip, i)
		}
		seen[ip.String()] = true
	}
}
