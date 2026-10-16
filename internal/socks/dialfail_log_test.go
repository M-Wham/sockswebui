package socks

import (
	"errors"
	"strings"
	"testing"
)

// TestDialFailedLogsRedactedError: a failed CONNECT dial logs its reason
// passed through Options.Redact; a secret in the error never reaches the
// log. Without a Redact func the error text is not logged at all.
func TestDialFailedLogsRedactedError(t *testing.T) {
	const secret = "hunter2-proxy-pass"
	redact := func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") }
	cases := []struct {
		name    string
		redact  func(string) string
		want    string
		notWant []string
	}{
		{"redactor", redact, "err=\"egress: hop socksB: auth user:[REDACTED] rejected\"", []string{secret}},
		{"no redactor", nil, "reason=dial_failed", []string{secret, "auth user"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := newFakeDialer()
			fd.err = errors.New("egress: hop socksB: auth user:" + secret + " rejected")
			var lc logCapture
			_, addr := startServer(t, Options{Dialer: fd, Logger: lc.logger(), Redact: tc.redact})
			c := dialServer(t, addr)
			if m := greet(t, c, 0x00); m != 0x00 {
				t.Fatalf("method = %#x, want 0x00", m)
			}
			mustWrite(t, c, requestBytes(0x01, 0x01, []byte{192, 0, 2, 7}, 80))
			if rep := readReply(t, c); rep != repGeneralFailure {
				t.Fatalf("rep = %#x, want general failure", rep)
			}
			lc.waitFor(t, "dial_failed")
			out := lc.waitFor(t, tc.want)
			for _, bad := range tc.notWant {
				if strings.Contains(out, bad) {
					t.Fatalf("log contains %q: %s", bad, out)
				}
			}
		})
	}
}
