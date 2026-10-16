package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestValidateEndpointHostSyntax: a proxy host is an IP literal or a DNS hostname. Control
// bytes and spaces must never reach the HTTP CONNECT request line.
func TestValidateEndpointHostSyntax(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	long253 := strings.Join([]string{label63, label63, label63, strings.Repeat("a", 61)}, ".")
	cases := []struct {
		name, host string
		ok         bool
	}{
		{"hostname", "proxy.example.test", true},
		{"single label", "proxy", true},
		{"trailing dot", "proxy.example.test.", true},
		{"digits and hyphen", "p-1.example.test", true},
		{"punycode IDN", "xn--bcher-kva.example", true},
		{"ipv4", "192.0.2.10", true},
		{"ipv6", "2001:db8::1", true},
		{"max length", long253, true},
		{"CRLF", "a.test\r\nX-Evil: 1", false},
		{"CR", "a.test\r", false},
		{"LF", "a.test\n", false},
		{"space", "a.test b", false},
		{"NUL", "a.test\x00", false},
		{"tab", "a.test\t", false},
		{"DEL", "a\x7f.test", false},
		{"unicode IDN unencoded", "bücher.example", false},
		{"overlong", long253 + "a", false},
		{"overlong label", label63 + "a.test", false},
		{"leading hyphen", "-a.test", false},
		{"trailing hyphen", "a-.test", false},
		{"empty label", "a..test", false},
		{"brackets", "[2001:db8::1]", false},
		{"colon port", "a.test:80", false},
		{"slash", "a.test/x", false},
		{"ipv6 zone", "fe80::1%eth0", false},
	}
	for _, kind := range []string{KindHTTP, KindSOCKS5} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				e := proxyEndpoint("ep", kind)
				e.Host = tc.host
				errs := ValidateEndpoint("endpoints[0]", e)
				if tc.ok && len(errs) != 0 {
					t.Fatalf("host %q: errors %v, want none", tc.host, errs)
				}
				if !tc.ok && !hasField(errs, "endpoints[0].host") {
					t.Fatalf("host %q: errors %v, want a host error", tc.host, errs)
				}
			})
		}
	}
	t.Run("Validate rejects it too", func(t *testing.T) {
		c := baseConfig(t)
		e := proxyEndpoint("ep-bad", KindHTTP)
		e.Host = "a.test\r\nX: y"
		c.Endpoints = append(c.Endpoints, e)
		field := fmt.Sprintf("endpoints[%d].host", len(c.Endpoints)-1)
		if !hasField(asValidationErrors(t, Validate(c)), field) {
			t.Fatal("Validate accepted a CRLF host")
		}
	})
}
