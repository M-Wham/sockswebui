package importer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseFile(t *testing.T) {
	o := validOpts(t)
	data := []byte(confText(t, o))

	t.Run("Mullvad name sets relay, country and name", func(t *testing.T) {
		e, warns, err := ParseFile("mullvad_wireguard_linux_se_all/se-got-wg-001.conf", data)
		if err != nil {
			t.Fatalf("ParseFile: %v", err)
		}
		if e.MullvadRelay != "se-got-wg-001" || e.Country != "se" || e.Name != "se-got-wg-001" {
			t.Fatalf("relay/country/name = %q/%q/%q", e.MullvadRelay, e.Country, e.Name)
		}
		if e.PeerPublicKey != o.PublicKey || e.Endpoint != o.Endpoint {
			t.Fatalf("endpoint fields not parsed: %+v", e.Endpoint)
		}
		if len(warns) != 0 {
			t.Fatalf("warnings = %v; want none", warns)
		}
	})

	t.Run("any other WireGuard name accepted without warnings", func(t *testing.T) {
		for _, name := range []string{"home.conf", `C:\vpn\home.conf`, "dir/home.conf"} {
			e, warns, err := ParseFile(name, data)
			if err != nil {
				t.Fatalf("%s: ParseFile: %v", name, err)
			}
			if e.Name != "home" || e.MullvadRelay != "" || e.Country != "" {
				t.Fatalf("%s: name/relay/country = %q/%q/%q", name, e.Name, e.MullvadRelay, e.Country)
			}
			if e.PeerPublicKey != o.PublicKey {
				t.Fatalf("%s: endpoint not returned", name)
			}
			if len(warns) != 0 {
				t.Fatalf("%s: warnings = %v; want none", name, warns)
			}
		}
	})

	t.Run("malformed content passes T2.1 error through", func(t *testing.T) {
		bad := []byte("[Interface]\nPrivateKey = nope\n")
		_, wantErr := ParseConf(bad)
		e, warns, err := ParseFile("se-got-wg-001.conf", bad)
		if err == nil || wantErr == nil || err.Error() != wantErr.Error() {
			t.Fatalf("err = %v; want %v", err, wantErr)
		}
		if e.Kind != "" || warns != nil {
			t.Fatalf("on error got endpoint %+v, warnings %v; want zero values", e, warns)
		}
	})
}

func TestParseFileSanitizesName(t *testing.T) {
	data := []byte(confText(t, validOpts(t)))
	long := strings.Repeat("é", 100) + ".conf"
	cases := []struct{ file, want string }{
		{"ho\x00m\x1be\u0085.conf", "home"},
		{"\x01\x02.conf", "imported"},
		{".conf", "imported"},
		{"   .conf", "imported"},
		{long, strings.Repeat("é", maxNameRunes)},
	}
	for _, tc := range cases {
		e, _, err := ParseFile(tc.file, data)
		if err != nil {
			t.Fatalf("%q: %v", tc.file, err)
		}
		if e.Name != tc.want {
			t.Errorf("%q: name = %q, want %q", tc.file, e.Name, tc.want)
		}
		if utf8.RuneCountInString(e.Name) > maxNameRunes {
			t.Errorf("%q: name too long", tc.file)
		}
	}
}
