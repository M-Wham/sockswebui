package config

import (
	"strings"
	"testing"
)

func TestRouteRules(t *testing.T) {
	tests := []struct {
		name      string
		route     []string
		wantField string // empty = valid
		wantMsg   []string
	}{
		{name: "single wireguard", route: []string{"WG-SE"}},
		{name: "wireguard in wireguard", route: []string{"WG-SE", "WG-US"}},
		{name: "wireguard then proxy", route: []string{"WG-SE", "GatewayProxy"}},
		{name: "proxy only", route: []string{"GatewayProxy"}},
		{name: "proxy then proxy", route: []string{"GatewayProxy", "OtherProxy"}},
		{name: "empty is direct", route: []string{}},
		{name: "nil is direct", route: nil},
		{name: "four hops", route: []string{"WG-SE", "WG-US", "GatewayProxy", "OtherProxy"}},
		{
			name: "proxy before wireguard", route: []string{"GatewayProxy", "WG-SE"},
			wantField: "slots[0].route[1]", wantMsg: []string{"WireGuard", "must precede proxy hops"},
		},
		{
			name: "five hops", route: []string{"WG-SE", "WG-US", "WG-SE", "GatewayProxy", "OtherProxy"},
			wantField: "slots[0].route", wantMsg: []string{"5 hops", "4"},
		},
		{
			name: "unknown endpoint", route: []string{"WG-SE", "nope"},
			wantField: "slots[0].route[1]", wantMsg: []string{"unknown endpoint", "nope"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig(t)
			c.Slots[0].Route = tc.route
			err := Validate(c)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			ve := asValidationErrors(t, err)
			var found *FieldError
			for i := range ve {
				if ve[i].Field == tc.wantField {
					found = &ve[i]
				}
			}
			if found == nil {
				t.Fatalf("no error for field %q; got %v", tc.wantField, ve)
			}
			for _, m := range tc.wantMsg {
				if !strings.Contains(found.Message, m) {
					t.Errorf("message %q does not mention %q", found.Message, m)
				}
			}
		})
	}
}

func TestValidateRouteStandalone(t *testing.T) {
	c := baseConfig(t)
	ve := ValidateRoute("route", []string{"GatewayProxy", "WG-SE"}, c.Endpoints)
	if !hasField(ve, "route[1]") {
		t.Fatalf("ValidateRoute = %v, want error at route[1]", ve)
	}
	if ve := ValidateRoute("route", []string{"WG-SE", "GatewayProxy"}, c.Endpoints); len(ve) != 0 {
		t.Fatalf("ValidateRoute valid route = %v", ve)
	}
}
