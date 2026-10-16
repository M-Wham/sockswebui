package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func hasFieldError(ve ValidationErrors, field, substr string) bool {
	for _, fe := range ve {
		if fe.Field == field && strings.Contains(fe.Message, substr) {
			return true
		}
	}
	return false
}

func TestSecretsOmittedSlotOwnList(t *testing.T) {
	c := baseConfig(t)
	c.Slots[0].SocksAuth = &SocksAuth{Username: "u"}
	c.Slots[0].SecretsOmitted = []string{OmittedSocksPassword}

	ve := asValidationErrors(t, Validate(c))
	if !hasFieldError(ve, "slots[0].enabled", "secrets missing") {
		t.Fatalf("want slots[0].enabled secrets missing, got %v", ve)
	}
	if !SlotSecretsMissing(c, c.Slots[0]) {
		t.Error("SlotSecretsMissing = false for own list")
	}

	c.Slots[0].Enabled = false
	if err := Validate(c); err != nil {
		t.Fatalf("disabled slot with missing secrets: %v", err)
	}

	c.Slots[0].Enabled = true
	c.Slots[0].SocksAuth.Password = "re-entered"
	c.Slots[0].SecretsOmitted = nil
	if err := Validate(c); err != nil {
		t.Fatalf("slot with empty lists: %v", err)
	}
}

func TestSecretsOmittedSlotRouteEndpoint(t *testing.T) {
	c := baseConfig(t)
	c.Endpoints[0].PrivateKey = ""
	c.Endpoints[0].SecretsOmitted = []string{OmittedPrivateKey}

	ve := asValidationErrors(t, Validate(c))
	if !hasFieldError(ve, "slots[0].enabled", "secrets missing") {
		t.Fatalf("want slots[0].enabled secrets missing, got %v", ve)
	}
	if !SlotSecretsMissing(c, c.Slots[0]) {
		t.Error("SlotSecretsMissing = false for route endpoint list")
	}
	// A slot not routed through the endpoint is unaffected.
	other := testSlot(2, 1081, "WG-US")
	if SlotSecretsMissing(c, other) {
		t.Error("SlotSecretsMissing = true for unrelated route")
	}

	c.Slots[0].Enabled = false
	if err := Validate(c); err != nil {
		t.Fatalf("disabled slot: %v", err)
	}
	c.Slots[0].Enabled = true
	c.Endpoints[0].PrivateKey = fakeKey(t)
	c.Endpoints[0].SecretsOmitted = nil
	if err := Validate(c); err != nil {
		t.Fatalf("after re-entry: %v", err)
	}
}

func TestSecretsOmittedSessionTokenRule(t *testing.T) {
	c := baseConfig(t)
	ep := &c.Endpoints[2] // GatewayProxy socks5
	ep.Username = "acct-fixed-user"
	ep.Password = ""
	ep.Session = Session{Charset: SessionAlnum, Length: 10, CurrentID: "abcDEF1234"}
	ep.SecretsOmitted = []string{OmittedPassword}
	if err := Validate(c); err != nil {
		t.Fatalf("token held in omitted password: %v", err)
	}

	// The token in the username also passes while password is listed.
	ep.Username = "acct-{session}"
	if err := Validate(c); err != nil {
		t.Fatalf("token in username, password listed: %v", err)
	}

	// Once password leaves the list the token-count rule applies again.
	ep.Username = "acct-fixed-user"
	ep.SecretsOmitted = nil
	ve := asValidationErrors(t, Validate(c))
	if !hasFieldError(ve, "endpoints[2].session", "exactly once") {
		t.Fatalf("want token-count error, got %v", ve)
	}
}

func TestSecretsOmittedErrorsNoSecrets(t *testing.T) {
	c := baseConfig(t)
	ep := &c.Endpoints[2]
	ep.Username = "secretuser-XYZ"
	ep.Password = "secretpass-QRS"
	c.Slots[0].Route = []string{"GatewayProxy"}
	ep.SecretsOmitted = []string{OmittedPassword}
	err := Validate(c)
	ve := asValidationErrors(t, err)
	raw, _ := json.Marshal(ve)
	for _, s := range []string{"secretuser-XYZ", "secretpass-QRS"} {
		if strings.Contains(err.Error(), s) || strings.Contains(string(raw), s) {
			t.Fatalf("error echoes %q: %s", s, raw)
		}
	}
}

func TestSecretsOmittedJSONAndClone(t *testing.T) {
	c := baseConfig(t)
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "secrets_omitted") {
		t.Fatalf("empty list serialised: %s", raw)
	}
	c.Endpoints[0].SecretsOmitted = []string{OmittedPrivateKey}
	c.Slots[0].SecretsOmitted = []string{OmittedSocksPassword}
	cl := cloneConfig(c)
	cl.Endpoints[0].SecretsOmitted[0] = "x"
	cl.Slots[0].SecretsOmitted[0] = "x"
	if c.Endpoints[0].SecretsOmitted[0] != OmittedPrivateKey || c.Slots[0].SecretsOmitted[0] != OmittedSocksPassword {
		t.Fatal("cloneConfig shares SecretsOmitted backing arrays")
	}
}
