package api

import "testing"

func TestHostAllowList(t *testing.T) {
	l := NewNameList("Pi-Box.", "ui.example", "Router.Example.")
	allow := []string{
		"127.0.0.1", "127.0.0.1:8080", "10.0.0.5:80", "192.168.1.1",
		"[::1]", "[::1]:8080", "::1", "[fe80::1%25eth0]:8080",
		"localhost", "localhost:8080", "LOCALHOST", "localhost.",
		"pi-box", "PI-BOX:8080", "pi-box.local", "pi-box.local:8080", "Pi-Box.Local.",
		"ui.example", "UI.EXAMPLE:443", "ui.example.", "router.example", "router.example:8080",
	}
	for _, h := range allow {
		if !l.Allows(h) {
			t.Errorf("Allows(%q) = false, want true", h)
		}
	}
	deny := []string{
		"", ":8080", "evil.example", "evil.example:8080",
		"ui.example.evil.test", "evilui.example", "ui.example..", "xui.example",
		"pi-box.local.evil", "pi-box.lan", "localhost.evil", "evil-localhost",
		"ui.exаmple",           // Cyrillic a
		"Kocalhost",            // Kelvin sign lower-cases to k
		"localhost:80:80",      // junk port
		"localhost:http",       // non-numeric port
		"localhost:",           // empty port
		"[evil.example]:80",    // brackets around a name
		"[::1",                 // unterminated bracket
		"1.2.3.4.evil.example", // IP-looking prefix
		"ui.example/path",      // path
		"localhost:8080\x00",   // control byte
	}
	for _, h := range deny {
		if l.Allows(h) {
			t.Errorf("Allows(%q) = true, want false", h)
		}
	}
}

func TestHostAllowListNoHostname(t *testing.T) {
	l := NewNameList("")
	if l.Allows(".local") || l.Allows("") || l.Allows("evil.example") {
		t.Error("empty hostname must not widen the list")
	}
	if !l.Allows("localhost") || !l.Allows("127.0.0.1:1") {
		t.Error("IP literals and localhost must still pass")
	}
	var zero NameList
	if zero.Allows("evil.example") || !zero.Allows("127.0.0.1") {
		t.Error("zero NameList: IP only")
	}
}

func TestHostAllowListValidUIHost(t *testing.T) {
	for _, ok := range []string{"ui.example", "UI.Example.", "router", "a-b.c-d.example"} {
		if err := ValidUIHost(ok); err != nil {
			t.Errorf("ValidUIHost(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", ".", " ", "https://ui.example", "ui.example/x", "ui.example:8080",
		"[::1]:80", "ui example", "ui.exаmple", "ui..example", "-ui.example", "ui.example..", "ui_x.example"} {
		if err := ValidUIHost(bad); err == nil {
			t.Errorf("ValidUIHost(%q) = nil, want error", bad)
		}
	}
}
