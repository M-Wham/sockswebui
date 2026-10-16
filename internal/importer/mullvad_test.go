package importer

import "testing"

func TestMullvadName(t *testing.T) {
	cases := []struct {
		file, relay, country string
		ok                   bool
	}{
		{"se-got-wg-001.conf", "se-got-wg-001", "se", true},
		{"us-nyc-wg-301.conf", "us-nyc-wg-301", "us", true},
		{"mullvad_wireguard_linux_se_all/se-got-wg-001.conf", "se-got-wg-001", "se", true},
		{`mullvad_wireguard_windows_se_all\se-got-wg-001.conf`, "se-got-wg-001", "se", true},
		{"SE-GOT-WG-001.conf", "se-got-wg-001", "se", true},
		{"se-got-wg-001.CONF", "se-got-wg-001", "se", true},
		{"home.conf", "", "", false},
		{"se-got-001.conf", "", "", false},
		{"se-got-wg-001.txt", "", "", false},
		{"se-got-wg-.conf", "", "", false},
		{"swe-got-wg-001.conf", "", "", false},
		{"se-go-wg-001.conf", "", "", false},
		{"se-got-wg-001a.conf", "", "", false},
		{"x-se-got-wg-001.conf", "", "", false},
		{"se-got-wg-001.conf/home.conf", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			relay, country, ok := MullvadName(tc.file)
			if relay != tc.relay || country != tc.country || ok != tc.ok {
				t.Fatalf("MullvadName(%q) = (%q, %q, %v); want (%q, %q, %v)",
					tc.file, relay, country, ok, tc.relay, tc.country, tc.ok)
			}
		})
	}
}
