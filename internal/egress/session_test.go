package egress

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

// countingReader is a deterministic byte source: 0, 1, 2, ... (wrapping).
type countingReader struct{ n byte }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.n
		r.n += 7 // stride keeps output varied but reproducible
	}
	return len(p), nil
}

// sessionFixtures is the per-provider session-ID reference table, verbatim.
var sessionFixtures = []struct {
	provider, field, template, charset string
	length                             int
	want                               string
}{
	{"GatewayProxy", "username", "USER-country-GB-ssid-{session}", "alnum", 10, `^USER-country-GB-ssid-[A-Za-z0-9]{10}$`},
	{"BrightData", "username", "brd-customer-ID-zone-ZONE-session-{session}", "alnum", 10, `^brd-customer-ID-zone-ZONE-session-[A-Za-z0-9]{10}$`},
	{"Oxylabs", "username", "customer-USER-cc-DE-sessid-{session}-sesstime-10", "alnum", 10, `^customer-USER-cc-DE-sessid-[A-Za-z0-9]{10}-sesstime-10$`},
	{"Decodo", "username", "user-USER-country-us-session-{session}-sessionduration-30", "alnum", 10, `^user-USER-country-us-session-[A-Za-z0-9]{10}-sessionduration-30$`},
	{"SOAX", "username", "package-ID-country-us-sessionid-{session}-sessionlength-600", "alnum", 10, `^package-ID-country-us-sessionid-[A-Za-z0-9]{10}-sessionlength-600$`},
	{"Geonode", "username", "USER-session-{session}", "alnum", 10, `^USER-session-[A-Za-z0-9]{10}$`},
	{"Shifter", "username", "customer-USER-sid-{session}-ttl-120", "alnum", 10, `^customer-USER-sid-[A-Za-z0-9]{10}-ttl-120$`},
	{"DataImpulse", "username", "USER__sid.{session}", "alnum", 10, `^USER__sid\.[A-Za-z0-9]{10}$`},
	{"HexProxies", "username", "USER-session-{session}", "alnum", 10, `^USER-session-[A-Za-z0-9]{10}$`},
	{"ProxyScrape", "username", "USER-session-{session}-lifetime-5", "alnum", 10, `^USER-session-[A-Za-z0-9]{10}-lifetime-5$`},
	{"Webshare", "username", "USER-us-{session}", "digits", 4, `^USER-us-[1-9][0-9]{3}$`},
	{"PacketStream", "password", "AUTHKEY_country-US_session-{session}", "alnum", 10, `^AUTHKEY_country-US_session-[A-Za-z0-9]{10}$`},
	{"IPRoyal", "password", "PASS_session-{session}_lifetime-30m", "alnum", 8, `^PASS_session-[A-Za-z0-9]{8}_lifetime-30m$`},
	{"NetNut", "password", "PASS-res-us-sid-{session}", "digits", 9, `^PASS-res-us-sid-[1-9][0-9]{8}$`},
}

func TestSessionFixtures(t *testing.T) {
	if len(sessionFixtures) != 14 {
		t.Fatalf("fixture rows = %d, want 14", len(sessionFixtures))
	}
	for _, f := range sessionFixtures {
		t.Run(f.provider, func(t *testing.T) {
			id, err := config.NewSessionID(&countingReader{}, f.charset, f.length)
			if err != nil {
				t.Fatal(err)
			}
			ep := config.Endpoint{Kind: config.KindSOCKS5, Username: "USER", Password: "PASS",
				Session: config.Session{Charset: f.charset, Length: f.length, CurrentID: id}}
			if f.field == "username" {
				ep.Username = f.template
			} else {
				ep.Password = f.template
			}
			user, pass := RenderCredentials(ep)
			got, other, otherWant := user, pass, "PASS"
			if f.field == "password" {
				got, other, otherWant = pass, user, "USER"
			}
			if !regexp.MustCompile(f.want).MatchString(got) {
				t.Errorf("%s rendered %q, want match %s", f.field, got, f.want)
			}
			if other != otherWant {
				t.Errorf("untemplated field = %q, want %q", other, otherWant)
			}
			if !strings.Contains(got, id) {
				t.Errorf("rendered %q does not contain id %q", got, id)
			}
		})
	}
}

// dialEcho opens one connection to echo.rig.invalid:80 through ch.
func dialEcho(t *testing.T, ch Chain) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := ch.DialContext(ctx, "tcp", echoHost+":80")
	if err == nil {
		_ = c.Close()
	}
	return err
}

func TestSessionRender(t *testing.T) {
	echo := startEcho(t)
	hosts := map[string]string{echoHost: echo}
	prefix := func(p string) func(string) bool {
		return func(u string) bool { return strings.HasPrefix(u, p) }
	}
	sess := config.Session{Charset: config.SessionAlnum, Length: 10, CurrentID: "AbC123xyZ9"}

	cases := []struct {
		name, kind, user, pass   string
		session                  config.Session
		wantUser, wantPass, cfgP string
	}{
		{"socks5-user-template", config.KindSOCKS5, "acct-sid-{session}", "pw", sess, "acct-sid-AbC123xyZ9", "pw", "pw"},
		{"http-user-template", config.KindHTTP, "acct-sid-{session}", "pw", sess, "acct-sid-AbC123xyZ9", "pw", "pw"},
		{"socks5-pass-template", config.KindSOCKS5, "acct", "PASS_session-{session}", sess, "acct", "PASS_session-AbC123xyZ9", "PASS_session-AbC123xyZ9"},
		{"http-pass-template", config.KindHTTP, "acct", "PASS_session-{session}", sess, "acct", "PASS_session-AbC123xyZ9", "PASS_session-AbC123xyZ9"},
		{"socks5-no-template", config.KindSOCKS5, "acct", "p{ss}", config.Session{}, "acct", "p{ss}", "p{ss}"},
		{"http-no-template", config.KindHTTP, "acct", "p{ss}", config.Session{}, "acct", "p{ss}", "p{ss}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testrig.UpstreamConfig{Name: tc.name, Password: tc.cfgP, Hosts: hosts, AcceptUser: prefix("acct")}
			var addr string
			var creds func() []testrig.Credential
			if tc.kind == config.KindSOCKS5 {
				up := testrig.NewLoopbackSOCKS5(t, cfg)
				addr, creds = up.Addr(), up.Credentials
			} else {
				up := testrig.NewLoopbackHTTP(t, cfg)
				addr, creds = up.Addr(), up.Credentials
			}
			ep := proxyEP(t, "up", tc.kind, addr, tc.user, tc.pass)
			ep.Session = tc.session
			eps := []config.Endpoint{ep}
			ch := build(t, []string{"up"}, eps)
			if err := dialEcho(t, ch); err != nil {
				t.Fatalf("dial: %v", err)
			}
			got := creds()
			want := testrig.Credential{User: tc.wantUser, Pass: tc.wantPass}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("upstream received %+v, want [%+v]", got, want)
			}
			if eps[0].Username != tc.user || eps[0].Password != tc.pass {
				t.Errorf("stored endpoint mutated: user=%q", eps[0].Username)
			}
		})
	}
}

func TestSessionRender255(t *testing.T) {
	up := testrig.NewLoopbackSOCKS5(t, testrig.UpstreamConfig{Name: "long",
		AcceptUser: func(string) bool { return true }})
	// Stored 250 bytes (fits), rendered 240+1+32 = 273 bytes (does not).
	long := strings.Repeat("u", 240) + "-{session}"
	id := strings.Repeat("AbC123xyZ9", 4)[:32]
	ep := proxyEP(t, "up", config.KindSOCKS5, up.Addr(), long, "topsecretpass")
	ep.Session = config.Session{Charset: config.SessionAlnum, Length: 32, CurrentID: id}
	// First line of defence: Build validates (config.Validate) and refuses it.
	if ch, err := Build([]string{"up"}, []config.Endpoint{ep}); err == nil {
		_ = ch.Close()
		t.Fatal("Build accepted a 273-byte rendered username")
	}
	// Defence in depth: the hop itself refuses, before sending credentials.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := socks5Hop(ep, deviceDial)(ctx, "tcp", echoHost+":80")
	if c != nil {
		_ = c.Close()
	}
	if err == nil {
		t.Fatal("dial with 273-byte rendered username succeeded")
	}
	msg := err.Error()
	for _, s := range []string{"uuuuuuuu", id[:10], "topsecretpass", "{session}"} {
		if strings.Contains(msg, s) {
			t.Errorf("error %q leaks %q", msg, s)
		}
	}
	if len(up.Credentials()) != 0 {
		t.Errorf("upstream received credentials: %d", len(up.Credentials()))
	}
}
